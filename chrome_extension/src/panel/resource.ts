// How the panel holds what it fetched: the last value, the last error and
// whether a request is in flight, kept side by side so a failed refresh (a
// poll, a reload) doesn't blank what is already on screen.

import { RpcError, type ErrorKind } from '../rpc';

export interface Resource<T> {
    value: T | null;
    error: RpcError | null;
    loading: boolean;
}

export function loading<T>(previous?: Resource<T>): Resource<T> {
    return { value: previous?.value ?? null, error: null, loading: true };
}

export function loaded<T>(value: T): Resource<T> {
    return { value, error: null, loading: false };
}

/** A failed fetch; whatever was loaded before stays. */
export function failed<T>(error: unknown, previous?: Resource<T>): Resource<T> {
    return { value: previous?.value ?? null, error: toRpcError(error), loading: false };
}

/** The resource after a Promise.allSettled entry for it. */
export function settled<T>(
    result: PromiseSettledResult<T>,
    previous?: Resource<T>
): Resource<T> {
    return result.status === 'fulfilled'
        ? loaded(result.value)
        : failed(result.reason, previous);
}

export function toRpcError(e: unknown): RpcError {
    if (e instanceof RpcError) return e;
    return new RpcError('extension', e instanceof Error ? e.message : String(e));
}

/**
 * Failures no section can recover from by itself — the host isn't
 * installed, isn't allowed, or can't start the server — which take over the
 * whole panel instead of showing inline.
 */
export function isFatal(kind: ErrorKind): boolean {
    return kind === 'host-missing' || kind === 'host-forbidden' || kind === 'server';
}

/** The first fatal error among the resources, if any. */
export function fatalError(...resources: Resource<unknown>[]): RpcError | null {
    for (const r of resources) {
        if (r.error && isFatal(r.error.kind)) return r.error;
    }
    return null;
}
