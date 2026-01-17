
const API_BASE = (typeof window !== "undefined" && window.location.origin === "http://localhost:5173")
    ? "http://localhost:3000"
    : "";

export interface LspLocation {
    uri: string;
    range: {
        start: { line: number; character: number };
        end: { line: number; character: number };
    };
}

export interface LspHover {
    contents: string | { language: string; value: string } | Array<string | { language: string; value: string }>;
    range?: any;
}

export class LspClient {
    private ws: WebSocket | null = null;
    private messageQueue: string[] = [];
    private pendingRequests: Map<number, { resolve: (val: any) => void; reject: (err: any) => void }> = new Map();
    private nextId = 1;
    private onNotification: ((method: string, params: any) => void) | null = null;
    private ready = false;

    constructor(private onReady?: () => void) { }

    async prepareContext(project: string, root: string, buffer: string, type: string, content: string): Promise<string> {
        const res = await fetch(`${API_BASE}/api/prepare-diff-lsp`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ project, root, buffer, type, content })
        });
        if (!res.ok) throw new Error("Failed to prepare LSP context");
        const data = await res.json();
        return data.path;
    }

    connect() {
        // Assume ws relative to current origin or fixed port if dev
        const wsUrl = (API_BASE || window.location.origin).replace(/^http/, 'ws') + '/api/lsp';
        this.ws = new WebSocket(wsUrl);

        this.ws.onopen = () => {
            console.log("LSP WebSocket connected");
            this.flushQueue();
            if (this.onReady) this.onReady();
        };

        this.ws.onmessage = (event) => {
            console.log("LSP Message received:", event.data);
            const msg = JSON.parse(event.data);
            if (msg.id !== undefined && this.pendingRequests.has(msg.id)) {
                const { resolve, reject } = this.pendingRequests.get(msg.id)!;
                this.pendingRequests.delete(msg.id);
                if (msg.error) reject(msg.error);
                else resolve(msg.result);
            } else if (msg.method) {
                if (this.onNotification) this.onNotification(msg.method, msg.params);
            }
        };

        this.ws.onerror = (e) => console.error("LSP WebSocket error", e);
        this.ws.onclose = () => {
            console.log("LSP WebSocket closed");
            this.ready = false;
        };
    }

    private flushQueue() {
        if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
        while (this.messageQueue.length > 0) {
            const msg = this.messageQueue.shift()!;
            console.log("LSP Sending flush:", msg);
            this.ws.send(msg);
        }
    }

    request(method: string, params: any): Promise<any> {
        return new Promise((resolve, reject) => {
            const id = this.nextId++;
            const req = { jsonrpc: "2.0", id, method, params };
            const msg = JSON.stringify(req);
            this.pendingRequests.set(id, { resolve, reject });

            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                console.log("LSP Sending request:", msg);
                this.ws.send(msg);
            } else {
                console.log("LSP Queuing request:", msg);
                this.messageQueue.push(msg);
            }
        });
    }

    notify(method: string, params: any) {
        const req = { jsonrpc: "2.0", method, params };
        const msg = JSON.stringify(req);
        if (this.ws && this.ws.readyState === WebSocket.OPEN) {
            console.log("LSP Sending notify:", msg);
            this.ws.send(msg);
        } else {
            console.log("LSP Queuing notify:", msg);
            this.messageQueue.push(msg);
        }
    }

    async initialize(rootPath: string) {
        return this.request("initialize", {
            processId: null,
            rootUri: `file://${rootPath}`,
            capabilities: {
                textDocument: {
                    hover: { contentFormat: ["markdown", "plaintext"] },
                    references: {},
                    definition: {}
                }
            }
        });
    }

    initialized() {
        this.notify("initialized", {});
    }

    async didOpen(uri: string, languageId: string, version: number, text: string) {
        console.log("LSP didOpen: Waiting 1s...", uri);
        // Wait 1 second before sending open file to give the lsp time to init
        await new Promise(resolve => setTimeout(resolve, 1000));
        console.log("LSP didOpen: Sending now...", uri);
        this.notify("textDocument/didOpen", {
            textDocument: { uri, languageId, version, text }
        });
    }

    async hover(uri: string, line: number, character: number): Promise<LspHover | null> {
        return this.request("textDocument/hover", {
            textDocument: { uri },
            position: { line, character }
        });
    }

    async references(uri: string, line: number, character: number): Promise<LspLocation[] | null> {
        return this.request("textDocument/references", {
            textDocument: { uri },
            position: { line, character },
            context: { includeDeclaration: true }
        });
    }
}
