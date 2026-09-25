// Shared language-server sessions for the web client's LSP WebSockets.
//
// Each WebSocket used to spawn its own language server and kill it on close.
// For a review that meant a fresh diff-lsp, and behind it fresh gopls /
// rust-analyzer / typescript-language-server backends that index the whole
// workspace from scratch, every time a review opened, a hunk was expanded
// (the diff changes, so the client reconnects), or a code viewer moved to
// another file. Every first hover or reference lookup paid that cold start.
// The emacs client doesn't: lsp-mode keeps one diff-lsp per project running
// and reconnects each review buffer to it.
//
// This pool does the same. A WebSocket attaches to a running server for the
// same workspace when there is one, and a server whose last WebSocket closes
// stays up for a while in case the review is reopened. Sharing a server
// between clients means the pool has to speak a little of the protocol:
//
//   - `initialize` is accepted once per server, so the first answer is
//     replayed to every later client (and `initialized` forwarded once).
//   - Request ids are rewritten, since every client numbers its own from 1.
//   - A client's open documents are closed for it when it detaches.

import { spawn } from 'bun';

type JsonRpcId = number | string;

interface JsonRpcMessage {
    jsonrpc?: string;
    id?: JsonRpcId | null;
    method?: string;
    // biome-ignore lint/suspicious/noExplicitAny: arbitrary LSP params
    params?: any;
    result?: unknown;
    error?: unknown;
}

/** The pool's view of one WebSocket. */
export interface LspPeer {
    send(body: string): void;
    close(code?: number, reason?: string): void;
}

/** The pool's view of a running language server. */
export interface LspServer {
    /** Writes one JSON-RPC message body; the server adds the framing. */
    send(body: string): void;
    kill(): void;
}

/** Splits a language server's stdout into message bodies (Content-Length framing). */
export class LspFrameParser {
    private buffer = Buffer.alloc(0);

    constructor(private onMessage: (body: string) => void) {}

    push(chunk: Uint8Array) {
        this.buffer = Buffer.concat([this.buffer, chunk]);
        while (true) {
            const separator = this.buffer.indexOf('\r\n\r\n');
            if (separator === -1) return;
            const header = this.buffer.subarray(0, separator).toString('utf-8');
            const length = header.match(/Content-Length: *(\d+)/i);
            if (!length) {
                console.error('[LSP] Invalid header:', header);
                this.buffer = this.buffer.subarray(separator + 4);
                continue;
            }
            const end = separator + 4 + parseInt(length[1], 10);
            if (this.buffer.length < end) return;
            this.onMessage(this.buffer.subarray(separator + 4, end).toString('utf-8'));
            this.buffer = this.buffer.subarray(end);
        }
    }
}

export function frameLspMessage(body: string): string {
    return `Content-Length: ${Buffer.byteLength(body, 'utf-8')}\r\n\r\n${body}`;
}

// diff-lsp's SupportedFileType::from_extension: it starts one backend per
// language found in the diff's file headers.
const DIFF_LSP_LANGUAGES: Record<string, string> = {
    rs: 'rust',
    go: 'go',
    py: 'python',
    ts: 'typescript',
    tsx: 'typescript',
    js: 'javascript',
    jsx: 'javascript',
    mjs: 'javascript',
    cjs: 'javascript',
    c: 'c',
    h: 'c',
    cpp: 'cpp',
    cc: 'cpp',
    cxx: 'cpp',
    hpp: 'cpp',
    hxx: 'cpp',
    hh: 'cpp',
    java: 'java',
    rb: 'ruby',
    hs: 'haskell',
    lhs: 'haskell',
};

/**
 * Which diff-lsp session can serve a diff-lsp init tempfile. diff-lsp reads
 * the root, worktree and backend languages from the tempfile once, at
 * startup (read_initialization_params_from_tempfile), so a running diff-lsp
 * can take a later diff only when root and worktree match and it already runs
 * every backend the diff needs. The parsing mirrors diff-lsp's.
 */
export function diffLspWorkspace(tempfile: string): { key: string; langs: string[] } {
    let root = '';
    let worktree = '';
    const langs = new Set<string>();
    for (const line of tempfile.split('\n')) {
        const rootMatch = /^Root:\s+(.*)/.exec(line);
        if (rootMatch) root = rootMatch[1].trim();
        const worktreeMatch = /^Worktree:\s+(.*)/.exec(line);
        if (worktreeMatch) {
            const value = worktreeMatch[1].trim();
            if (value !== 'undefined' && value !== 'null') worktree = value;
        }

        let filename: string | undefined;
        const fileMatch = /^(?:modified|new file|deleted)\s+(.*)/.exec(line);
        const gitMatch = /^diff --git\s+(.*)/.exec(line);
        if (fileMatch) {
            filename = fileMatch[1];
        } else if (gitMatch) {
            filename = gitMatch[1]
                .split(/\s+/)
                .pop()
                ?.replace(/^[ab]\//, '');
        }
        const dot = filename?.lastIndexOf('.') ?? -1;
        const lang = filename && dot !== -1 ? DIFF_LSP_LANGUAGES[filename.slice(dot + 1)] : null;
        if (lang) langs.add(lang);
    }
    return { key: `diff-lsp\0${root}\0${worktree}`, langs: [...langs].sort() };
}

let nextPeerId = 1;

/** One language server and the WebSockets attached to it. */
export class LspSession {
    private peers = new Map<number, LspPeer>();
    // Requests forwarded to the server, by the id the server saw.
    private pending = new Map<number, { peer: number; id: JsonRpcId }>();
    private nextServerId = 1;
    private initReply: JsonRpcMessage | null = null;
    private initServerId: number | null = null;
    private initWaiters: { peer: number; id: JsonRpcId }[] = [];
    private initializedSent = false;
    private openDocs = new Map<number, Set<string>>();
    /** When the last peer detached; null while any peer is attached. */
    idleSince: number | null = null;
    closed = false;

    constructor(
        readonly key: string,
        readonly langs: string[],
        private server: LspServer
    ) {}

    get peerCount(): number {
        return this.peers.size;
    }

    attach(peer: LspPeer): number {
        const peerId = nextPeerId++;
        this.peers.set(peerId, peer);
        this.openDocs.set(peerId, new Set());
        this.idleSince = null;
        return peerId;
    }

    detach(peerId: number, now: number) {
        if (!this.peers.delete(peerId)) return;
        for (const [serverId, origin] of this.pending) {
            if (origin.peer === peerId) this.pending.delete(serverId);
        }
        this.initWaiters = this.initWaiters.filter(w => w.peer !== peerId);

        const docs = this.openDocs.get(peerId) ?? new Set<string>();
        this.openDocs.delete(peerId);
        const stillOpen = new Set([...this.openDocs.values()].flatMap(d => [...d]));
        for (const uri of docs) {
            if (stillOpen.has(uri)) continue;
            this.send({
                jsonrpc: '2.0',
                method: 'textDocument/didClose',
                params: { textDocument: { uri } },
            });
        }

        if (this.peers.size === 0) this.idleSince = now;
    }

    /** A message from a peer, on its way to the server. */
    fromPeer(peerId: number, body: string) {
        const peer = this.peers.get(peerId);
        if (!peer || this.closed) return;
        let msg: JsonRpcMessage;
        try {
            msg = JSON.parse(body);
        } catch {
            console.error('[LSP] Unparseable message from client:', body.slice(0, 200));
            return;
        }
        const isRequest = msg.method !== undefined && msg.id !== undefined && msg.id !== null;

        switch (msg.method) {
            case 'initialize':
                if (!isRequest) return;
                if (this.initReply) {
                    peer.send(JSON.stringify({ ...this.initReply, id: msg.id }));
                    return;
                }
                this.initWaiters.push({ peer: peerId, id: msg.id as JsonRpcId });
                if (this.initServerId === null) {
                    this.initServerId = this.nextServerId++;
                    this.send({ ...msg, id: this.initServerId });
                }
                return;
            case 'initialized':
                if (this.initializedSent) return;
                this.initializedSent = true;
                break;
            // The pool decides when a shared server stops, not any one client.
            case 'shutdown':
                if (isRequest)
                    peer.send(JSON.stringify({ jsonrpc: '2.0', id: msg.id, result: null }));
                return;
            case 'exit':
                return;
            case 'textDocument/didOpen':
                this.openDocs.get(peerId)?.add(msg.params?.textDocument?.uri);
                break;
            case 'textDocument/didClose':
                this.openDocs.get(peerId)?.delete(msg.params?.textDocument?.uri);
                break;
            case '$/cancelRequest': {
                const serverId = this.serverIdFor(peerId, msg.params?.id);
                if (serverId !== null)
                    this.send({ ...msg, params: { ...msg.params, id: serverId } });
                return;
            }
        }

        if (isRequest) {
            const serverId = this.nextServerId++;
            this.pending.set(serverId, { peer: peerId, id: msg.id as JsonRpcId });
            this.send({ ...msg, id: serverId });
            return;
        }
        // A notification, or a reply to a request the server made: the server
        // chose that id, so it passes through as is.
        this.server.send(body);
    }

    /** A message from the server, on its way to whichever peer it's for. */
    fromServer(body: string) {
        let msg: JsonRpcMessage;
        try {
            msg = JSON.parse(body);
        } catch {
            console.error('[LSP] Unparseable message from server:', body.slice(0, 200));
            return;
        }

        if (msg.method !== undefined) {
            // A notification or a server-to-client request: every client sees it.
            for (const peer of this.peers.values()) peer.send(body);
            return;
        }
        if (typeof msg.id !== 'number') return;

        if (msg.id === this.initServerId) {
            if (msg.error) {
                // Let the next client try again rather than replaying a failure.
                this.initServerId = null;
            } else {
                this.initReply = msg;
            }
            for (const waiter of this.initWaiters) {
                this.peers.get(waiter.peer)?.send(JSON.stringify({ ...msg, id: waiter.id }));
            }
            this.initWaiters = [];
            return;
        }

        const origin = this.pending.get(msg.id);
        if (!origin) return;
        this.pending.delete(msg.id);
        this.peers.get(origin.peer)?.send(JSON.stringify({ ...msg, id: origin.id }));
    }

    /** Stops the server and disconnects every peer. */
    close(reason: string) {
        if (this.closed) return;
        this.closed = true;
        const peers = [...this.peers.values()];
        this.peers.clear();
        this.pending.clear();
        this.server.kill();
        for (const peer of peers) peer.close(1011, reason);
    }

    private serverIdFor(peerId: number, id: JsonRpcId | undefined): number | null {
        for (const [serverId, origin] of this.pending) {
            if (origin.peer === peerId && origin.id === id) return serverId;
        }
        return null;
    }

    private send(msg: JsonRpcMessage) {
        this.server.send(JSON.stringify(msg));
    }
}

export interface LspPoolOptions {
    /** How long a session with no peers stays up. */
    idleTimeoutMs: number;
    /** Most sessions kept with no peers; the longest idle go first. */
    maxIdle: number;
    /** Starts a language server; onExit fires once, when it stops for any reason. */
    start(argv: string[], onMessage: (body: string) => void, onExit: () => void): LspServer;
}

export class LspSessionPool {
    private sessions: LspSession[] = [];

    constructor(private opts: LspPoolOptions) {}

    get size(): number {
        return this.sessions.length;
    }

    /**
     * A running session for `key` that covers `langs`, or a new one started
     * with `argv`. The caller attaches to it and hands it back via release().
     */
    acquire(key: string, langs: string[], argv: string[]): LspSession {
        const running = this.sessions.find(
            s => !s.closed && s.key === key && langs.every(l => s.langs.includes(l))
        );
        if (running) return running;

        let session: LspSession | null = null;
        const server = this.opts.start(
            argv,
            body => session?.fromServer(body),
            () => {
                if (session) this.drop(session, 'language server exited');
            }
        );
        session = new LspSession(key, langs, server);
        this.sessions.push(session);
        return session;
    }

    release(session: LspSession, peerId: number, now: number) {
        session.detach(peerId, now);
        const idle = this.sessions
            .filter(s => s.idleSince !== null)
            .sort((a, b) => (a.idleSince ?? 0) - (b.idleSince ?? 0));
        while (idle.length > this.opts.maxIdle) {
            this.drop(idle.shift() as LspSession, 'too many idle language servers');
        }
    }

    /** Stops every session idle for longer than the timeout. */
    evictIdle(now: number) {
        for (const session of [...this.sessions]) {
            if (session.idleSince !== null && now - session.idleSince >= this.opts.idleTimeoutMs) {
                this.drop(session, 'idle');
            }
        }
    }

    private drop(session: LspSession, reason: string) {
        this.sessions = this.sessions.filter(s => s !== session);
        session.close(reason);
    }
}

/** Spawns a stdio language server. */
export function startLspServer(
    argv: string[],
    onMessage: (body: string) => void,
    onExit: () => void
): LspServer {
    const proc = spawn(argv, {
        stdin: 'pipe',
        stdout: 'pipe',
        stderr: 'inherit',
        env: process.env,
    });
    console.log(`[LSP] Started ${argv.join(' ')} (pid ${proc.pid})`);

    const parser = new LspFrameParser(onMessage);
    (async () => {
        const reader = proc.stdout.getReader();
        try {
            while (true) {
                const { done, value } = await reader.read();
                if (done) break;
                parser.push(value);
            }
        } catch (e) {
            console.error('[LSP] Error reading from language server:', e);
        }
    })();
    proc.exited.then(code => {
        console.log(`[LSP] ${argv[0]} (pid ${proc.pid}) exited with ${code}`);
        onExit();
    });

    return {
        send(body) {
            try {
                proc.stdin.write(frameLspMessage(body));
                proc.stdin.flush();
            } catch (e) {
                console.error('[LSP] Error writing to language server:', e);
            }
        },
        kill() {
            proc.kill();
        },
    };
}
