import { afterEach, beforeEach, describe, expect, mock, test } from 'bun:test';
import {
    getAIOutput,
    getAllReviews,
    getPluginOutput,
    getPR,
    getStatus,
    listAIFeatures,
    listPlugins,
    rerunPlugins,
    RpcError,
    runAIFeature,
    syncPR,
} from './rpc';

const PR = { owner: 'octocat', repo: 'hello', number: 42 };
const ARGS = { Owner: 'octocat', Repo: 'hello', Number: 42 };

let reply: unknown;
const sendMessage = mock(async (_message: unknown): Promise<unknown> => reply);
const original = (globalThis as { chrome?: unknown }).chrome;

beforeEach(() => {
    reply = { ok: true, result: {} };
    sendMessage.mockClear();
    sendMessage.mockImplementation(async () => reply);
    (globalThis as { chrome?: unknown }).chrome = { runtime: { id: 'ext', sendMessage } };
});

afterEach(() => {
    (globalThis as { chrome?: unknown }).chrome = original;
});

function sent(): unknown {
    expect(sendMessage).toHaveBeenCalledTimes(1);
    return sendMessage.mock.calls[0][0];
}

describe('requests', () => {
    // Params go out as the args object; the service worker wraps them for Go.
    test.each([
        ['getAllReviews', () => getAllReviews(), 'RPCHandler.GetAllReviews', {}],
        ['getPR', () => getPR(PR), 'RPCHandler.GetPR', { ...ARGS, SkipCache: false }],
        [
            'getPR skipping the cache',
            () => getPR(PR, { skipCache: true }),
            'RPCHandler.GetPR',
            { ...ARGS, SkipCache: true },
        ],
        ['syncPR', () => syncPR(PR), 'RPCHandler.SyncPR', ARGS],
        ['listPlugins', () => listPlugins(), 'RPCHandler.ListPlugins', {}],
        ['getPluginOutput', () => getPluginOutput(PR), 'RPCHandler.GetPluginOutput', ARGS],
        ['rerunPlugins (all)', () => rerunPlugins(PR), 'RPCHandler.RerunPlugins', ARGS],
        [
            'rerunPlugins (empty list = all)',
            () => rerunPlugins(PR, []),
            'RPCHandler.RerunPlugins',
            ARGS,
        ],
        [
            'rerunPlugins (one)',
            () => rerunPlugins(PR, ['lint']),
            'RPCHandler.RerunPlugins',
            { ...ARGS, Plugins: ['lint'] },
        ],
        ['listAIFeatures', () => listAIFeatures(), 'RPCHandler.ListAIFeatures', {}],
        [
            'runAIFeature',
            () => runAIFeature(PR, 'change-diagram'),
            'RPCHandler.RunAIFeature',
            { ...ARGS, Feature: 'change-diagram', Force: false },
        ],
        [
            'runAIFeature forced',
            () => runAIFeature(PR, 'change-diagram', { force: true }),
            'RPCHandler.RunAIFeature',
            { ...ARGS, Feature: 'change-diagram', Force: true },
        ],
        [
            'getAIOutput (all)',
            () => getAIOutput(PR),
            'RPCHandler.GetAIOutput',
            { ...ARGS, Feature: '' },
        ],
        [
            'getAIOutput (one)',
            () => getAIOutput(PR, 'review-ease'),
            'RPCHandler.GetAIOutput',
            { ...ARGS, Feature: 'review-ease' },
        ],
    ] as const)('%s', async (_, run, method, params) => {
        await run();
        expect(sent()).toEqual({ type: 'crs-rpc', method, params });
    });

    test('getStatus', async () => {
        const status = {
            connected: true,
            host: { server_path: '/s', log_path: '/l', version: 'v' },
        };
        reply = status;
        expect(await getStatus()).toEqual(status);
        expect(sent()).toEqual({ type: 'crs-status' });
    });
});

describe('replies', () => {
    test('unwraps the list and map replies', async () => {
        reply = { ok: true, result: { content: '* org', items: [{ number: 1 }] } };
        expect(await getAllReviews()).toEqual([{ number: 1 }] as never);
        reply = { ok: true, result: { plugins: [{ Name: 'lint' }] } };
        expect(await listPlugins()).toEqual([{ Name: 'lint' }] as never);
        reply = { ok: true, result: { output: { lint: { status: 'success' } } } };
        expect(await getPluginOutput(PR)).toEqual({ lint: { status: 'success' } } as never);
    });

    test("normalises Go's null slices and maps", async () => {
        reply = { ok: true, result: { content: '', items: null } };
        expect(await getAllReviews()).toEqual([]);
        reply = { ok: true, result: { plugins: null } };
        expect(await listPlugins()).toEqual([]);
        reply = { ok: true, result: { features: null } };
        expect(await listAIFeatures()).toEqual([]);
        reply = { ok: true, result: { output: null } };
        expect(await getPluginOutput(PR)).toEqual({});
        expect(await getAIOutput(PR)).toEqual({});
    });

    test('returns whole replies as they are', async () => {
        reply = {
            ok: true,
            result: { okay: true, outcome: 'started', message: 'm', output: null },
        };
        expect(await runAIFeature(PR, 'x')).toEqual({
            okay: true,
            outcome: 'started',
            message: 'm',
            output: null,
        });
    });
});

describe('errors', () => {
    async function thrown(p: Promise<unknown>): Promise<RpcError> {
        try {
            await p;
        } catch (e) {
            expect(e).toBeInstanceOf(RpcError);
            return e as RpcError;
        }
        throw new Error('expected a rejection');
    }

    test('throws the service worker error with its kind and log path', async () => {
        reply = {
            ok: false,
            error: {
                kind: 'server',
                message: 'server exited',
                logPath: '/home/u/.crs/native_host.log',
            },
        };
        const e = await thrown(getPR(PR));
        expect(e.kind).toBe('server');
        expect(e.message).toBe('server exited');
        expect(e.logPath).toBe('/home/u/.crs/native_host.log');
    });

    test.each(['host-missing', 'host-forbidden', 'rpc', 'timeout', 'disconnected'] as const)(
        'keeps kind %s',
        async kind => {
            reply = { ok: false, error: { kind, message: 'm' } };
            const e = await thrown(listPlugins());
            expect(e.kind).toBe(kind);
            expect(e.logPath).toBeUndefined();
        }
    );

    test('an unreachable service worker is an extension error', async () => {
        sendMessage.mockImplementation(async () => {
            throw new Error('Could not establish connection. Receiving end does not exist.');
        });
        const e = await thrown(getAllReviews());
        expect(e.kind).toBe('extension');
        expect(e.message).toContain('Receiving end does not exist');
    });

    test.each([
        ['no reply', undefined],
        ['a reply without ok', { result: 1 }],
        ['a failure without an error', { ok: false }],
    ])('%s is an extension error', async (_, r) => {
        reply = r;
        expect((await thrown(getAllReviews())).kind).toBe('extension');
    });

    test('a malformed status reply is an extension error', async () => {
        reply = undefined;
        expect((await thrown(getStatus())).kind).toBe('extension');
    });
});
