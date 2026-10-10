// The native host's side of the wire, as the service worker reads it, plus the
// error kinds every layer above reports failures with. Pure — no chrome.* — so
// it is unit tested directly.
//
// The host (crs_native_host) bridges Chrome native messaging to the server's
// newline-delimited JSON-RPC. Each native message it sends is one of:
//
// - a JSON-RPC response `{id, result, error}`, forwarded verbatim;
// - a `crs_chunk`: a piece of a response too large for one native message
//   (Chrome caps host → extension messages at 1 MB). A stream's chunks arrive
//   contiguously and in order, since one writer serializes the host's output;
//   concatenating their `data` and JSON.parse-ing the result gives the response;
// - a `crs_host` event: the server started (`ready`), could not be started
//   (`error`), or died (`server_exited`). The host exits after the last two.

/** How a call failed — what the panel decides its error state on. */
export type ErrorKind =
    /** Chrome found no host manifest for com.c_hipple.crs: install.sh hasn't been run. */
    | 'host-missing'
    /** The host manifest exists but its allowed_origins doesn't list this extension's id. */
    | 'host-forbidden'
    /** The host couldn't find or start the server, or the server died; see logPath. */
    | 'server'
    /** The native port closed for any other reason (the host crashed, say). */
    | 'disconnected'
    /** The server answered with a JSON-RPC error. */
    | 'rpc'
    /** No answer within the call's timeout. */
    | 'timeout'
    /** Extension plumbing: the service worker was unreachable or replied with garbage. */
    | 'extension';

/** A failure as it crosses from the service worker to a page. */
export interface ErrorInfo {
    kind: ErrorKind;
    message: string;
    /** The host's log file, when the host told us where it is. */
    logPath?: string;
}

/** What the host reports once it has started the server. */
export interface HostReady {
    event: 'ready';
    server_path: string;
    log_path: string;
    version: string;
}

/** The host could not find or start the server, or the server exited; the host exits next. */
export interface HostFailure {
    event: 'error' | 'server_exited';
    message: string;
    log_path?: string;
}

export type HostEvent = HostReady | HostFailure;

export interface Chunk {
    /** Host-assigned counter; all chunks of one response share it. */
    stream: number;
    /** 0-based position within the stream. */
    seq: number;
    /** Number of chunks in the stream. */
    total: number;
    data: string;
}

/** A JSON-RPC 1.0 response, as Go's net/rpc/jsonrpc writes it. */
export interface JsonRpcResponse {
    id: number;
    result: unknown;
    /** Go sends the error's text as a string, null on success. */
    error: unknown;
}

/** A JSON-RPC 1.0 request; Go's jsonrpc takes params as a one-element array. */
export interface JsonRpcRequest {
    method: string;
    params: [unknown];
    id: number;
}

export type HostMessage =
    | { type: 'response'; response: JsonRpcResponse }
    | { type: 'chunk'; chunk: Chunk }
    | { type: 'host'; event: HostEvent }
    | { type: 'invalid'; reason: string };

/**
 * The JSON-RPC request for a call. This is the one place params are wrapped:
 * callers everywhere else pass the args object itself.
 */
export function buildRequest(id: number, method: string, params: unknown): JsonRpcRequest {
    return { method, params: [params ?? {}], id };
}

/** Sorts one native message from the host into what the service worker does with it. */
export function classifyHostMessage(msg: unknown): HostMessage {
    if (!isObject(msg)) return { type: 'invalid', reason: 'not a JSON object' };

    if ('crs_chunk' in msg) {
        const c = msg.crs_chunk;
        if (
            isObject(c) &&
            Number.isInteger(c.stream) &&
            Number.isInteger(c.seq) &&
            Number.isInteger(c.total) &&
            typeof c.data === 'string'
        ) {
            return { type: 'chunk', chunk: c as unknown as Chunk };
        }
        return { type: 'invalid', reason: 'malformed crs_chunk' };
    }

    if ('crs_host' in msg) {
        const e = msg.crs_host;
        if (isObject(e) && e.event === 'ready') {
            return {
                type: 'host',
                event: {
                    event: 'ready',
                    server_path: str(e.server_path),
                    log_path: str(e.log_path),
                    version: str(e.version),
                },
            };
        }
        if (isObject(e) && (e.event === 'error' || e.event === 'server_exited')) {
            const logPath = str(e.log_path);
            return {
                type: 'host',
                event: {
                    event: e.event,
                    message: str(e.message) || defaultFailureMessage(e.event),
                    ...(logPath ? { log_path: logPath } : {}),
                },
            };
        }
        return { type: 'invalid', reason: 'unknown crs_host event' };
    }

    if (typeof msg.id === 'number' && 'result' in msg && 'error' in msg) {
        return { type: 'response', response: msg as unknown as JsonRpcResponse };
    }
    return { type: 'invalid', reason: 'neither a response, a chunk nor a host event' };
}

/** A chunk stream that broke the protocol; the stream is dropped. */
export class ChunkProtocolError extends Error {
    override name = 'ChunkProtocolError';
}

/**
 * Reassembles chunked responses. The host sends a stream's chunks back to
 * back, so at most one stream is open at a time: a chunk from another stream
 * before the open one finishes, a gap, a repeat or a change of `total` all
 * mean the protocol broke, and the open stream is dropped. A new stream
 * starting at seq 0 recovers.
 */
export class ChunkAssembler {
    private open: { stream: number; total: number; next: number; parts: string[] } | null = null;

    /** Feeds one chunk; returns the whole text once its stream completes. */
    push(chunk: Chunk): string | undefined {
        const { stream, seq, total, data } = chunk;
        if (total < 1 || seq < 0 || seq >= total) {
            this.open = null;
            throw new ChunkProtocolError(
                `chunk ${seq} of ${total} in stream ${stream} is out of range`
            );
        }

        if (this.open === null) {
            if (seq !== 0) {
                throw new ChunkProtocolError(
                    `stream ${stream} starts at chunk ${seq}, not 0 (its start was lost)`
                );
            }
            this.open = { stream, total, next: 0, parts: [] };
        } else if (this.open.stream !== stream) {
            const dropped = this.open.stream;
            this.open = null;
            throw new ChunkProtocolError(
                `stream ${stream} interleaved with unfinished stream ${dropped}`
            );
        } else if (seq !== this.open.next || total !== this.open.total) {
            const expected = this.open.next;
            this.open = null;
            throw new ChunkProtocolError(
                `stream ${stream} sent chunk ${seq}/${total}, expected ${expected}`
            );
        }

        this.open.parts.push(data);
        this.open.next++;
        if (this.open.next < this.open.total) return undefined;

        const text = this.open.parts.join('');
        this.open = null;
        return text;
    }

    /** Whether a stream is part-way through; it is lost if the port closes now. */
    get pending(): boolean {
        return this.open !== null;
    }

    reset(): void {
        this.open = null;
    }
}

// The exact texts Chrome sets chrome.runtime.lastError to when connectNative fails.
const HOST_NOT_FOUND = 'Specified native messaging host not found.';
const HOST_FORBIDDEN = 'Access to the specified native messaging host is forbidden.';

/**
 * Why the native port closed. A failure the host reported before exiting
 * says more than Chrome's generic "Native host has exited.", so it wins.
 */
export function disconnectError(
    lastErrorMessage: string | undefined,
    hostFailure?: HostFailure | null
): ErrorInfo {
    if (hostFailure) return hostFailureError(hostFailure);
    if (lastErrorMessage === HOST_NOT_FOUND) {
        return {
            kind: 'host-missing',
            message:
                'The native host com.c_hipple.crs is not installed. Run chrome_extension/crs_native_host/install.sh, then reload the extension.',
        };
    }
    if (lastErrorMessage === HOST_FORBIDDEN) {
        return {
            kind: 'host-forbidden',
            message:
                "The native host's manifest does not allow this extension. Re-run install.sh (with --extension-id if this extension's id differs).",
        };
    }
    return {
        kind: 'disconnected',
        message: lastErrorMessage
            ? `The native host disconnected: ${lastErrorMessage}`
            : 'The native host disconnected.',
    };
}

/** A host `error` / `server_exited` event as an error. */
export function hostFailureError(event: HostFailure): ErrorInfo {
    return {
        kind: 'server',
        message: event.message,
        ...(event.log_path ? { logPath: event.log_path } : {}),
    };
}

/** The text of a JSON-RPC `error` member: Go sends a string, others may send an object. */
export function rpcErrorMessage(error: unknown): string {
    if (typeof error === 'string') return error;
    if (isObject(error) && typeof error.message === 'string') return error.message;
    try {
        return JSON.stringify(error);
    } catch {
        return String(error);
    }
}

function defaultFailureMessage(event: 'error' | 'server_exited'): string {
    return event === 'error' ? 'The native host could not start the server.' : 'The server exited.';
}

function isObject(v: unknown): v is Record<string, unknown> {
    return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function str(v: unknown): string {
    return typeof v === 'string' ? v : '';
}
