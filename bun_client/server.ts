import { spawn, Subprocess } from "bun";

import { resolve, join } from "path";
import { assets } from "./embedded_assets";
const SERVER_PATH = resolve(process.env.HOME || "/home/chris", "go/bin/crs");
const DIFF_LSP_PATH = resolve(process.env.HOME || "/home/chris", ".cargo/bin/diff-lsp");

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

const getShellPath = () => {
    try {
        const shell = process.env.SHELL || "bash";
        const { stdout } = Bun.spawnSync([shell, "-lc", "echo $PATH"]);
        return stdout.toString().trim();
    } catch (e) {
        console.error("Failed to fetch shell PATH:", e);
        return process.env.PATH || "";
    }
};

const SHELL_PATH = getShellPath();

class RpcBridge {
    private proc: any;
    private pending = new Map<string | number, { resolve: (val: any) => void; reject: (err: any) => void }>();
    private buffer = "";

    constructor() {
        this.proc = spawn([SERVER_PATH, "--server"], {
            cwd: "..", // Run from project root
            stdin: "pipe",
            stdout: "pipe",
            stderr: "inherit",
            env: {
                ...process.env,
                PATH: SHELL_PATH,
                HOME: process.env.HOME || "/home/chris"
            }
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
    port: parseInt(process.env.PORT || "3000"),
    async fetch(req, server) {
        const url = new URL(req.url);

        if (url.pathname === "/api/lsp") {
            const success = server.upgrade(req, {
                data: {
                    cmd: DIFF_LSP_PATH,
                    envs: {
                        PATH: SHELL_PATH,
                        HOME: process.env.HOME || "/home/chris"
                    }
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

        if (url.pathname === "/api/prepare-diff-lsp" && req.method === "POST") {
            const body = await req.json();
            const { project, root, buffer, type, content } = body;
            const header = `Project: ${project}
Root: ${root}
Buffer: ${buffer}
Type: ${type}
`;
            const fullContent = header + content;
            const filename = `diff_lsp_${crypto.randomUUID()}`;
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

        const indexPath = resolve(publicPath, "index.html");
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
                try {
                    while (true) {
                        const { done, value } = await reader.read();
                        if (done) break;
                        ws.send(value);
                    }
                } catch (e) {
                    console.error("Error reading from LSP:", e);
                }
            })();
        },
        message(ws, message) {
            const proc = ws.data.proc;
            if (proc && proc.stdin && typeof proc.stdin !== "number") {
                proc.stdin.write(message);
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

console.log(`Bun server running on http://localhost:${parseInt(process.env.PORT || "3000")}`);
