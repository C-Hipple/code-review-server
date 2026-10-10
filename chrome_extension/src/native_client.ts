// The service worker's connection to the native host: one port, opened on the
// first call and reused, carrying JSON-RPC requests out and responses (whole
// or chunked) and host events back. The port is injected so tests can drive
// it without Chrome; background.ts passes chrome.runtime.connectNative.
//
// One open port is one running server: the host starts `codereviewserver
// --server` when Chrome connects and stops it when the port closes. An open
// native port also keeps the MV3 service worker alive (Chrome >= 105), so
// the server lives as long as the browser does.

import {
    buildRequest,
    ChunkAssembler,
    ChunkProtocolError,
    classifyHostMessage,
    disconnectError,
    hostFailureError,
    rpcErrorMessage,
    type ErrorInfo,
    type HostFailure,
    type HostReady,
    type JsonRpcResponse,
} from './native_protocol';
import type { HostStatus, RpcReply } from './types';

/** GetPR on a cache miss goes to GitHub, so be generous. */
export const DEFAULT_TIMEOUT_MS = 120_000;

/** The parts of chrome.runtime.Port the client uses. */
export interface NativePort {
    postMessage(message: unknown): void;
    onMessage: { addListener(callback: (message: unknown) => void): void };
    onDisconnect: { addListener(callback: () => void): void };
}

export interface NativeClientOptions {
    /** Opens a port to the host; throws if Chrome refuses outright. */
    connect: () => NativePort;
    /**
     * chrome.runtime.lastError?.message. Chrome sets it only for the duration
     * of the onDisconnect callback, which is where it is read.
     */
    lastError: () => string | undefined;
    timeoutMs?: number;
}

interface Pending {
    resolve: (reply: RpcReply) => void;
    timer: ReturnType<typeof setTimeout>;
}

export class NativeClient {
    private readonly connect: () => NativePort;
    private readonly readLastError: () => string | undefined;
    private readonly timeoutMs: number;

    private port: NativePort | null = null;
    private nextId = 1;
    private readonly pending = new Map<number, Pending>();
    private readonly chunks = new ChunkAssembler();
    /** The current port's host has started the server. */
    private ready = false;
    /** The current port's host reported a failure; it exits next. */
    private failure: HostFailure | null = null;
    private lastReady: HostReady | null = null;
    private lastError: ErrorInfo | undefined;

    constructor(options: NativeClientOptions) {
        this.connect = options.connect;
        this.readLastError = options.lastError;
        this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    }

    /**
     * Calls a server method. `params` is the args object; it is wrapped into
     * JSON-RPC's one-element array here (buildRequest). Never rejects: a
     * failure comes back as `{ok: false, error}`.
     */
    call(method: string, params: unknown, timeoutMs = this.timeoutMs): Promise<RpcReply> {
        return new Promise(resolve => {
            let port: NativePort;
            try {
                port = this.ensurePort();
            } catch (e) {
                this.lastError = disconnectError(errorText(e));
                resolve({ ok: false, error: this.lastError });
                return;
            }

            const id = this.nextId++;
            const timer = setTimeout(() => {
                this.settle(id, {
                    ok: false,
                    error: {
                        kind: 'timeout',
                        message: `${method} got no answer within ${Math.round(timeoutMs / 1000)} s.`,
                    },
                });
            }, timeoutMs);
            this.pending.set(id, { resolve, timer });

            try {
                port.postMessage(buildRequest(id, method, params));
            } catch (e) {
                // The port died between ensurePort and here; onDisconnect will
                // clear it, but this call has already failed.
                this.settle(id, {
                    ok: false,
                    error: { kind: 'disconnected', message: errorText(e) },
                });
            }
        });
    }

    status(): HostStatus {
        const status: HostStatus = { connected: this.port !== null && this.ready };
        if (this.lastReady) {
            const { server_path, log_path, version } = this.lastReady;
            status.host = { server_path, log_path, version };
        }
        if (this.lastError) status.lastError = this.lastError;
        return status;
    }

    private ensurePort(): NativePort {
        if (this.port) return this.port;
        const port = this.connect();
        this.port = port;
        this.ready = false;
        this.failure = null;
        this.chunks.reset();
        // Events from a port that has since been replaced are ignored.
        port.onMessage.addListener(msg => {
            if (this.port === port) this.onMessage(msg);
        });
        port.onDisconnect.addListener(() => {
            if (this.port === port) this.onDisconnect();
        });
        return port;
    }

    private onMessage(msg: unknown): void {
        const classified = classifyHostMessage(msg);
        switch (classified.type) {
            case 'response':
                this.onResponse(classified.response);
                return;
            case 'chunk':
                this.onChunk(classified.chunk);
                return;
            case 'host':
                if (classified.event.event === 'ready') {
                    this.ready = true;
                    this.lastReady = classified.event;
                    this.lastError = undefined;
                } else {
                    // The host exits after this; fail fast rather than wait for
                    // the disconnect, and keep the reason for it.
                    this.failure = classified.event;
                    this.lastError = hostFailureError(classified.event);
                    this.failAll(this.lastError);
                }
                return;
            case 'invalid':
                console.warn('crs: ignoring a message from the native host:', classified.reason);
                return;
        }
    }

    private onChunk(chunk: Parameters<ChunkAssembler['push']>[0]): void {
        let text: string | undefined;
        try {
            text = this.chunks.push(chunk);
        } catch (e) {
            // The response it carried is lost; its call fails by timeout. The
            // host serializes its writes, so this is a host bug, not a race.
            if (e instanceof ChunkProtocolError) {
                console.error('crs: dropped a chunked response:', e.message);
                return;
            }
            throw e;
        }
        if (text === undefined) return;

        let parsed: unknown;
        try {
            parsed = JSON.parse(text);
        } catch (e) {
            console.error('crs: a chunked response is not valid JSON:', errorText(e));
            return;
        }
        const inner = classifyHostMessage(parsed);
        if (inner.type === 'response') {
            this.onResponse(inner.response);
        } else {
            console.error('crs: a chunked message is not a JSON-RPC response');
        }
    }

    private onResponse(response: JsonRpcResponse): void {
        if (!this.pending.has(response.id)) {
            // Most likely a call that already timed out.
            console.warn(`crs: response for unknown or expired call ${response.id}`);
            return;
        }
        if (response.error !== null && response.error !== undefined) {
            this.settle(response.id, {
                ok: false,
                error: { kind: 'rpc', message: rpcErrorMessage(response.error) },
            });
        } else {
            this.settle(response.id, { ok: true, result: response.result });
        }
    }

    private onDisconnect(): void {
        const error = disconnectError(this.readLastError(), this.failure);
        this.port = null;
        this.ready = false;
        this.failure = null;
        this.chunks.reset();
        this.lastError = error;
        this.failAll(error);
    }

    private failAll(error: ErrorInfo): void {
        for (const id of [...this.pending.keys()]) {
            this.settle(id, { ok: false, error });
        }
    }

    private settle(id: number, reply: RpcReply): void {
        const call = this.pending.get(id);
        if (!call) return;
        this.pending.delete(id);
        clearTimeout(call.timer);
        call.resolve(reply);
    }
}

function errorText(e: unknown): string {
    return e instanceof Error ? e.message : String(e);
}
