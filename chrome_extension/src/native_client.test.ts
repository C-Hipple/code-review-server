import { afterAll, describe, expect, spyOn, test } from 'bun:test';
import { NativeClient, type NativePort } from './native_client';

/** A native port the test drives by hand. */
class FakePort implements NativePort {
    sent: unknown[] = [];
    throwOnPost = false;
    private messageListeners: ((m: unknown) => void)[] = [];
    private disconnectListeners: (() => void)[] = [];

    postMessage(message: unknown): void {
        if (this.throwOnPost) throw new Error('Attempting to use a disconnected port object');
        this.sent.push(message);
    }
    onMessage = { addListener: (cb: (m: unknown) => void) => void this.messageListeners.push(cb) };
    onDisconnect = { addListener: (cb: () => void) => void this.disconnectListeners.push(cb) };

    receive(message: unknown): void {
        for (const cb of this.messageListeners) cb(message);
    }
    disconnect(): void {
        for (const cb of this.disconnectListeners) cb();
    }
    lastId(): number {
        return (this.sent[this.sent.length - 1] as { id: number }).id;
    }
}

function setup(timeoutMs?: number) {
    const ports: FakePort[] = [];
    const env = { lastError: undefined as string | undefined };
    const client = new NativeClient({
        connect: () => {
            const port = new FakePort();
            ports.push(port);
            return port;
        },
        lastError: () => env.lastError,
        timeoutMs,
    });
    return { client, ports, env };
}

// The client logs dropped and unexpected messages; keep the test output clean.
const quiet = [
    spyOn(console, 'warn').mockImplementation(() => {}),
    spyOn(console, 'error').mockImplementation(() => {}),
];
afterAll(() => quiet.forEach(s => s.mockRestore()));

const READY = {
    crs_host: {
        event: 'ready',
        server_path: '/go/bin/codereviewserver',
        log_path: '/l',
        version: 'v1',
    },
};

describe('NativeClient', () => {
    test('connects lazily, wraps params and resolves the response', async () => {
        const { client, ports } = setup();
        expect(ports).toHaveLength(0);

        const reply = client.call('RPCHandler.GetPR', { Owner: 'o', Repo: 'r', Number: 5 });
        expect(ports).toHaveLength(1);
        expect(ports[0].sent).toEqual([
            { method: 'RPCHandler.GetPR', params: [{ Owner: 'o', Repo: 'r', Number: 5 }], id: 1 },
        ]);

        ports[0].receive({ id: 1, result: { okay: true }, error: null });
        expect(await reply).toEqual({ ok: true, result: { okay: true } });
    });

    test('reuses the port and matches responses by id', async () => {
        const { client, ports } = setup();
        const a = client.call('RPCHandler.ListPlugins', {});
        const b = client.call('RPCHandler.ListAIFeatures', {});
        expect(ports).toHaveLength(1);
        ports[0].receive({ id: 2, result: 'b', error: null });
        ports[0].receive({ id: 1, result: 'a', error: null });
        expect(await a).toEqual({ ok: true, result: 'a' });
        expect(await b).toEqual({ ok: true, result: 'b' });
    });

    test('a JSON-RPC error is an rpc error', async () => {
        const { client, ports } = setup();
        const reply = client.call('RPCHandler.GetPR', {});
        ports[0].receive({ id: 1, result: null, error: 'pull request not found' });
        expect(await reply).toEqual({
            ok: false,
            error: { kind: 'rpc', message: 'pull request not found' },
        });
    });

    test('reassembles a chunked response', async () => {
        const { client, ports } = setup();
        const reply = client.call('RPCHandler.GetPR', {});
        const text = JSON.stringify({ id: 1, result: { diff: 'x'.repeat(1000) }, error: null });
        const parts = [text.slice(0, 400), text.slice(400, 800), text.slice(800)];
        parts.forEach((data, seq) =>
            ports[0].receive({ crs_chunk: { stream: 1, seq, total: 3, data } })
        );
        expect(await reply).toEqual({ ok: true, result: { diff: 'x'.repeat(1000) } });
    });

    test('reports the host once it is ready', async () => {
        const { client, ports } = setup();
        expect(client.status()).toEqual({ connected: false });
        void client.call('RPCHandler.Hello', {});
        expect(client.status().connected).toBe(false);
        ports[0].receive(READY);
        expect(client.status()).toEqual({
            connected: true,
            host: { server_path: '/go/bin/codereviewserver', log_path: '/l', version: 'v1' },
        });
    });

    test('a missing host fails pending calls with host-missing', async () => {
        const { client, ports, env } = setup();
        const reply = client.call('RPCHandler.GetAllReviews', {});
        env.lastError = 'Specified native messaging host not found.';
        ports[0].disconnect();
        const result = await reply;
        expect(result.ok).toBe(false);
        expect(!result.ok && result.error.kind).toBe('host-missing');
        expect(client.status().connected).toBe(false);
        expect(client.status().lastError?.kind).toBe('host-missing');
    });

    test('a forbidden host fails with host-forbidden', async () => {
        const { client, ports, env } = setup();
        const reply = client.call('RPCHandler.GetAllReviews', {});
        env.lastError = 'Access to the specified native messaging host is forbidden.';
        ports[0].disconnect();
        const result = await reply;
        expect(!result.ok && result.error.kind).toBe('host-forbidden');
    });

    test("a host error event fails calls at once with the host's message and log", async () => {
        const { client, ports, env } = setup();
        const reply = client.call('RPCHandler.GetAllReviews', {});
        ports[0].receive({
            crs_host: { event: 'error', message: 'codereviewserver not found', log_path: '/l' },
        });
        expect(await reply).toEqual({
            ok: false,
            error: { kind: 'server', message: 'codereviewserver not found', logPath: '/l' },
        });
        // The disconnect that follows keeps the host's reason, not Chrome's.
        env.lastError = 'Native host has exited.';
        ports[0].disconnect();
        expect(client.status().lastError).toEqual({
            kind: 'server',
            message: 'codereviewserver not found',
            logPath: '/l',
        });
    });

    test('reconnects on the next call after a disconnect, and ready clears the error', async () => {
        const { client, ports, env } = setup();
        void client.call('RPCHandler.Hello', {});
        env.lastError = 'Native host has exited.';
        ports[0].disconnect();
        expect(client.status().lastError?.kind).toBe('disconnected');

        env.lastError = undefined;
        const reply = client.call('RPCHandler.Hello', {});
        expect(ports).toHaveLength(2);
        ports[1].receive(READY);
        ports[1].receive({ id: ports[1].lastId(), result: 'hi', error: null });
        expect(await reply).toEqual({ ok: true, result: 'hi' });
        expect(client.status().lastError).toBeUndefined();
        expect(client.status().connected).toBe(true);
    });

    test('ignores events from a port that has been replaced', async () => {
        const { client, ports } = setup();
        void client.call('RPCHandler.Hello', {});
        ports[0].disconnect();
        const reply = client.call('RPCHandler.Hello', {});
        ports[1].receive(READY);
        // A late disconnect from the old port must not tear down the new one.
        ports[0].disconnect();
        expect(client.status().connected).toBe(true);
        ports[1].receive({ id: ports[1].lastId(), result: 1, error: null });
        expect(await reply).toEqual({ ok: true, result: 1 });
    });

    test('times out, and ignores the late response', async () => {
        const { client, ports } = setup(20);
        const reply = await client.call('RPCHandler.GetPR', {});
        expect(reply.ok).toBe(false);
        expect(!reply.ok && reply.error.kind).toBe('timeout');
        expect(!reply.ok && reply.error.message).toContain('RPCHandler.GetPR');
        ports[0].receive({ id: 1, result: 'late', error: null }); // no throw
    });

    test('a connect that throws fails the call', async () => {
        const client = new NativeClient({
            connect: () => {
                throw new Error('Specified native messaging host not found.');
            },
            lastError: () => undefined,
        });
        const reply = await client.call('RPCHandler.Hello', {});
        expect(!reply.ok && reply.error.kind).toBe('host-missing');
    });

    test('a post on a dead port fails the call', async () => {
        const { client, ports } = setup();
        void client.call('RPCHandler.Hello', {}); // opens the port
        ports[0].throwOnPost = true;
        const reply = await client.call('RPCHandler.Hello', {});
        expect(!reply.ok && reply.error.kind).toBe('disconnected');
    });

    test('a broken chunk stream leaves other calls working', async () => {
        const { client, ports } = setup();
        const broken = client.call('RPCHandler.GetPR', {}, 30);
        const fine = client.call('RPCHandler.Hello', {});
        ports[0].receive({ crs_chunk: { stream: 1, seq: 0, total: 3, data: '{"id":1,' } });
        ports[0].receive({ crs_chunk: { stream: 1, seq: 2, total: 3, data: '}' } }); // gap
        ports[0].receive({ id: 2, result: 'ok', error: null });
        expect(await fine).toEqual({ ok: true, result: 'ok' });
        const r = await broken;
        expect(!r.ok && r.error.kind).toBe('timeout');
    });
});
