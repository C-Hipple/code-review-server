const API_BASE =
    typeof window !== 'undefined' && window.location.origin === 'http://localhost:5173'
        ? 'http://localhost:5172'
        : '';

export interface LspLocation {
    uri: string;
    range: {
        start: { line: number; character: number };
        end: { line: number; character: number };
    };
}

/** A definition-style answer from a server that links source and target ranges. */
export interface LspLocationLink {
    targetUri: string;
    targetRange: LspLocation['range'];
    targetSelectionRange?: LspLocation['range'];
    originSelectionRange?: LspLocation['range'];
}

/** textDocument/documentSymbol's hierarchical answer. */
export interface LspDocumentSymbol {
    name: string;
    detail?: string;
    kind: number;
    range: LspLocation['range'];
    selectionRange: LspLocation['range'];
    children?: LspDocumentSymbol[];
}

/** textDocument/documentSymbol's flat answer, and workspace/symbol's. */
export interface LspSymbolInformation {
    name: string;
    kind: number;
    containerName?: string;
    location: LspLocation;
}

export interface LspHover {
    contents:
        | string
        | { language: string; value: string }
        | Array<string | { language: string; value: string }>;
    range?: any;
}

/** Source text by file URI, then by 0-based line. */
export type LocationLines = Record<string, Record<number, string>>;

/** The text on each line the locations point at, read from disk by the server. */
export async function fetchLocationLines(locations: LspLocation[]): Promise<LocationLines> {
    const res = await fetch(`${API_BASE}/api/lsp-lines`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
            locations: locations.map(l => ({ uri: l.uri, line: l.range.start.line })),
        }),
    });
    if (!res.ok) throw new Error(`Failed to read location lines: ${res.status}`);
    const data = await res.json();
    return data.lines || {};
}

export class LspClient {
    private ws: WebSocket | null = null;
    private messageQueue: string[] = [];
    private pendingRequests: Map<
        number,
        { resolve: (val: any) => void; reject: (err: any) => void }
    > = new Map();
    private nextId = 1;
    private onNotification: ((method: string, params: any) => void) | null = null;

    constructor(private onReady?: () => void) {}

    /** Receives the server's notifications, e.g. `$/progress`. */
    setNotificationHandler(handler: ((method: string, params: any) => void) | null) {
        this.onNotification = handler;
    }

    async prepareContext(
        project: string,
        root: string,
        buffer: string,
        type: string,
        content: string,
        worktree: string
    ): Promise<string> {
        const res = await fetch(`${API_BASE}/api/prepare-diff-lsp`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ project, root, buffer, type, content, worktree }),
        });
        if (!res.ok) throw new Error('Failed to prepare LSP context');
        const data = await res.json();
        return data.path;
    }

    connect(wsPath: string = '/api/lsp') {
        // Assume ws relative to current origin or fixed port if dev
        const wsUrl = (API_BASE || window.location.origin).replace(/^http/, 'ws') + wsPath;
        this.ws = new WebSocket(wsUrl);

        this.ws.onopen = () => {
            console.log('LSP WebSocket connected');
            this.flushQueue();
            if (this.onReady) this.onReady();
        };

        this.ws.onmessage = event => {
            const msg = JSON.parse(event.data);
            // A response has no method. A request the server makes numbers
            // its own ids, which can match one of ours; the bridge answers
            // those (lsp_pool.ts), so they never reach here.
            if (
                msg.method === undefined &&
                msg.id !== undefined &&
                this.pendingRequests.has(msg.id)
            ) {
                const { resolve, reject } = this.pendingRequests.get(msg.id)!;
                this.pendingRequests.delete(msg.id);
                if (msg.error) reject(msg.error);
                else resolve(msg.result);
            } else if (msg.method) {
                if (this.onNotification) this.onNotification(msg.method, msg.params);
            }
        };

        this.ws.onerror = e => console.error('LSP WebSocket error', e);
        this.ws.onclose = event => {
            console.log('LSP WebSocket closed', event.reason);
            // Nothing in flight will be answered now.
            for (const { reject } of this.pendingRequests.values()) {
                reject(new Error(`LSP connection closed ${event.reason}`));
            }
            this.pendingRequests.clear();
        };
    }

    disconnect() {
        if (this.ws) {
            this.ws.onclose = null;
            this.ws.onerror = null;
            this.ws.onmessage = null;
            this.ws.close();
            this.ws = null;
        }
        for (const { reject } of this.pendingRequests.values()) {
            reject(new Error('LSP disconnected'));
        }
        this.pendingRequests.clear();
        this.messageQueue = [];
    }

    private flushQueue() {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
        while (this.messageQueue.length > 0) {
            this.ws.send(this.messageQueue.shift()!);
        }
    }

    // Aborting `signal` cancels the request on the server too, so it doesn't
    // hold up the requests behind it: diff-lsp answers one at a time.
    request(method: string, params: any, signal?: AbortSignal): Promise<any> {
        return new Promise((resolve, reject) => {
            if (signal?.aborted) {
                reject(signal.reason);
                return;
            }
            // Queued messages go out when the socket opens; one that has
            // closed (its server exited) would never answer.
            if (this.ws && this.ws.readyState > WebSocket.OPEN) {
                reject(new Error('LSP connection closed'));
                return;
            }
            const id = this.nextId++;
            const req = { jsonrpc: '2.0', id, method, params };
            this.pendingRequests.set(id, { resolve, reject });
            signal?.addEventListener(
                'abort',
                () => {
                    if (!this.pendingRequests.delete(id)) return;
                    this.notify('$/cancelRequest', { id });
                    reject(signal.reason);
                },
                { once: true }
            );
            this.send(JSON.stringify(req));
        });
    }

    notify(method: string, params: any) {
        this.send(JSON.stringify({ jsonrpc: '2.0', method, params }));
    }

    private send(msg: string) {
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(msg);
        } else {
            this.messageQueue.push(msg);
        }
    }

    /**
     * `workspace` is for a plain language server rooted at a project (the
     * code viewer's): it's told the workspace folder, which pyright needs to
     * resolve imports, and that the bridge answers its configuration and
     * progress requests. diff-lsp gets the bare handshake it always has.
     */
    async initialize(rootPath: string, workspace = false) {
        const rootUri = `file://${rootPath}`;
        const textDocument = {
            hover: { contentFormat: ['markdown', 'plaintext'] },
            references: {},
            definition: {},
            typeDefinition: {},
        };
        if (!workspace) {
            return this.request('initialize', {
                processId: null,
                rootUri,
                capabilities: { textDocument },
            });
        }
        return this.request('initialize', {
            processId: null,
            rootUri,
            rootPath,
            workspaceFolders: [{ uri: rootUri, name: rootPath.split('/').pop() || rootPath }],
            capabilities: {
                general: { positionEncodings: ['utf-16'] },
                window: { workDoneProgress: true },
                workspace: { configuration: true, workspaceFolders: true, symbol: {} },
                textDocument: {
                    ...textDocument,
                    implementation: {},
                    documentSymbol: { hierarchicalDocumentSymbolSupport: true },
                },
            },
        });
    }

    initialized() {
        this.notify('initialized', {});
    }

    // Sent straight after `initialized`, as any editor does. The server may
    // already be running (the bun server shares them — see lsp_pool.ts), in
    // which case the handshake is answered without waiting on it at all.
    didOpen(uri: string, languageId: string, version: number, text: string) {
        this.notify('textDocument/didOpen', {
            textDocument: { uri, languageId, version, text },
        });
    }

    didClose(uri: string) {
        this.notify('textDocument/didClose', { textDocument: { uri } });
    }

    async hover(
        uri: string,
        line: number,
        character: number,
        signal?: AbortSignal
    ): Promise<LspHover | null> {
        return this.request(
            'textDocument/hover',
            { textDocument: { uri }, position: { line, character } },
            signal
        );
    }

    async references(
        uri: string,
        line: number,
        character: number,
        signal?: AbortSignal
    ): Promise<LspLocation[] | null> {
        return this.request(
            'textDocument/references',
            {
                textDocument: { uri },
                position: { line, character },
                context: { includeDeclaration: true },
            },
            signal
        );
    }

    async definition(
        uri: string,
        line: number,
        character: number,
        signal?: AbortSignal
    ): Promise<LspLocation | LspLocation[] | LspLocationLink[] | null> {
        return this.request(
            'textDocument/definition',
            { textDocument: { uri }, position: { line, character } },
            signal
        );
    }

    async typeDefinition(
        uri: string,
        line: number,
        character: number,
        signal?: AbortSignal
    ): Promise<LspLocation | LspLocation[] | LspLocationLink[] | null> {
        return this.request(
            'textDocument/typeDefinition',
            { textDocument: { uri }, position: { line, character } },
            signal
        );
    }

    async implementation(
        uri: string,
        line: number,
        character: number,
        signal?: AbortSignal
    ): Promise<LspLocation | LspLocation[] | LspLocationLink[] | null> {
        return this.request(
            'textDocument/implementation',
            { textDocument: { uri }, position: { line, character } },
            signal
        );
    }

    async documentSymbol(
        uri: string,
        signal?: AbortSignal
    ): Promise<LspDocumentSymbol[] | LspSymbolInformation[] | null> {
        return this.request('textDocument/documentSymbol', { textDocument: { uri } }, signal);
    }

    async workspaceSymbol(
        query: string,
        signal?: AbortSignal
    ): Promise<LspSymbolInformation[] | null> {
        return this.request('workspace/symbol', { query }, signal);
    }
}
