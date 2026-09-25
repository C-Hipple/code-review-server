import { useState, useEffect, useRef, useCallback } from 'react';
import { LspClient, LspHover, LspLocation, LocationLines, fetchLocationLines } from '../lsp';
import { API_BASE } from '../api';
import { hasHoverContent, mergeLocationLines, toLocationList } from '../lsp_utils';

export type LspSection = 'hover' | 'definitions' | 'typeDefinitions' | 'refs';

export interface LspData {
    hover: LspHover | null;
    refs: LspLocation[] | null;
    definitions: LspLocation[] | null;
    typeDefinitions: LspLocation[] | null;
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

interface UseLspDiffOptions {
    mode: 'diff';
    repoPath: string | null;
    worktreePath: string;
    repoName: string;
    prNumber: number;
    diffContent: string;
    enabled?: boolean;
}

interface UseLspFileOptions {
    mode: 'file';
    repoPath: string;
    filePath: string;
    fileContent: string;
    languageId: string;
    enabled?: boolean;
}

export type UseLspOptions = UseLspDiffOptions | UseLspFileOptions;

export interface UseLspResult {
    available: boolean | null;
    connected: boolean;
    /**
     * Looks up hover, definition, type definition and references at a
     * position. lspData fills in as each answer arrives — it stays null until
     * the first non-empty one, or until the query has run long enough to be
     * worth saying it's loading — so a popover can open on the fast answers
     * while references are still being found.
     */
    query: (line: number, col: number) => Promise<LspQueryResult>;
    lspData: LspData | null;
    clearData: () => void;
}

// A query that has shown nothing after this long opens the popover in its
// loading state, so a slow lookup visibly registers the click. A click on
// whitespace is answered (empty) well within it and flashes nothing.
const LOADING_POPOVER_DELAY_MS = 300;

// A running server answers `initialize` at once (the bun server shares them);
// a new diff-lsp first starts and initializes a backend per language.
const INITIALIZE_TIMEOUT_MS = 60_000;

// Map Prism language IDs to LSP language IDs used by the server
const prismToLspLanguage: Record<string, string> = {
    typescript: 'typescript',
    javascript: 'javascript',
    tsx: 'tsx',
    jsx: 'jsx',
    go: 'go',
    rust: 'rust',
    python: 'python',
};

// Notifications sent before the server has answered `initialize` are dropped
// (diff-lsp's tower-lsp does this), so the handshake waits for the answer.
async function initialize(client: LspClient, rootPath: string) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
        await Promise.race([
            client.initialize(rootPath),
            new Promise((_, reject) => {
                timer = setTimeout(
                    () => reject(new Error('LSP initialize timed out')),
                    INITIALIZE_TIMEOUT_MS
                );
            }),
        ]);
    } finally {
        clearTimeout(timer);
    }
    client.initialized();
}

export function useLsp(options: UseLspOptions): UseLspResult {
    const [available, setAvailable] = useState<boolean | null>(null);
    const [connected, setConnected] = useState(false);
    const [lspData, setLspData] = useState<LspData | null>(null);

    const clientRef = useRef<LspClient | null>(null);
    const uriRef = useRef<string | null>(null);
    const rootsRef = useRef<string[]>([]);
    const initializingRef = useRef(false);
    // The query in flight; aborting it cancels its requests on the server.
    const queryRef = useRef<AbortController | null>(null);

    const enabled = options.enabled !== false;

    const clearData = useCallback(() => {
        queryRef.current?.abort();
        queryRef.current = null;
        setLspData(null);
    }, []);

    // Diff mode effect
    useEffect(() => {
        if (options.mode !== 'diff') return;
        if (!enabled || !options.diffContent || !options.repoPath) {
            setAvailable(null);
            return;
        }

        let cancelled = false;

        const init = async () => {
            if (initializingRef.current) return;
            initializingRef.current = true;
            let client: LspClient | null = null;

            try {
                // Check diff-lsp availability
                const res = await fetch(`${API_BASE}/api/check-lsp`);
                const data = await res.json();
                if (cancelled) return;

                setAvailable(data.available);
                if (!data.available || !options.repoPath) return;

                client = new LspClient();

                // Prepare context for diff-lsp BEFORE connecting: connecting
                // spawns the diff-lsp process, which reads its init params
                // from this tempfile at startup.
                const path = await client.prepareContext(
                    options.repoName,
                    options.repoPath,
                    `PR #${options.prNumber}`,
                    'code-review',
                    options.diffContent,
                    options.worktreePath
                );
                if (cancelled) {
                    client.disconnect();
                    return;
                }

                client.connect(`/api/lsp?tempfile=${encodeURIComponent(path)}`);

                const uri = `file://${path}`;
                uriRef.current = uri;

                await initialize(client, '/');
                if (cancelled) {
                    client.disconnect();
                    return;
                }
                client.didOpen(uri, 'diff', 1, options.diffContent);

                clientRef.current = client;
                rootsRef.current = [options.worktreePath, options.repoPath].filter(Boolean);
                setConnected(true);
            } catch (e) {
                console.error('LSP init failed', e);
                client?.disconnect();
            } finally {
                initializingRef.current = false;
            }
        };

        init();

        return () => {
            cancelled = true;
            queryRef.current?.abort();
            if (clientRef.current) {
                clientRef.current.disconnect();
                clientRef.current = null;
            }
            uriRef.current = null;
            setConnected(false);
            setLspData(null);
            initializingRef.current = false;
        };
    }, [
        options.mode === 'diff' ? options.diffContent : null,
        options.mode === 'diff' ? options.repoPath : null,
        options.mode === 'diff' ? options.repoName : null,
        options.mode === 'diff' ? options.prNumber : null,
        enabled,
    ]);

    // File mode effect
    useEffect(() => {
        if (options.mode !== 'file') return;

        const lspLang = prismToLspLanguage[options.languageId];
        if (!enabled || !options.fileContent || !lspLang) {
            setAvailable(lspLang ? null : false);
            return;
        }

        let cancelled = false;

        const init = async () => {
            if (initializingRef.current) return;
            initializingRef.current = true;
            let client: LspClient | null = null;

            try {
                // Check language server availability
                const res = await fetch(`${API_BASE}/api/check-lsp-file?lang=${lspLang}`);
                const data = await res.json();
                if (cancelled) return;

                setAvailable(data.available);
                if (!data.available) return;

                client = new LspClient();
                // The root lets the bun server hand every viewer of this repo
                // the same running server.
                client.connect(
                    `/api/lsp-file?lang=${lspLang}&root=${encodeURIComponent(options.repoPath)}`
                );

                // Initialize with repo path as root
                await initialize(client, options.repoPath);
                if (cancelled) {
                    client.disconnect();
                    return;
                }

                // Construct the file URI
                const filePath = options.filePath.startsWith('/')
                    ? options.filePath
                    : `${options.repoPath}/${options.filePath}`;
                const uri = `file://${filePath}`;
                uriRef.current = uri;

                client.didOpen(uri, lspLang, 1, options.fileContent);

                clientRef.current = client;
                rootsRef.current = [options.repoPath].filter(Boolean);
                setConnected(true);
            } catch (e) {
                console.error('LSP init failed', e);
                client?.disconnect();
            } finally {
                initializingRef.current = false;
            }
        };

        init();

        return () => {
            cancelled = true;
            queryRef.current?.abort();
            if (clientRef.current) {
                clientRef.current.disconnect();
                clientRef.current = null;
            }
            uriRef.current = null;
            setConnected(false);
            setLspData(null);
            initializingRef.current = false;
        };
    }, [
        options.mode === 'file' ? options.filePath : null,
        options.mode === 'file' ? options.languageId : null,
        options.mode === 'file' ? options.repoPath : null,
        options.mode === 'file' ? options.fileContent : null,
        enabled,
    ]);

    const query = useCallback(
        async (line: number, col: number): Promise<LspQueryResult> => {
            queryRef.current?.abort();
            setLspData(null);
            const client = clientRef.current;
            const uri = uriRef.current;
            if (!client || !uri) return 'empty';

            const controller = new AbortController();
            queryRef.current = controller;
            const { signal } = controller;

            let queryLine = line;
            let queryCol = col;

            if (options.mode === 'diff') {
                // Header is 5 lines followed by a newline, so diff starts at line 6 (0-indexed)
                queryLine = line + 6;
                // Add 1 to col to account for the diff prefix (+/- / space)
                queryCol = col + 1;
            }

            let data: LspData = {
                hover: null,
                refs: null,
                definitions: null,
                typeDefinitions: null,
                loading: { hover: true, definitions: true, typeDefinitions: true, refs: true },
                lines: {},
                roots: rootsRef.current,
            };
            let found = false;
            let shown = false;
            const show = () => {
                if (signal.aborted) return;
                shown = true;
                setLspData(data);
            };
            const loadingTimer = setTimeout(show, LOADING_POPOVER_DELAY_MS);

            const settle = async <T>(
                section: LspSection,
                request: Promise<T>,
                normalize: (res: T) => LspData[LspSection]
            ) => {
                let value: LspData[LspSection] = null;
                try {
                    value = normalize(await request);
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

            // Sent in this order on purpose: diff-lsp asks its backend one
            // request at a time, and references is the slow one, so hover and
            // definitions go first rather than waiting behind it.
            await Promise.all([
                settle('hover', client.hover(uri, queryLine, queryCol, signal), hover =>
                    hasHoverContent(hover) ? hover : null
                ),
                settle(
                    'definitions',
                    client.definition(uri, queryLine, queryCol, signal),
                    toLocationList
                ),
                settle(
                    'typeDefinitions',
                    client.typeDefinition(uri, queryLine, queryCol, signal),
                    toLocationList
                ),
                settle('refs', client.references(uri, queryLine, queryCol, signal), toLocationList),
            ]);
            clearTimeout(loadingTimer);

            if (signal.aborted) return 'stale';
            if (queryRef.current === controller) queryRef.current = null;
            if (!found && !shown) {
                setLspData(null);
                return 'empty';
            }
            return 'shown';
        },
        [options.mode]
    );

    return { available, connected, query, lspData, clearData };
}
