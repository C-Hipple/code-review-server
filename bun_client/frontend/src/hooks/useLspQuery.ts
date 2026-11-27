import { useCallback, useRef, useState } from 'react';
import {
    fetchLocationLines,
    type LocationLines,
    type LspClient,
    type LspHover,
    type LspLocation,
} from '../lsp';
import {
    hasHoverContent,
    isRetryableLspError,
    mergeLocationLines,
    toLocationList,
} from '../lsp_utils';

export type LspSection = 'hover' | 'definitions' | 'typeDefinitions' | 'implementations' | 'refs';

export interface LspData {
    hover: LspHover | null;
    refs: LspLocation[] | null;
    definitions: LspLocation[] | null;
    typeDefinitions: LspLocation[] | null;
    implementations: LspLocation[] | null;
    /** Sections whose answer hasn't come back yet. */
    loading: Record<LspSection, boolean>;
    /** What's on each location's line; filled in after the locations arrive. */
    lines: LocationLines;
    /** Workspace roots that location paths are shown relative to. */
    roots: string[];
}

/**
 * How a query ended: with its answers on show in lspData, with nothing at
 * that position (lspData cleared), or cut short by a later query or
 * clearData(). A query that turns up nothing after its popover has opened
 * to say it's loading counts as shown: the popover says nothing was found.
 */
export type LspQueryResult = 'shown' | 'empty' | 'stale';

/** The server a query goes to, the document it's about, and where paths are shown from. */
export interface LspQueryTarget {
    client: LspClient;
    uri: string;
    roots: string[];
}

// A query that has shown nothing after this long opens the popover in its
// loading state, so a slow lookup visibly registers the click. A click on
// whitespace is answered (empty) well within it and flashes nothing.
const LOADING_POPOVER_DELAY_MS = 300;

// rust-analyzer answers "content modified" while it's still indexing, and a
// server may cancel a request it would rather be asked again; either is worth
// a few more tries before showing nothing.
const RETRY_DELAY_MS = 500;
const MAX_RETRIES = 6;

/** A request, asked again while the server says to try later. */
export async function withRetries<T>(send: () => Promise<T>, signal?: AbortSignal): Promise<T> {
    for (let attempt = 0; ; attempt++) {
        try {
            return await send();
        } catch (e) {
            if (signal?.aborted || attempt >= MAX_RETRIES || !isRetryableLspError(e)) throw e;
        }
        await new Promise(resolve => setTimeout(resolve, RETRY_DELAY_MS));
        if (signal?.aborted) throw signal.reason;
    }
}

const ALL_SECTIONS: LspSection[] = [
    'hover',
    'definitions',
    'typeDefinitions',
    'implementations',
    'refs',
];

function request(
    target: LspQueryTarget,
    section: LspSection,
    line: number,
    col: number,
    signal: AbortSignal
): Promise<unknown> {
    const { client, uri } = target;
    switch (section) {
        case 'hover':
            return client.hover(uri, line, col, signal);
        case 'definitions':
            return client.definition(uri, line, col, signal);
        case 'typeDefinitions':
            return client.typeDefinition(uri, line, col, signal);
        case 'implementations':
            return client.implementation(uri, line, col, signal);
        case 'refs':
            return client.references(uri, line, col, signal);
    }
}

function normalize(section: LspSection, res: unknown): LspData[LspSection] {
    if (section === 'hover') {
        const hover = res as LspHover | null;
        return hasHoverContent(hover) ? hover : null;
    }
    return toLocationList(res as Parameters<typeof toLocationList>[0]);
}

export interface UseLspQueryResult {
    /**
     * Looks up the sections at a position. lspData fills in as each answer
     * arrives — it stays null until the first non-empty one, or until the
     * query has run long enough to be worth saying it's loading — so a
     * popover can open on the fast answers while references are still being
     * found.
     */
    query: (line: number, col: number) => Promise<LspQueryResult>;
    lspData: LspData | null;
    clearData: () => void;
}

/**
 * The click-for-info query both LSP views share. `sections` are asked in the
 * order given: diff-lsp asks its backend one request at a time, and
 * references is the slow one, so it goes last rather than holding up hover
 * and definitions.
 */
export function useLspQuery(
    getTarget: () => LspQueryTarget | null,
    sections: LspSection[]
): UseLspQueryResult {
    const [lspData, setLspData] = useState<LspData | null>(null);
    // The query in flight; aborting it cancels its requests on the server.
    const queryRef = useRef<AbortController | null>(null);
    const getTargetRef = useRef(getTarget);
    getTargetRef.current = getTarget;
    const sectionsKey = sections.join(',');

    const clearData = useCallback(() => {
        queryRef.current?.abort();
        queryRef.current = null;
        setLspData(null);
    }, []);

    const query = useCallback(
        async (line: number, col: number): Promise<LspQueryResult> => {
            queryRef.current?.abort();
            setLspData(null);
            const target = getTargetRef.current();
            if (!target) return 'empty';

            const controller = new AbortController();
            queryRef.current = controller;
            const { signal } = controller;
            const asked = sectionsKey.split(',') as LspSection[];

            const loading = Object.fromEntries(
                ALL_SECTIONS.map(s => [s, asked.includes(s)])
            ) as Record<LspSection, boolean>;
            let data: LspData = {
                hover: null,
                refs: null,
                definitions: null,
                typeDefinitions: null,
                implementations: null,
                loading,
                lines: {},
                roots: target.roots,
            };
            let found = false;
            let shown = false;
            const show = () => {
                if (signal.aborted) return;
                shown = true;
                setLspData(data);
            };
            const loadingTimer = setTimeout(show, LOADING_POPOVER_DELAY_MS);

            const settle = async (section: LspSection) => {
                let value: LspData[LspSection] = null;
                try {
                    value = normalize(
                        section,
                        await withRetries(() => request(target, section, line, col, signal), signal)
                    );
                } catch (e) {
                    if (signal.aborted) return;
                    console.error(`LSP ${section} request failed`, e);
                }
                if (signal.aborted) return;
                data = {
                    ...data,
                    [section]: value,
                    loading: { ...data.loading, [section]: false },
                };
                if (value) found = true;
                if (value || shown) show();

                if (Array.isArray(value)) {
                    try {
                        const lines = await fetchLocationLines(value);
                        data = { ...data, lines: mergeLocationLines(data.lines, lines) };
                        show();
                    } catch (e) {
                        console.error('Failed to read the lines LSP locations point at', e);
                    }
                }
            };

            await Promise.all(asked.map(settle));
            clearTimeout(loadingTimer);

            if (signal.aborted) return 'stale';
            if (queryRef.current === controller) queryRef.current = null;
            if (!found && !shown) {
                setLspData(null);
                return 'empty';
            }
            return 'shown';
        },
        [sectionsKey]
    );

    return { query, lspData, clearData };
}
