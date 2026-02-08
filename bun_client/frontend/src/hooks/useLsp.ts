import { useState, useEffect, useRef, useCallback } from 'react';
import { LspClient, LspHover, LspLocation } from '../lsp';
import { API_BASE } from '../api';

export interface LspData {
    hover: LspHover | null;
    refs: LspLocation[] | null;
}

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
    query: (line: number, col: number) => Promise<LspData | null>;
    lspData: LspData | null;
    clearData: () => void;
}

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

export function useLsp(options: UseLspOptions): UseLspResult {
    const [available, setAvailable] = useState<boolean | null>(null);
    const [connected, setConnected] = useState(false);
    const [lspData, setLspData] = useState<LspData | null>(null);

    const clientRef = useRef<LspClient | null>(null);
    const uriRef = useRef<string | null>(null);
    const initializingRef = useRef(false);

    const enabled = options.enabled !== false;

    const clearData = useCallback(() => {
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

            try {
                // Check diff-lsp availability
                const res = await fetch(`${API_BASE}/api/check-lsp`);
                const data = await res.json();
                if (cancelled) return;

                setAvailable(data.available);
                if (!data.available || !options.repoPath) return;

                const client = new LspClient();
                client.connect('/api/lsp');

                // Prepare context for diff-lsp
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

                const uri = `file://${path}`;
                uriRef.current = uri;

                // Initialize
                try {
                    await Promise.race([
                        client.initialize('/'),
                        new Promise((_, reject) =>
                            setTimeout(() => reject(new Error('LSP Initialize timeout')), 5000)
                        ),
                    ]);
                } catch (e) {
                    console.error('LSP Initialize error/timeout', e);
                }
                if (cancelled) {
                    client.disconnect();
                    return;
                }

                client.initialized();
                await client.didOpen(uri, 'diff', 1, options.diffContent);

                if (cancelled) {
                    client.disconnect();
                    return;
                }

                clientRef.current = client;
                setConnected(true);
            } catch (e) {
                console.error('LSP init failed', e);
            } finally {
                initializingRef.current = false;
            }
        };

        init();

        return () => {
            cancelled = true;
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

            try {
                // Check language server availability
                const res = await fetch(`${API_BASE}/api/check-lsp-file?lang=${lspLang}`);
                const data = await res.json();
                if (cancelled) return;

                setAvailable(data.available);
                if (!data.available) return;

                const client = new LspClient();
                client.connect(`/api/lsp-file?lang=${lspLang}`);

                // Initialize with repo path as root
                try {
                    await Promise.race([
                        client.initialize(options.repoPath),
                        new Promise((_, reject) =>
                            setTimeout(() => reject(new Error('LSP Initialize timeout')), 5000)
                        ),
                    ]);
                } catch (e) {
                    console.error('LSP Initialize error/timeout', e);
                }
                if (cancelled) {
                    client.disconnect();
                    return;
                }

                client.initialized();

                // Construct the file URI
                const filePath = options.filePath.startsWith('/')
                    ? options.filePath
                    : `${options.repoPath}/${options.filePath}`;
                const uri = `file://${filePath}`;
                uriRef.current = uri;

                await client.didOpen(uri, lspLang, 1, options.fileContent);

                if (cancelled) {
                    client.disconnect();
                    return;
                }

                clientRef.current = client;
                setConnected(true);
            } catch (e) {
                console.error('LSP init failed', e);
            } finally {
                initializingRef.current = false;
            }
        };

        init();

        return () => {
            cancelled = true;
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
        async (line: number, col: number): Promise<LspData | null> => {
            const client = clientRef.current;
            const uri = uriRef.current;
            if (!client || !uri) return null;

            let queryLine = line;
            let queryCol = col;

            if (options.mode === 'diff') {
                // Header is 5 lines followed by a newline, so diff starts at line 6 (0-indexed)
                queryLine = line + 6;
                // Add 1 to col to account for the diff prefix (+/- / space)
                queryCol = col + 1;
            }

            try {
                const [hover, refs] = await Promise.all([
                    client.hover(uri, queryLine, queryCol),
                    client.references(uri, queryLine, queryCol),
                ]);

                let hasHover = false;
                if (hover && hover.contents) {
                    if (typeof hover.contents === 'string') {
                        hasHover = hover.contents.length > 0;
                    } else if (Array.isArray(hover.contents)) {
                        hasHover = hover.contents.length > 0;
                    } else if (typeof hover.contents === 'object') {
                        hasHover = !!(hover.contents as any).value;
                    }
                }
                const hasRefs = refs && refs.length > 0;

                if (hasHover || hasRefs) {
                    const data = { hover, refs };
                    setLspData(data);
                    return data;
                }

                return null;
            } catch (e) {
                console.error('Failed to fetch LSP data', e);
                return null;
            }
        },
        [options.mode]
    );

    return { available, connected, query, lspData, clearData };
}
