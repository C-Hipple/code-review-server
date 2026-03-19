import { readdir } from 'node:fs/promises';
import { join, relative, resolve } from 'node:path';
import { type Subprocess, spawn } from 'bun';

let assets: Record<string, any> = {};
try {
    // @ts-expect-error
    const assetsModule = await import('./embedded_assets');
    assets = assetsModule.assets;
} catch (e) {
    console.log('[Server] Embedded assets not found or broken, skipping...');
}

const SERVER_PATH = Bun.which('crs') || 'crs';
const DIFF_LSP_PATH = Bun.which('diff-lsp');

// Language server configurations for file-mode LSP
const LANGUAGE_SERVER_CONFIG: Record<string, { cmd: string; args: string[] }> = {
    typescript: { cmd: 'typescript-language-server', args: ['--stdio'] },
    javascript: { cmd: 'typescript-language-server', args: ['--stdio'] },
    tsx: { cmd: 'typescript-language-server', args: ['--stdio'] },
    jsx: { cmd: 'typescript-language-server', args: ['--stdio'] },
    go: { cmd: 'gopls', args: ['serve'] },
    rust: { cmd: 'rust-analyzer', args: [] },
    python: { cmd: 'pyright-langserver', args: ['--stdio'] },
};

// Resolve which language servers are available at startup
const AVAILABLE_LANGUAGE_SERVERS: Record<string, { cmd: string; args: string[] }> = {};
for (const [lang, config] of Object.entries(LANGUAGE_SERVER_CONFIG)) {
    const resolved = Bun.which(config.cmd);
    if (resolved) {
        AVAILABLE_LANGUAGE_SERVERS[lang] = { cmd: resolved, args: config.args };
    }
}
console.log(
    `[LSP] Available language servers: ${Object.keys(AVAILABLE_LANGUAGE_SERVERS).join(', ') || 'none'}`
);

// Resolve the project root.
// When compiled, import.meta.dir is a virtual path (/$bunfs/root).
// We must use a real path for the OS to spawn processes correctly.
function getProjectRoot() {
    const metaDir = import.meta.dir;
    if (metaDir.startsWith('/$bunfs')) {
        // If we are in bun_client directory, the project root is one level up
        const cwd = process.cwd();
        return cwd.endsWith('bun_client') ? resolve(cwd, '..') : cwd;
    }
    return resolve(metaDir, '..');
}

const PROJECT_ROOT = getProjectRoot();

interface JsonRpcRequest {
    method: string;
    params: unknown[];
    id: number | string;
}

interface JsonRpcResponse {
    result?: unknown;
    error?: unknown;
    id: number | string;
}

class RpcBridge {
    private proc: ReturnType<typeof spawn>;
    private pending = new Map<
        string | number,
        { resolve: (val: unknown) => void; reject: (err: unknown) => void }
    >();
    private buffer = '';

    constructor() {
        console.log(`[RpcBridge] PROJECT_ROOT: ${PROJECT_ROOT}`);
        console.log(`[RpcBridge] Resolved SERVER_PATH: ${SERVER_PATH}`);

        this.proc = spawn([SERVER_PATH, '--server'], {
            cwd: PROJECT_ROOT,
            stdin: 'pipe',
            stdout: 'pipe',
            stderr: 'inherit',
            env: process.env,
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
            console.error('Error reading from server:', e);
        }
    }

    private handleChunk(chunk: string) {
        this.buffer += chunk;

        let depth = 0;
        let start = 0;
        let inString = false;
        let isEscaped = false;

        for (let i = 0; i < this.buffer.length; i++) {
            const char = this.buffer[i];

            if (isEscaped) {
                isEscaped = false;
                continue;
            }

            if (char === '\\') {
                isEscaped = true;
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
                            console.error('Failed to parse JSON:', jsonStr, e);
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
        if (res.id !== undefined) {
            const pending = this.pending.get(res.id);
            if (pending) {
                const { resolve, reject } = pending;
                this.pending.delete(res.id);
                if (res.error) {
                    reject(res.error);
                } else {
                    resolve(res.result);
                }
            }
        }
    }

    public call(method: string, params: unknown[]): Promise<unknown> {
        const id = Date.now() + Math.random();
        const req: JsonRpcRequest = { method, params, id };

        return new Promise((resolve, reject) => {
            this.pending.set(id, { resolve, reject });
            const data = JSON.stringify(req);
            // Go's jsonrpc (JSON-RPC 1.0) expects newline-delimited JSON.
            this.proc.stdin.write(`${data}\n`);
        });
    }
}

const bridge = new RpcBridge();

const CORS_HEADERS = {
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Methods': 'POST, GET, OPTIONS, DELETE, PATCH',
    'Access-Control-Allow-Headers': 'Content-Type',
};

Bun.serve<{
    cmd: string | null;
    args?: string[];
    envs: Record<string, string>;
    proc?: Subprocess;
}>({
    port: parseInt(process.env.PORT || '5172', 10),
    async fetch(req, server) {
        const url = new URL(req.url);

        if (url.pathname === '/api/lsp') {
            console.log('lsp call happening');

            const success = server.upgrade(req, {
                data: {
                    cmd: DIFF_LSP_PATH,
                    envs: process.env as Record<string, string>,
                },
            });
            if (success) return undefined;
            return new Response('Upgrade failed', { status: 500 });
        }

        if (url.pathname === '/api/lsp-file') {
            const lang = url.searchParams.get('lang');
            if (!lang || !AVAILABLE_LANGUAGE_SERVERS[lang]) {
                return new Response('Language server not available', { status: 404 });
            }

            const serverConfig = AVAILABLE_LANGUAGE_SERVERS[lang];
            console.log(`[LSP-File] Upgrading WebSocket for language: ${lang}`);

            const success = server.upgrade(req, {
                data: {
                    cmd: serverConfig.cmd,
                    args: serverConfig.args,
                    envs: process.env as Record<string, string>,
                },
            });
            if (success) return undefined;
            return new Response('Upgrade failed', { status: 500 });
        }

        // CORS
        if (req.method === 'OPTIONS') {
            return new Response(null, {
                headers: CORS_HEADERS,
            });
        }

        // Helper to handle RPC calls
        const handleRpc = async (method: string, params: unknown[]) => {
            try {
                const result = await bridge.call(method, params);
                return new Response(JSON.stringify({ result }), {
                    headers: {
                        ...CORS_HEADERS,
                        'Content-Type': 'application/json',
                    },
                });
            } catch (err) {
                console.error(`RPC Error (${method}):`, err);
                return new Response(JSON.stringify({ error: err }), {
                    status: 500,
                    headers: {
                        ...CORS_HEADERS,
                        'Content-Type': 'application/json',
                    },
                });
            }
        };

        // Specialized Handlers for the "New JSON Format"
        if (url.pathname === '/api/get-pr' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.GetPR', [body]);
        }

        if (url.pathname === '/api/add-comment' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.AddComment', [body]);
        }

        if (url.pathname === '/api/edit-comment' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.EditComment', [body]);
        }

        if (url.pathname === '/api/delete-comment' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.DeleteComment', [body]);
        }

        if (url.pathname === '/api/sync-pr' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.SyncPR', [body]);
        }

        if (url.pathname === '/api/get-adjacent-pr' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.GetAdjacentPR', [body]);
        }

        if (url.pathname === '/api/submit-review' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.SubmitReview', [body]);
        }

        if (url.pathname === '/api/reviews' && req.method === 'POST') {
            return handleRpc('RPCHandler.GetAllReviews', [{}]);
        }

        if (url.pathname === '/api/set-feedback' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.SetFeedback', [body]);
        }

        if (url.pathname === '/api/remove-pr-comments' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.RemovePRComments', [body]);
        }

        if (
            url.pathname === '/api/list-plugins' &&
            (req.method === 'POST' || req.method === 'GET')
        ) {
            return handleRpc('RPCHandler.ListPlugins', [{}]);
        }

        if (
            url.pathname === '/api/get-plugin-output' &&
            (req.method === 'POST' || req.method === 'GET')
        ) {
            let body: unknown;
            if (req.method === 'GET') {
                body = {
                    Owner: url.searchParams.get('owner'),
                    Repo: url.searchParams.get('repo'),
                    Number: parseInt(url.searchParams.get('number') || '0', 10),
                };
            } else {
                body = await req.json();
            }
            return handleRpc('RPCHandler.GetPluginOutput', [body]);
        }

        if (url.pathname === '/api/rerun-plugins' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc('RPCHandler.RerunPlugins', [body]);
        }

        if (url.pathname === '/api/check-lsp' && req.method === 'GET') {
            return new Response(JSON.stringify({ available: !!DIFF_LSP_PATH }), {
                headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
            });
        }

        if (url.pathname === '/api/check-lsp-file' && req.method === 'GET') {
            const lang = url.searchParams.get('lang');
            return new Response(
                JSON.stringify({ available: !!lang && !!AVAILABLE_LANGUAGE_SERVERS[lang] }),
                {
                    headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                }
            );
        }

        // Read file contents from repository
        if (url.pathname === '/api/read-file' && req.method === 'POST') {
            const body = await req.json();
            const { repoPath, filePath } = body;

            if (!filePath) {
                return new Response(JSON.stringify({ error: 'filePath is required' }), {
                    status: 400,
                    headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                });
            }

            let resolved: string;

            // If filePath is absolute, use it directly
            if (filePath.startsWith('/')) {
                resolved = filePath;
            } else if (repoPath) {
                // Security: Ensure filePath is within repoPath
                resolved = resolve(repoPath, filePath);
                if (!resolved.startsWith(repoPath)) {
                    return new Response(JSON.stringify({ error: 'Invalid path' }), {
                        status: 400,
                        headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                    });
                }
            } else {
                return new Response(
                    JSON.stringify({
                        error: 'Either absolute filePath or repoPath is required',
                    }),
                    {
                        status: 400,
                        headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                    }
                );
            }

            const file = Bun.file(resolved);
            if (await file.exists()) {
                const content = await file.text();
                return new Response(JSON.stringify({ content }), {
                    headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                });
            }
            return new Response(JSON.stringify({ error: 'File not found' }), {
                status: 404,
                headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
            });
        }

        // List files in repository recursively
        if (url.pathname === '/api/list-files' && req.method === 'POST') {
            const body = await req.json();
            const { repoPath } = body;

            if (!repoPath) {
                return new Response(JSON.stringify({ error: 'repoPath is required' }), {
                    status: 400,
                    headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                });
            }

            try {
                const listFilesRecursive = async (
                    dir: string,
                    baseDir: string
                ): Promise<string[]> => {
                    const entries = await readdir(dir, { withFileTypes: true });
                    const files: string[] = [];

                    for (const entry of entries) {
                        const name = entry.name;
                        if (
                            name === 'node_modules' ||
                            name === '.git' ||
                            name === '.next' ||
                            name === 'dist'
                        )
                            continue;

                        const res = resolve(dir, name);
                        if (entry.isDirectory()) {
                            files.push(...(await listFilesRecursive(res, baseDir)));
                        } else {
                            files.push(relative(baseDir, res));
                        }
                    }
                    return files;
                };

                const files = await listFilesRecursive(repoPath, repoPath);
                return new Response(JSON.stringify({ files }), {
                    headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                });
            } catch (err) {
                console.error('Error listing files:', err);
                return new Response(JSON.stringify({ error: String(err) }), {
                    status: 500,
                    headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
                });
            }
        }

        if (url.pathname === '/api/prepare-diff-lsp' && req.method === 'POST') {
            const body = await req.json();
            const { project, root, buffer, type, content, worktree } = body;
            const header = `Project: ${project}
Root: ${root}
Worktree: ${worktree}
Buffer: ${buffer}
Type: ${type}
`;
            const fullContent = header + content;
            const filename = `diff_lsp_${crypto.randomUUID()}-bun`;
            const filePath = join('/tmp', filename);
            await Bun.write(filePath, fullContent);
            return new Response(JSON.stringify({ path: filePath }), {
                headers: { ...CORS_HEADERS, 'Content-Type': 'application/json' },
            });
        }

        // Generic legacy RPC endpoint
        if (url.pathname === '/api/rpc' && req.method === 'POST') {
            const body = await req.json();
            return handleRpc(body.method, body.params);
        }

        // Serve static files
        let path = url.pathname;
        if (path === '/') path = '/index.html';

        // 0. Development proxy to Vite
        if (process.env.NODE_ENV === 'development') {
            try {
                const viteUrl = new URL(path, 'http://localhost:5173');
                const response = await fetch(viteUrl.toString());
                if (response.ok) {
                    // Forward the response with corrected headers if needed
                    return new Response(response.body, {
                        status: response.status,
                        headers: response.headers,
                    });
                }
            } catch (e) {
                // Vite might not be up yet, continue to other fallback routes
            }
        }

        // 1. Try embedded assets (for single-binary distribution)
        if (assets[path]) {
            return new Response(Bun.file(assets[path]));
        }

        // 2. Fallback to disk (for development)
        const publicPath = resolve(import.meta.dir, 'frontend/dist');
        const filePath = resolve(publicPath, path.substring(1));

        if (filePath.startsWith(publicPath)) {
            const file = Bun.file(filePath);
            if (await file.exists()) {
                return new Response(file);
            }
        }

        // 3. SPA fallback: serve index.html
        if (assets['/index.html']) {
            return new Response(Bun.file(assets['/index.html']));
        }

        const indexPath = resolve(import.meta.dir, 'frontend/dist/index.html');
        const indexFile = Bun.file(indexPath);
        if (await indexFile.exists()) {
            return new Response(indexFile);
        }

        return new Response('Not Found', { status: 404 });
    },
    websocket: {
        open(ws) {
            console.log('LSP WebSocket connected');
            const { cmd, args, envs } = ws.data;
            if (!cmd) {
                console.warn('LSP binary not found, closing websocket');
                ws.close();
                return;
            }

            try {
                const proc = spawn(args && args.length > 0 ? [cmd, ...args] : [cmd], {
                    stdin: 'pipe',
                    stdout: 'pipe',
                    stderr: 'inherit',
                    env: { ...process.env, ...envs },
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

                                const headerPart = buffer
                                    .subarray(0, separatorIndex)
                                    .toString('utf-8');
                                const lengthMatch = headerPart.match(/Content-Length: (\d+)/i);

                                if (!lengthMatch) {
                                    console.error('Invalid LSP header:', headerPart);
                                    // Skip past this separator
                                    buffer = buffer.subarray(separatorIndex + 4);
                                    continue;
                                }

                                const contentLength = parseInt(lengthMatch[1], 10);
                                const totalMessageLength = separatorIndex + 4 + contentLength;

                                if (buffer.length >= totalMessageLength) {
                                    const jsonBuf = buffer.subarray(
                                        separatorIndex + 4,
                                        totalMessageLength
                                    );
                                    const jsonStr = jsonBuf.toString('utf-8');
                                    ws.send(jsonStr);
                                    buffer = buffer.subarray(totalMessageLength);
                                } else {
                                    break;
                                }
                            }
                        }
                    } catch (e) {
                        console.error('Error reading from LSP:', e);
                    }
                })();
            } catch (e) {
                console.error('Error spawning LSP process:', e);
                ws.close();
            }
        },
        message(ws, message) {
            const proc = ws.data.proc;
            if (proc?.stdin && typeof proc.stdin !== 'number') {
                const msgStr =
                    typeof message === 'string' ? message : new TextDecoder().decode(message);
                const length = new TextEncoder().encode(msgStr).length;
                const wrapped = `Content-Length: ${length}\r\n\r\n${msgStr}`;
                try {
                    proc.stdin.write(wrapped);
                    proc.stdin.flush();
                } catch (e) {
                    console.error('Error writing to LSP process:', e);
                }
            }
        },
        close(ws) {
            console.log('LSP WebSocket closed');
            const proc = ws.data.proc;
            if (proc) {
                try {
                    proc.kill();
                } catch (e) {
                    console.error('Error killing LSP process:', e);
                }
            }
        },
    },
});

console.log(`Bun server running on http://localhost:${parseInt(process.env.PORT || '5172', 10)}`);
