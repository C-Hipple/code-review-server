import { useCallback, useEffect, useRef, useState } from 'react';
import { API_BASE } from '../api';
import { LspClient, type LspLocation, type LspSymbolInformation } from '../lsp';
import {
    applyProgress,
    keepsLspSession,
    type LspProgress,
    type OutlineSymbol,
    outlineSymbols,
    progressSummary,
    toLocationList,
} from '../lsp_utils';
import { initializeClient } from './useLsp';
import { type LspQueryTarget, type LspSection, useLspQuery, withRetries } from './useLspQuery';

/** What /api/lsp-workspace says about a file (lsp_languages.ts on the bridge). */
export interface LspWorkspace {
    available: boolean;
    lang: string;
    languageId: string;
    root: string;
    server: string;
    outside: boolean;
}

export async function fetchLspWorkspace(checkout: string, file: string): Promise<LspWorkspace> {
    const res = await fetch(`${API_BASE}/api/lsp-workspace`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ checkout, file }),
    });
    if (!res.ok) throw new Error(`Failed to look up the language server: ${res.status}`);
    return res.json();
}

interface Session {
    client: LspClient;
    lang: string;
    root: string;
    server: string;
    /** The document open on it, if any. */
    uri: string | null;
}

interface UseFileLspOptions {
    /** The PR's checkout (worktree, else the clone): where relative paths start. */
    checkout: string;
    /** Absolute path of the file on show. */
    filePath: string;
    /** That file's text; null while it loads. */
    text: string | null;
    enabled: boolean;
}

export interface UseFileLspResult {
    /** Whether the file has a language server: null until known. */
    available: boolean | null;
    connected: boolean;
    /** Which server, e.g. "gopls", and where it's rooted. */
    server: string;
    root: string;
    /** What the server is busy with ("Indexing 40%"), or null when idle. */
    progress: string | null;
    query: ReturnType<typeof useLspQuery>['query'];
    lspData: ReturnType<typeof useLspQuery>['lspData'];
    clearData: () => void;
    /** Where the symbol at a 0-based position is defined. */
    definitionAt: (line: number, col: number) => Promise<LspLocation[] | null>;
    /** The open file's declarations, in file order; null until they arrive. */
    symbols: OutlineSymbol[] | null;
    /** Declarations matching `query` anywhere in the workspace. */
    searchWorkspace: (query: string, signal?: AbortSignal) => Promise<LspSymbolInformation[]>;
}

// Hover first; implementations before references, the slowest.
const FILE_SECTIONS: LspSection[] = [
    'hover',
    'definitions',
    'typeDefinitions',
    'implementations',
    'refs',
];

/**
 * The code viewer's language server: the one an editor would start for the
 * file, rooted at its project (see lsp_languages.ts on the bridge). Moving
 * to another file of the same project keeps the connection and swaps the
 * open document; a file of another language or project connects to that
 * one's server, which the bridge keeps running between viewers.
 */
export function useFileLsp({
    checkout,
    filePath,
    text,
    enabled,
}: UseFileLspOptions): UseFileLspResult {
    const [available, setAvailable] = useState<boolean | null>(null);
    const [connected, setConnected] = useState(false);
    const [info, setInfo] = useState({ server: '', root: '' });
    const [progress, setProgress] = useState<Map<string | number, LspProgress>>(new Map());
    const [symbols, setSymbols] = useState<OutlineSymbol[] | null>(null);

    const sessionRef = useRef<Session | null>(null);
    // The document the viewer shows. Until the server has it open, nothing
    // is asked about it: positions in it mean nothing in the last one.
    const wantedUriRef = useRef('');
    wantedUriRef.current = filePath ? `file://${filePath}` : '';
    // Each sync waits for the one before it, so a quick run of file changes
    // can't open two connections or leave the wrong document open.
    const queueRef = useRef<Promise<void>>(Promise.resolve());
    const generationRef = useRef(0);

    /** The session, when the document on show is the one open on it. */
    const currentSession = useCallback((): (Session & { uri: string }) | null => {
        const session = sessionRef.current;
        if (!session?.uri || session.uri !== wantedUriRef.current) return null;
        return session as Session & { uri: string };
    }, []);

    const getTarget = useCallback((): LspQueryTarget | null => {
        const session = currentSession();
        if (!session) return null;
        return { client: session.client, uri: session.uri, roots: [checkout, session.root] };
    }, [checkout, currentSession]);
    const { query, lspData, clearData } = useLspQuery(getTarget, FILE_SECTIONS);

    const closeSession = useCallback(() => {
        sessionRef.current?.client.disconnect();
        sessionRef.current = null;
        setConnected(false);
        setProgress(new Map());
    }, []);

    useEffect(() => {
        if (!enabled || text === null || !filePath) return;
        const generation = ++generationRef.current;
        const stale = () => generation !== generationRef.current;

        const sync = async () => {
            if (stale()) return;
            setSymbols(null);
            const workspace = await fetchLspWorkspace(checkout, filePath);
            if (stale()) return;
            if (!workspace.available) {
                setAvailable(false);
                setConnected(false);
                const session = sessionRef.current;
                if (session?.uri) {
                    session.client.didClose(session.uri);
                    session.uri = null;
                }
                return;
            }
            setAvailable(true);

            let session = sessionRef.current;
            const root = keepsLspSession(session, workspace, filePath)
                ? (session as Session).root
                : workspace.root;
            if (!session || session.lang !== workspace.lang || session.root !== root) {
                closeSession();
                const client = new LspClient();
                client.setNotificationHandler((method, params) => {
                    if (method === '$/progress')
                        setProgress(running => applyProgress(running, params));
                });
                // The root lets the bridge hand every viewer of this project
                // the same running server.
                client.connect(
                    `/api/lsp-file?lang=${encodeURIComponent(workspace.lang)}&root=${encodeURIComponent(root)}`
                );
                try {
                    await initializeClient(client, root, true);
                } catch (e) {
                    client.disconnect();
                    throw e;
                }
                if (stale()) {
                    client.disconnect();
                    return;
                }
                session = {
                    client,
                    lang: workspace.lang,
                    root,
                    server: workspace.server,
                    uri: null,
                };
                sessionRef.current = session;
                setInfo({ server: workspace.server, root });
            }

            const uri = `file://${filePath}`;
            if (session.uri !== uri) {
                if (session.uri) session.client.didClose(session.uri);
                session.client.didOpen(uri, workspace.languageId, 1, text);
                session.uri = uri;
            }
            setConnected(true);

            try {
                const outline = outlineSymbols(
                    await withRetries(() => (session as Session).client.documentSymbol(uri))
                );
                if (!stale()) setSymbols(outline);
            } catch (e) {
                if (!stale()) setSymbols([]);
                console.error('LSP documentSymbol failed', e);
            }
        };

        queueRef.current = queueRef.current.then(sync).catch(e => {
            console.error('LSP init failed', e);
            if (!stale()) closeSession();
        });
    }, [checkout, filePath, text, enabled, closeSession]);

    // A viewer that closes lets go of its server; the bridge keeps it
    // running for the next one.
    useEffect(
        () => () => {
            generationRef.current++;
            sessionRef.current?.client.disconnect();
            sessionRef.current = null;
        },
        []
    );

    const definitionAt = useCallback(
        async (line: number, col: number) => {
            const session = currentSession();
            if (!session) return null;
            try {
                return toLocationList(
                    await withRetries(() => session.client.definition(session.uri, line, col))
                );
            } catch (e) {
                console.error('LSP definition request failed', e);
                return null;
            }
        },
        [currentSession]
    );

    const searchWorkspace = useCallback(async (q: string, signal?: AbortSignal) => {
        const session = sessionRef.current;
        if (!session || !q.trim()) return [];
        const found = await withRetries(() => session.client.workspaceSymbol(q, signal), signal);
        return Array.isArray(found) ? found.filter(s => s?.location?.uri && s.location.range) : [];
    }, []);

    return {
        available,
        connected,
        server: info.server,
        root: info.root,
        progress: progressSummary(progress),
        query,
        lspData,
        clearData,
        definitionAt,
        symbols,
        searchWorkspace,
    };
}
