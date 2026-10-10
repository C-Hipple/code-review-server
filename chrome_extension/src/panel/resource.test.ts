import { describe, expect, test } from 'bun:test';
import { RpcError } from '../rpc';
import { failed, fatalError, isFatal, loaded, loading, settled, toRpcError } from './resource';

describe('resources', () => {
    test('a failed refresh keeps the value it had', () => {
        const before = loaded([1, 2]);
        const after = failed(new RpcError('timeout', 'slow'), loading(before));
        expect(after.value).toEqual([1, 2]);
        expect(after.error?.kind).toBe('timeout');
        expect(after.loading).toBe(false);
    });

    test('settled', () => {
        expect(settled({ status: 'fulfilled', value: 3 })).toEqual(loaded(3));
        const rejected = settled<number>(
            { status: 'rejected', reason: new Error('boom') },
            loaded(1)
        );
        expect(rejected.value).toBe(1);
        expect(rejected.error?.kind).toBe('extension');
        expect(rejected.error?.message).toBe('boom');
    });

    test('toRpcError keeps an RpcError as is', () => {
        const e = new RpcError('server', 'gone', '/log');
        expect(toRpcError(e)).toBe(e);
        expect(toRpcError('text').message).toBe('text');
    });

    test('fatal kinds take over the panel', () => {
        expect(isFatal('host-missing')).toBe(true);
        expect(isFatal('host-forbidden')).toBe(true);
        expect(isFatal('server')).toBe(true);
        for (const kind of ['rpc', 'timeout', 'disconnected', 'extension'] as const) {
            expect(isFatal(kind)).toBe(false);
        }
        const rpc = failed(new RpcError('rpc', 'not found'));
        const missing = failed(new RpcError('host-missing', 'no host'));
        expect(fatalError(loaded(1), rpc)).toBeNull();
        expect(fatalError(loaded(1), rpc, missing)?.kind).toBe('host-missing');
    });
});
