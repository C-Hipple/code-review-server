import { afterAll, describe, expect, test } from 'bun:test';
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import { readLocationLines } from './lsp_lines';
import {
    diffLspWorkspace,
    frameLspMessage,
    LspFrameParser,
    type LspPoolOptions,
    LspSessionPool,
} from './lsp_pool';

// biome-ignore lint/suspicious/noExplicitAny: decoded JSON-RPC messages
type Msg = any;

interface FakeServer {
    argv: string[];
    sent: Msg[];
    killed: boolean;
    reply(msg: Msg): void;
    exit(): void;
}

function fakePool(opts: Partial<LspPoolOptions> = {}) {
    const servers: FakeServer[] = [];
    const pool = new LspSessionPool({
        idleTimeoutMs: 1000,
        maxIdle: 2,
        ...opts,
        start(argv, onMessage, onExit) {
            const server: FakeServer = {
                argv,
                sent: [],
                killed: false,
                reply: msg => onMessage(JSON.stringify(msg)),
                exit: onExit,
            };
            servers.push(server);
            return {
                send: body => server.sent.push(JSON.parse(body)),
                kill: () => {
                    server.killed = true;
                },
            };
        },
    });
    return { pool, servers };
}

function fakePeer() {
    const peer = {
        sent: [] as Msg[],
        closed: null as { code?: number; reason?: string } | null,
        send: (body: string) => peer.sent.push(JSON.parse(body)),
        close: (code?: number, reason?: string) => {
            peer.closed = { code, reason };
        },
    };
    return peer;
}

const request = (id: number, method: string, params: Msg = {}) =>
    JSON.stringify({ jsonrpc: '2.0', id, method, params });
const notify = (method: string, params: Msg = {}) =>
    JSON.stringify({ jsonrpc: '2.0', method, params });

describe('LspFrameParser', () => {
    test('reassembles a message split across chunks', () => {
        const bodies: string[] = [];
        const parser = new LspFrameParser(b => bodies.push(b));
        const framed = frameLspMessage('{"id":1,"result":"ok"}');
        parser.push(Buffer.from(framed.slice(0, 10)));
        parser.push(Buffer.from(framed.slice(10, 30)));
        expect(bodies).toEqual([]);
        parser.push(Buffer.from(framed.slice(30)));
        expect(bodies).toEqual(['{"id":1,"result":"ok"}']);
    });

    test('splits several messages in one chunk', () => {
        const bodies: string[] = [];
        const parser = new LspFrameParser(b => bodies.push(b));
        parser.push(Buffer.from(frameLspMessage('{"a":1}') + frameLspMessage('{"b":2}')));
        expect(bodies).toEqual(['{"a":1}', '{"b":2}']);
    });

    test('counts Content-Length in bytes, not characters', () => {
        const body = '{"hover":"→ func λ()"}';
        const framed = frameLspMessage(body);
        expect(framed).toStartWith(`Content-Length: ${Buffer.byteLength(body)}\r\n\r\n`);
        const bodies: string[] = [];
        const parser = new LspFrameParser(b => bodies.push(b));
        parser.push(Buffer.from(framed + frameLspMessage('{}')));
        expect(bodies).toEqual([body, '{}']);
    });
});

describe('diffLspWorkspace', () => {
    const tempfile = (worktree: string, files: string) =>
        `Project: crs\nRoot: /src/crs\nWorktree: ${worktree}\nBuffer: PR #1\nType: code-review\n${files}`;

    test('reads the root, worktree and backend languages like diff-lsp does', () => {
        const ws = diffLspWorkspace(
            tempfile(
                '/wt/pr-1',
                'modified   server/server.go\nnew file   web/app.tsx\ndiff --git a/lib.rs b/lib.rs\ndeleted    README.md\n+Root: not a header\n'
            )
        );
        expect(ws.key).toBe('diff-lsp\0/src/crs\0/wt/pr-1');
        expect(ws.langs).toEqual(['go', 'rust', 'typescript']);
    });

    test('a missing worktree keys on the root alone', () => {
        expect(diffLspWorkspace(tempfile('', 'modified   a.go\n')).key).toBe(
            'diff-lsp\0/src/crs\0'
        );
        expect(diffLspWorkspace(tempfile('undefined', 'modified   a.go\n')).key).toBe(
            'diff-lsp\0/src/crs\0'
        );
    });
});

describe('LspSession', () => {
    test('answers a later initialize from the first one', () => {
        const { pool, servers } = fakePool();
        const first = fakePeer();
        const session = pool.acquire('k', [], ['diff-lsp']);
        const firstId = session.attach(first);
        session.fromPeer(firstId, request(1, 'initialize', { rootUri: 'file:///' }));

        expect(servers).toHaveLength(1);
        const init = servers[0].sent[0];
        expect(init.method).toBe('initialize');
        servers[0].reply({
            jsonrpc: '2.0',
            id: init.id,
            result: { capabilities: { hoverProvider: true } },
        });
        expect(first.sent).toEqual([
            { jsonrpc: '2.0', id: 1, result: { capabilities: { hoverProvider: true } } },
        ]);
        session.fromPeer(firstId, notify('initialized'));

        const second = fakePeer();
        const secondId = pool.acquire('k', [], ['diff-lsp']).attach(second);
        session.fromPeer(secondId, request(7, 'initialize'));
        session.fromPeer(secondId, notify('initialized'));

        expect(second.sent).toEqual([
            { jsonrpc: '2.0', id: 7, result: { capabilities: { hoverProvider: true } } },
        ]);
        expect(servers).toHaveLength(1);
        expect(servers[0].sent.map(m => m.method)).toEqual(['initialize', 'initialized']);
    });

    test('a peer that asks while initialize is in flight gets the same answer', () => {
        const { pool, servers } = fakePool();
        const session = pool.acquire('k', [], ['diff-lsp']);
        const a = fakePeer();
        const b = fakePeer();
        const aId = session.attach(a);
        const bId = session.attach(b);
        session.fromPeer(aId, request(1, 'initialize'));
        session.fromPeer(bId, request(1, 'initialize'));
        expect(servers[0].sent).toHaveLength(1);

        servers[0].reply({ jsonrpc: '2.0', id: servers[0].sent[0].id, result: { ok: true } });
        expect(a.sent).toEqual([{ jsonrpc: '2.0', id: 1, result: { ok: true } }]);
        expect(b.sent).toEqual([{ jsonrpc: '2.0', id: 1, result: { ok: true } }]);
    });

    test('routes each response to the peer that asked, under its own id', () => {
        const { pool, servers } = fakePool();
        const session = pool.acquire('k', [], ['diff-lsp']);
        const a = fakePeer();
        const b = fakePeer();
        const aId = session.attach(a);
        const bId = session.attach(b);
        session.fromPeer(aId, request(2, 'textDocument/hover'));
        session.fromPeer(bId, request(2, 'textDocument/references'));

        const [hover, refs] = servers[0].sent;
        expect(hover.id).not.toBe(refs.id);
        servers[0].reply({ jsonrpc: '2.0', id: refs.id, result: ['ref'] });
        servers[0].reply({ jsonrpc: '2.0', id: hover.id, result: { contents: 'doc' } });

        expect(a.sent).toEqual([{ jsonrpc: '2.0', id: 2, result: { contents: 'doc' } }]);
        expect(b.sent).toEqual([{ jsonrpc: '2.0', id: 2, result: ['ref'] }]);
    });

    test('translates the id in a cancel request', () => {
        const { pool, servers } = fakePool();
        const session = pool.acquire('k', [], ['diff-lsp']);
        const peerId = session.attach(fakePeer());
        session.fromPeer(peerId, request(5, 'textDocument/references'));
        session.fromPeer(peerId, notify('$/cancelRequest', { id: 5 }));
        session.fromPeer(peerId, notify('$/cancelRequest', { id: 99 }));

        const [refs, cancel] = servers[0].sent;
        expect(cancel).toEqual({
            jsonrpc: '2.0',
            method: '$/cancelRequest',
            params: { id: refs.id },
        });
        expect(servers[0].sent).toHaveLength(2);
    });

    test('broadcasts server notifications', () => {
        const { pool, servers } = fakePool();
        const session = pool.acquire('k', [], ['diff-lsp']);
        const a = fakePeer();
        const b = fakePeer();
        session.attach(a);
        session.attach(b);
        servers[0].reply({
            jsonrpc: '2.0',
            method: 'window/logMessage',
            params: { message: 'hi' },
        });
        expect(a.sent).toHaveLength(1);
        expect(b.sent).toHaveLength(1);
    });

    test("closes a detached peer's documents unless another peer has them open", () => {
        const { pool, servers } = fakePool();
        const session = pool.acquire('k', [], ['gopls']);
        const aId = session.attach(fakePeer());
        const bId = session.attach(fakePeer());
        const open = (uri: string) => notify('textDocument/didOpen', { textDocument: { uri } });
        session.fromPeer(aId, open('file:///a.go'));
        session.fromPeer(aId, open('file:///shared.go'));
        session.fromPeer(bId, open('file:///shared.go'));
        servers[0].sent = [];

        pool.release(session, aId, 0);
        expect(servers[0].sent).toEqual([
            {
                jsonrpc: '2.0',
                method: 'textDocument/didClose',
                params: { textDocument: { uri: 'file:///a.go' } },
            },
        ]);
    });

    test('drops a response whose peer has gone, and never forwards shutdown', () => {
        const { pool, servers } = fakePool();
        const session = pool.acquire('k', [], ['diff-lsp']);
        const a = fakePeer();
        const aId = session.attach(a);
        session.fromPeer(aId, request(3, 'shutdown'));
        session.fromPeer(aId, notify('exit'));
        expect(a.sent).toEqual([{ jsonrpc: '2.0', id: 3, result: null }]);

        session.fromPeer(aId, request(4, 'textDocument/references'));
        const refs = servers[0].sent[0];
        expect(refs.method).toBe('textDocument/references');
        pool.release(session, aId, 0);
        servers[0].reply({ jsonrpc: '2.0', id: refs.id, result: [] });
        expect(a.sent).toHaveLength(1);
        expect(servers[0].killed).toBe(false);
    });
});

describe('LspSessionPool', () => {
    test('reuses a session for the same workspace when it covers the languages', () => {
        const { pool, servers } = fakePool();
        const goRust = pool.acquire('ws', ['go', 'rust'], ['diff-lsp', '/tmp/diff_lsp_1']);
        expect(pool.acquire('ws', ['go'], ['diff-lsp', '/tmp/diff_lsp_2'])).toBe(goRust);
        expect(pool.acquire('ws', ['go', 'python'], ['diff-lsp', '/tmp/diff_lsp_3'])).not.toBe(
            goRust
        );
        expect(pool.acquire('other', ['go'], ['diff-lsp', '/tmp/diff_lsp_4'])).not.toBe(goRust);
        expect(servers.map(s => s.argv[1])).toEqual([
            '/tmp/diff_lsp_1',
            '/tmp/diff_lsp_3',
            '/tmp/diff_lsp_4',
        ]);
    });

    test('keeps a session with no peers until it has been idle for the timeout', () => {
        const { pool, servers } = fakePool({ idleTimeoutMs: 1000 });
        const session = pool.acquire('ws', [], ['diff-lsp']);
        const peerId = session.attach(fakePeer());
        pool.release(session, peerId, 5000);

        pool.evictIdle(5999);
        expect(servers[0].killed).toBe(false);
        const again = pool.acquire('ws', [], ['diff-lsp']);
        expect(again).toBe(session);
        const againId = again.attach(fakePeer());
        pool.evictIdle(10_000);
        expect(servers[0].killed).toBe(false);

        pool.release(again, againId, 20_000);
        pool.evictIdle(21_000);
        expect(servers[0].killed).toBe(true);
        expect(pool.size).toBe(0);
        expect(servers).toHaveLength(1);
    });

    test('stops the longest-idle sessions beyond maxIdle', () => {
        const { pool, servers } = fakePool({ maxIdle: 1 });
        const a = pool.acquire('a', [], ['x']);
        const b = pool.acquire('b', [], ['x']);
        const aId = a.attach(fakePeer());
        const bId = b.attach(fakePeer());
        pool.release(a, aId, 1);
        expect(servers.map(s => s.killed)).toEqual([false, false]);
        pool.release(b, bId, 2);
        expect(servers.map(s => s.killed)).toEqual([true, false]);
        expect(pool.size).toBe(1);
    });

    test('a server that exits is dropped and its peers disconnected', () => {
        const { pool, servers } = fakePool();
        const session = pool.acquire('ws', [], ['diff-lsp']);
        const peer = fakePeer();
        session.attach(peer);
        servers[0].exit();

        expect(peer.closed?.code).toBe(1011);
        expect(pool.size).toBe(0);
        expect(pool.acquire('ws', [], ['diff-lsp'])).not.toBe(session);
        expect(servers).toHaveLength(2);
    });
});

describe('readLocationLines', () => {
    const dir = mkdtempSync(join(tmpdir(), 'lsp-lines-'));
    afterAll(() => rmSync(dir, { recursive: true, force: true }));

    test('returns the text on each requested 0-based line', async () => {
        const file = join(dir, 'main.go');
        writeFileSync(file, 'package main\r\n\r\nfunc main() {\r\n\tserve()\r\n}\r\n');
        const uri = pathToFileURL(file).href;

        const lines = await readLocationLines([
            { uri, line: 2 },
            { uri, line: 3 },
            { uri, line: 40 },
            { uri: pathToFileURL(join(dir, 'missing.go')).href, line: 0 },
            { uri: 'untitled:scratch', line: 0 },
        ]);
        expect(lines).toEqual({ [uri]: { 2: 'func main() {', 3: '\tserve()' } });
    });

    test('truncates long lines', async () => {
        const file = join(dir, 'long.ts');
        writeFileSync(file, `const x = '${'a'.repeat(500)}';\n`);
        const uri = pathToFileURL(file).href;
        const lines = await readLocationLines([{ uri, line: 0 }], 20);
        expect(lines[uri][0]).toHaveLength(20);
    });
});
