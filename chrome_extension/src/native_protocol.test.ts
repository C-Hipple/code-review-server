import { describe, expect, test } from 'bun:test';
import {
    buildRequest,
    type Chunk,
    ChunkAssembler,
    ChunkProtocolError,
    classifyHostMessage,
    disconnectError,
    rpcErrorMessage,
} from './native_protocol';

/** Splits `text` into a stream's chunks of at most `size` UTF-16 units. */
function chunksOf(stream: number, text: string, size: number): Chunk[] {
    const parts: string[] = [];
    for (let i = 0; i < text.length; i += size) parts.push(text.slice(i, i + size));
    return parts.map((data, seq) => ({ stream, seq, total: parts.length, data }));
}

describe('buildRequest', () => {
    test('wraps params in a one-element array', () => {
        expect(buildRequest(7, 'RPCHandler.GetPR', { Owner: 'o', Repo: 'r', Number: 1 })).toEqual({
            method: 'RPCHandler.GetPR',
            params: [{ Owner: 'o', Repo: 'r', Number: 1 }],
            id: 7,
        });
    });

    test('sends an empty args object when there are none', () => {
        expect(buildRequest(1, 'RPCHandler.ListPlugins', undefined).params).toEqual([{}]);
        expect(buildRequest(1, 'RPCHandler.ListPlugins', null).params).toEqual([{}]);
    });
});

describe('classifyHostMessage', () => {
    test('a JSON-RPC response', () => {
        const msg = { id: 3, result: { okay: true }, error: null };
        expect(classifyHostMessage(msg)).toEqual({ type: 'response', response: msg });
    });

    test('a chunk', () => {
        const chunk = { stream: 1, seq: 0, total: 2, data: '{"id"' };
        expect(classifyHostMessage({ crs_chunk: chunk })).toEqual({ type: 'chunk', chunk });
    });

    test('a ready event', () => {
        expect(
            classifyHostMessage({
                crs_host: {
                    event: 'ready',
                    server_path: '/bin/crs',
                    log_path: '/l',
                    version: '1',
                },
            })
        ).toEqual({
            type: 'host',
            event: { event: 'ready', server_path: '/bin/crs', log_path: '/l', version: '1' },
        });
    });

    test('error and server_exited events, with a default message', () => {
        expect(
            classifyHostMessage({
                crs_host: { event: 'error', message: 'not found', log_path: '/l' },
            })
        ).toEqual({
            type: 'host',
            event: { event: 'error', message: 'not found', log_path: '/l' },
        });
        expect(classifyHostMessage({ crs_host: { event: 'server_exited' } })).toEqual({
            type: 'host',
            event: { event: 'server_exited', message: 'The server exited.' },
        });
    });

    test.each([
        ['a string', 'hello'],
        ['null', null],
        ['an array', [1, 2]],
        ['a chunk without data', { crs_chunk: { stream: 1, seq: 0, total: 1 } }],
        [
            'a chunk with a fractional seq',
            { crs_chunk: { stream: 1, seq: 0.5, total: 1, data: '' } },
        ],
        ['an unknown host event', { crs_host: { event: 'hello' } }],
        ['a response without error', { id: 1, result: {} }],
        ['a response with a string id', { id: '1', result: {}, error: null }],
    ])('rejects %s', (_, msg) => {
        expect(classifyHostMessage(msg).type).toBe('invalid');
    });
});

describe('ChunkAssembler', () => {
    test('a one-chunk stream completes at once', () => {
        const a = new ChunkAssembler();
        expect(a.push({ stream: 1, seq: 0, total: 1, data: '{"id":1}' })).toBe('{"id":1}');
        expect(a.pending).toBe(false);
    });

    test('concatenates a stream in order, multi-byte text included', () => {
        const text = JSON.stringify({ id: 9, result: { body: 'héllo ✓ 😀 '.repeat(500) } });
        const a = new ChunkAssembler();
        const chunks = chunksOf(4, text, 333);
        expect(chunks.length).toBeGreaterThan(3);
        for (const c of chunks.slice(0, -1)) {
            expect(a.push(c)).toBeUndefined();
            expect(a.pending).toBe(true);
        }
        expect(a.push(chunks[chunks.length - 1])).toBe(text);
        expect(JSON.parse(text).id).toBe(9);
    });

    test('handles streams one after another', () => {
        const a = new ChunkAssembler();
        for (const [stream, text] of [
            [1, 'first stream'],
            [2, 'second one'],
        ] as const) {
            const out = chunksOf(stream, text, 4).map(c => a.push(c));
            expect(out[out.length - 1]).toBe(text);
        }
    });

    test('rejects a stream interleaved with an unfinished one, and drops both', () => {
        const a = new ChunkAssembler();
        a.push({ stream: 1, seq: 0, total: 2, data: 'a' });
        expect(() => a.push({ stream: 2, seq: 0, total: 2, data: 'b' })).toThrow(
            ChunkProtocolError
        );
        expect(a.pending).toBe(false);
        // The rest of either stream can't complete anything now.
        expect(() => a.push({ stream: 1, seq: 1, total: 2, data: 'a' })).toThrow(
            /starts at chunk 1/
        );
        expect(() => a.push({ stream: 2, seq: 1, total: 2, data: 'b' })).toThrow(
            /starts at chunk 1/
        );
    });

    test('rejects a gap', () => {
        const a = new ChunkAssembler();
        a.push({ stream: 1, seq: 0, total: 3, data: 'a' });
        expect(() => a.push({ stream: 1, seq: 2, total: 3, data: 'c' })).toThrow(/expected 1/);
        expect(a.pending).toBe(false);
    });

    test('rejects a repeated chunk', () => {
        const a = new ChunkAssembler();
        a.push({ stream: 1, seq: 0, total: 3, data: 'a' });
        a.push({ stream: 1, seq: 1, total: 3, data: 'b' });
        expect(() => a.push({ stream: 1, seq: 1, total: 3, data: 'b' })).toThrow(
            ChunkProtocolError
        );
    });

    test('rejects a change of total mid-stream', () => {
        const a = new ChunkAssembler();
        a.push({ stream: 1, seq: 0, total: 3, data: 'a' });
        expect(() => a.push({ stream: 1, seq: 1, total: 2, data: 'b' })).toThrow(
            ChunkProtocolError
        );
    });

    test('rejects a stream that does not start at 0', () => {
        expect(() => new ChunkAssembler().push({ stream: 1, seq: 1, total: 2, data: 'b' })).toThrow(
            ChunkProtocolError
        );
    });

    test.each([
        ['total 0', { stream: 1, seq: 0, total: 0, data: '' }],
        ['seq past total', { stream: 1, seq: 2, total: 2, data: '' }],
        ['negative seq', { stream: 1, seq: -1, total: 2, data: '' }],
    ])('rejects %s', (_, chunk) => {
        expect(() => new ChunkAssembler().push(chunk)).toThrow(/out of range/);
    });

    test('a new stream recovers after an error', () => {
        const a = new ChunkAssembler();
        a.push({ stream: 1, seq: 0, total: 2, data: 'a' });
        expect(() => a.push({ stream: 1, seq: 0, total: 2, data: 'a' })).toThrow();
        expect(a.push({ stream: 2, seq: 0, total: 2, data: 'ok ' })).toBeUndefined();
        expect(a.push({ stream: 2, seq: 1, total: 2, data: 'again' })).toBe('ok again');
    });

    test('reset drops a part-way stream', () => {
        const a = new ChunkAssembler();
        a.push({ stream: 1, seq: 0, total: 2, data: 'a' });
        a.reset();
        expect(a.pending).toBe(false);
        expect(a.push({ stream: 2, seq: 0, total: 1, data: 'b' })).toBe('b');
    });
});

describe('disconnectError', () => {
    test('host not installed', () => {
        const e = disconnectError('Specified native messaging host not found.');
        expect(e.kind).toBe('host-missing');
        expect(e.message).toContain('install.sh');
    });

    test('host forbids this extension', () => {
        expect(
            disconnectError('Access to the specified native messaging host is forbidden.').kind
        ).toBe('host-forbidden');
    });

    test('anything else is a plain disconnect', () => {
        expect(disconnectError('Native host has exited.')).toEqual({
            kind: 'disconnected',
            message: 'The native host disconnected: Native host has exited.',
        });
        expect(disconnectError(undefined).kind).toBe('disconnected');
    });

    test("the host's own failure report wins over Chrome's message", () => {
        expect(
            disconnectError('Native host has exited.', {
                event: 'server_exited',
                message: 'exit status 1',
                log_path: '/home/u/.crs/native_host.log',
            })
        ).toEqual({
            kind: 'server',
            message: 'exit status 1',
            logPath: '/home/u/.crs/native_host.log',
        });
    });
});

describe('rpcErrorMessage', () => {
    test("Go's string error", () => {
        expect(rpcErrorMessage('no such PR')).toBe('no such PR');
    });
    test('an object with a message', () => {
        expect(rpcErrorMessage({ code: 1, message: 'bad' })).toBe('bad');
    });
    test('anything else is stringified', () => {
        expect(rpcErrorMessage({ code: 1 })).toBe('{"code":1}');
    });
});
