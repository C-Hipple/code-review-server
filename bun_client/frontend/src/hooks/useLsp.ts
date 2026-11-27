import { useState, useEffect, useRef, useCallback } from 'react';
import { LspClient } from '../lsp';
import { API_BASE } from '../api';
import { useLspQuery, type LspQueryTarget, type LspSection } from './useLspQuery';

export type { LspData, LspQueryResult, LspSection } from './useLspQuery';

/**
 * The review diff's LSP: diff-lsp, which reads the diff as a document and
 * answers from backends over the checkout. The code viewer talks to the
 * file's own language server instead (useFileLsp).
 */
interface UseLspOptions {
    repoPath: string | null;
    worktreePath: string;
    repoName: string;
    prNumber: number;
    diffContent: string;
    enabled?: boolean;
}

export interface UseLspResult {
    available: boolean | null;
    connected: boolean;
    /** See useLspQuery: hover, definition, type definition and references at a diff position. */
    query: ReturnType<typeof useLspQuery>['query'];
    lspData: ReturnType<typeof useLspQuery>['lspData'];
    clearData: () => void;
}

// A running server answers `initialize` at once (the bun server shares them);
// a new diff-lsp first starts and initializes a backend per language.
export const INITIALIZE_TIMEOUT_MS = 60_000;

// Notifications sent before the server has answered `initialize` are dropped
// (diff-lsp's tower-lsp does this), so the handshake waits for the answer.
export async function initializeClient(client: LspClient, rootPath: string, workspace = false) {
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
        await Promise.race([
            client.initialize(rootPath, workspace),
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

// Sent in this order on purpose: diff-lsp asks its backend one request at a
// time, and references is the slow one, so hover and definitions go first
// rather than waiting behind it.
const DIFF_SECTIONS: LspSection[] = ['hover', 'definitions', 'typeDefinitions', 'refs'];

export function useLsp(options: UseLspOptions): UseLspResult {
    const [available, setAvailable] = useState<boolean | null>(null);
    const [connected, setConnected] = useState(false);

    const clientRef = useRef<LspClient | null>(null);
    const uriRef = useRef<string | null>(null);
    const rootsRef = useRef<string[]>([]);
    const initializingRef = useRef(false);

    const enabled = options.enabled !== false;

    const getTarget = useCallback((): LspQueryTarget | null => {
        const client = clientRef.current;
        const uri = uriRef.current;
        return client && uri ? { client, uri, roots: rootsRef.current } : null;
    }, []);
    const { query: rawQuery, lspData, clearData } = useLspQuery(getTarget, DIFF_SECTIONS);

    useEffect(() => {
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

                await initializeClient(client, '/');
                if (cancelled) {
                    client.disconnect();
                    return;
                }
                // Sent straight after `initialized`, as any editor does. The
                // server may already be running (the bun server shares them —
                // see lsp_pool.ts), in which case the handshake is answered
                // without waiting on it at all.
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
            clearData();
            if (clientRef.current) {
                clientRef.current.disconnect();
                clientRef.current = null;
            }
            uriRef.current = null;
            setConnected(false);
            initializingRef.current = false;
        };
    }, [
        options.diffContent,
        options.repoPath,
        // A worktree that shows up after the review opened (a sync picks up
        // the one the background workflow just created) has to be used:
        // until then diff-lsp reads the main checkout.
        options.worktreePath,
        options.repoName,
        options.prNumber,
        enabled,
    ]);

    // Header is 5 lines followed by a newline, so the diff starts at line 6
    // (0-indexed), and each line starts with its +/-/space prefix.
    const query = useCallback(
        (line: number, col: number) => rawQuery(line + 6, col + 1),
        [rawQuery]
    );

    return { available, connected, query, lspData, clearData };
}
