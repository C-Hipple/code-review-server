import { spawn, Subprocess } from "bun";
import { resolve, join, dirname } from "path";
import { assets } from "./embedded_assets";

const SERVER_PATH = Bun.which("crs") || "crs";
const DIFF_LSP_PATH = Bun.which("diff-lsp") || "diff-lsp";

// Resolve the project root. 
// When compiled, import.meta.dir is a virtual path (/$bunfs/root).
// We must use a real path for the OS to spawn processes correctly.
function getProjectRoot() {
    const metaDir = import.meta.dir;
    if (metaDir.startsWith("/$bunfs")) {
        // If we are in bun_client directory, the project root is one level up
        const cwd = process.cwd();
        return cwd.endsWith("bun_client") ? resolve(cwd, "..") : cwd;
    }
    return resolve(metaDir, "..");
}

const PROJECT_ROOT = getProjectRoot();

interface JsonRpcRequest {
    method: string;
    params: any[];
    id: number | string;
}

interface JsonRpcResponse {
    result?: any;
    error?: any;
    id: number | string;
}

class RpcBridge {
    private proc: any;
    private pending = new Map<string | number, { resolve: (val: any) => void; reject: (err: any) => void }>();
    private buffer = "";

    constructor() {
        console.log(`[RpcBridge] PROJECT_ROOT: ${PROJECT_ROOT}`);
        console.log(`[RpcBridge] Resolved SERVER_PATH: ${SERVER_PATH}`);
        
        this.proc = spawn([SERVER_PATH, "--server"], {
            cwd: PROJECT_ROOT,
            stdin: "pipe",
            stdout: "pipe",
            stderr: "inherit",
            env: process.env
        });

        this.readLoop();
    }

    private async readLoop() {
        const reader = this.proc.stdout.getReader();
        try {
            while (true) {
                const { done, value } = await reader.read();
                if (done) break;
                this.handleChunk(new TextDecoder().decode(value));
            }
        } catch (e) {
            console.error("Error reading from server:", e);
        }
    }

    private handleChunk(chunk: string) {
        this.buffer += chunk;

        let depth = 0;
        let start = 0;
        let inString = false;
        let escape = false;

        for (let i = 0; i < this.buffer.length; i++) {
            const char = this.buffer[i];

            if (escape) {
                escape = false;
                continue;
            }

            if (char === '\\') {
                escape = true;
                continue;
            }

            if (char === '"') {
                inString = !inString;
                continue;
            }

            if (!inString) {
                if (char === '{') {
                    if (depth === 0) start = i;
                    depth++;
                } else if (char === '}') {
                    depth--;
                    if (depth === 0) {
                        // Found a complete object
                        const jsonStr = this.buffer.substring(start, i + 1);
                        try {
                            const obj = JSON.parse(jsonStr);
                            this.handleResponse(obj);
                        } catch (e) {
                            console.error("Failed to parse JSON:", jsonStr, e);
                        }
                        // Remove processed part
                        this.buffer = this.buffer.substring(i + 1);
                        i = -1; // Restart scanning from 0
                    }
                }
            }
        }
    }

    private handleResponse(res: JsonRpcResponse) {
        if (res.id !== undefined && this.pending.has(res.id)) {
            const { resolve, reject } = this.pending.get(res.id)!;
            this.pending.delete(res.id);
            if (res.error) {
                reject(res.error);
            } else {
                resolve(res.result);
            }
        }
    }

    public call(method: string, params: any[]): Promise<any> {
        const id = Date.now() + Math.random();
        const req: JsonRpcRequest = { method, params, id };

        return new Promise((resolve, reject) => {
            this.pending.set(id, { resolve, reject });
            const data = JSON.stringify(req);
            // Go's jsonrpc (JSON-RPC 1.0) expects newline-delimited JSON.
            this.proc.stdin.write(data + "\n");
        });
    }
}

const bridge = new RpcBridge();

const CORS_HEADERS = {
    "Access-Control-Allow-Origin": "*",
    "Access-Control-Allow-Methods": "POST, GET, OPTIONS, DELETE, PATCH",
    "Access-Control-Allow-Headers": "Content-Type",
};

Bun.serve<{ cmd: string, envs: Record<string, string>, proc?: Subprocess }>({
    port: parseInt(process.env.PORT || "5172"),
    async fetch(req, server) {
        const url = new URL(req.url);

        if (url.pathname === "/api/lsp") {
          console.log("lsp call happening")

            const success = server.upgrade(req, {
                data: {
                    cmd: DIFF_LSP_PATH,
                    envs: process.env
                }
            });
            if (success) return undefined;
            return new Response("Upgrade failed", { status: 500 });
        }

        // CORS
        if (req.method === "OPTIONS") {
            return new Response(null, {
                headers: CORS_HEADERS,
            });
        }

        // Helper to handle RPC calls
        const handleRpc = async (method: string, params: any[]) => {
            try {
                const result = await bridge.call(method, params);
                return new Response(JSON.stringify({ result }), {
                    headers: {
                        ...CORS_HEADERS,
                        "Content-Type": "application/json",
                    },
                });
            } catch (err) {
                console.error(`RPC Error (${method}):`, err);
                return new Response(JSON.stringify({ error: err }), {
                    status: 500,
                    headers: {
                        ...CORS_HEADERS,
                        "Content-Type": "application/json",
                    },
                });
            }
        };

        // Specialized Handlers for the "New JSON Format"
        if (url.pathname === "/api/get-pr" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.GetPR", [body]);
        }

        if (url.pathname === "/api/add-comment" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.AddComment", [body]);
        }

        if (url.pathname === "/api/edit-comment" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.EditComment", [body]);
        }

        if (url.pathname === "/api/delete-comment" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.DeleteComment", [body]);
        }

        if (url.pathname === "/api/sync-pr" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.SyncPR", [body]);
        }

        if (url.pathname === "/api/submit-review" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.SubmitReview", [body]);
        }

        if (url.pathname === "/api/reviews" && req.method === "POST") {
            return handleRpc("RPCHandler.GetAllReviews", [{}]);
        }

        if (url.pathname === "/api/set-feedback" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.SetFeedback", [body]);
        }

        if (url.pathname === "/api/remove-pr-comments" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.RemovePRComments", [body]);
        }

        if (url.pathname === "/api/list-plugins" && (req.method === "POST" || req.method === "GET")) {
            return handleRpc("RPCHandler.ListPlugins", [{}]);
        }

        if (url.pathname === "/api/get-plugin-output" && (req.method === "POST" || req.method === "GET")) {
            let body;
            if (req.method === "GET") {
                body = {
                    Owner: url.searchParams.get("owner"),
                    Repo: url.searchParams.get("repo"),
                    Number: parseInt(url.searchParams.get("number") || "0", 10),
                };
            } else {
                body = await req.json();
            }
            return handleRpc("RPCHandler.GetPluginOutput", [body]);
        }

        if (url.pathname === "/api/check-repo-exists" && req.method === "POST") {
            const body = await req.json();
            return handleRpc("RPCHandler.CheckRepoExists", [body]);
        }

        if (url.pathname === "/api/prepare-diff-lsp" && req.method === "POST") {
            const body = await req.json();
            const { project, root, buffer, type, content } = body;
            const header = `Project: ${project}
Root: ${root}
Buffer: ${buffer}
Type: ${type}
`;
            const fullContent = header + content;
            const filename = `diff_lsp_${crypto.randomUUID()}-bun`;
            const filePath = join("/tmp", filename);
            await Bun.write(filePath, fullContent);
            return new Response(JSON.stringify({ path: filePath }), {
                headers: { ...CORS_HEADERS, "Content-Type": "application/json" }
            });
        }

        // Generic legacy RPC endpoint
        if (url.pathname === "/api/rpc" && req.method === "POST") {
            const body = await req.json();
            return handleRpc(body.method, body.params);
        }

        // Serve static files
        let path = url.pathname;
        if (path === "/") path = "/index.html";

        // 1. Try embedded assets (for single-binary distribution)
        if (assets[path]) {
            return new Response(Bun.file(assets[path]));
        }

        // 2. Fallback to disk (for development)
        const publicPath = resolve(import.meta.dir, "frontend/dist");
        const filePath = resolve(publicPath, path.substring(1));

        if (filePath.startsWith(publicPath)) {
            const file = Bun.file(filePath);
            if (await file.exists()) {
                return new Response(file);
            }
        }

        // 3. SPA fallback: serve index.html
        if (assets["/index.html"]) {
            return new Response(Bun.file(assets["/index.html"]));
        }

        const indexPath = resolve(import.meta.dir, "frontend/dist/index.html");
        const indexFile = Bun.file(indexPath);
        if (await indexFile.exists()) {
            return new Response(indexFile);
        }

        return new Response("Not Found", { status: 404 });
    },
    websocket: {
        open(ws) {
            console.log("LSP WebSocket connected");
            const { cmd, envs } = ws.data;
            const proc = spawn([cmd], {
                stdin: "pipe",
                stdout: "pipe",
                stderr: "inherit",
                env: { ...process.env, ...envs }
            });
            ws.data.proc = proc;

            const reader = proc.stdout.getReader();
            (async () => {
                let buffer = Buffer.alloc(0);
                try {
                    while (true) {
                        const { done, value } = await reader.read();
                        if (done) break;
                        buffer = Buffer.concat([buffer, value]);

                        while (true) {
                            const separatorIndex = buffer.indexOf('\r\n\r\n');
                            if (separatorIndex === -1) break;

                            const headerPart = buffer.subarray(0, separatorIndex).toString('utf-8');
                            const lengthMatch = headerPart.match(/Content-Length: (\d+)/i);
                            
                            if (!lengthMatch) {
                                console.error("Invalid LSP header:", headerPart);
                                // Skip past this separator
                                buffer = buffer.subarray(separatorIndex + 4);
                                continue;
                            }

                            const contentLength = parseInt(lengthMatch[1], 10);
                            const totalMessageLength = separatorIndex + 4 + contentLength;

                            if (buffer.length >= totalMessageLength) {
                                const jsonBuf = buffer.subarray(separatorIndex + 4, totalMessageLength);
                                const jsonStr = jsonBuf.toString('utf-8');
                                ws.send(jsonStr);
                                buffer = buffer.subarray(totalMessageLength);
                            } else {
                                break;
                            }
                        }
                    }
                } catch (e) {
                    console.error("Error reading from LSP:", e);
                }
            })();
        },
        message(ws, message) {
            console.log("LSP Server received message:", message);
            const proc = ws.data.proc;
            if (proc && proc.stdin && typeof proc.stdin !== "number") {
                const msgStr = typeof message === "string" ? message : new TextDecoder().decode(message);
                const length = new TextEncoder().encode(msgStr).length;
                const wrapped = `Content-Length: ${length}\r\n\r\n${msgStr}`;
                proc.stdin.write(wrapped);
                proc.stdin.flush();
            }
        },
        close(ws) {
            console.log("LSP WebSocket closed");
            const proc = ws.data.proc;
            if (proc) {
                proc.kill();
            }
        },
    }
});

console.log(`Bun server running on http://localhost:${parseInt(process.env.PORT || "5172")}`);
