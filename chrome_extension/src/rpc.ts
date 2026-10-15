// The panel's typed client for the server. Each function sends one
// `{type: 'crs-rpc', method, params}` message to the service worker, which
// forwards it to the server through the native host.
//
// Params are passed here as the plain args object, with the server's
// capitalised field names; the service worker wraps them into the
// one-element array Go's net/rpc/jsonrpc expects (buildRequest in
// native_protocol.ts) — that is the only place they are wrapped.
//
// Every failure throws an RpcError whose `kind` says what went wrong
// (ErrorKind in native_protocol.ts): the panel shows install steps for
// `host-missing`, the host's message and log for `server`, and an inline
// Retry for the rest.
//
// Only extension pages can use this: the service worker ignores content scripts.

import type { PullRef } from './github_url';
import type { ErrorInfo, ErrorKind } from './native_protocol';
import type {
    AIFeature,
    AIFeatureOutput,
    ExtensionRequest,
    GetAllReviewsReply,
    HostStatus,
    PluginConfig,
    PluginOutput,
    PRPayload,
    RerunPluginsReply,
    ReviewItem,
    RpcMethod,
    RpcReply,
    RunAIFeatureReply,
    SyncPRReply,
} from './types';

export type { ErrorKind, PullRef };

/** A failed call; `kind` is what the UI branches on. */
export class RpcError extends Error {
    override name = 'RpcError';
    readonly kind: ErrorKind;
    /** The native host's log file, for `server` errors. */
    readonly logPath?: string;

    constructor(kind: ErrorKind, message: string, logPath?: string) {
        super(message);
        this.kind = kind;
        if (logPath) this.logPath = logPath;
    }

    static from(info: ErrorInfo): RpcError {
        return new RpcError(info.kind, info.message, info.logPath);
    }
}

/** Calls any allowed server method with its args object and returns the raw reply. */
export async function call<T>(method: RpcMethod, params: Record<string, unknown> = {}): Promise<T> {
    const reply = await send({ type: 'crs-rpc', method, params });
    if (!isRpcReply(reply)) {
        throw new RpcError(
            'extension',
            `The service worker sent an unexpected reply to ${method}.`
        );
    }
    if (!reply.ok) throw RpcError.from(reply.error);
    return reply.result as T;
}

/**
 * How the native connection stands. Doesn't connect: before the first call
 * this reads `{connected: false}` with no error.
 */
export async function getStatus(): Promise<HostStatus> {
    const reply = await send({ type: 'crs-status' });
    if (typeof reply !== 'object' || reply === null || !('connected' in reply)) {
        throw new RpcError('extension', 'The service worker sent an unexpected status reply.');
    }
    return reply as HostStatus;
}

/** The review list: every PR in every section, in the server's order. */
export async function getAllReviews(): Promise<ReviewItem[]> {
    const reply = await call<GetAllReviewsReply>('RPCHandler.GetAllReviews');
    return reply.items ?? [];
}

/**
 * A PR's full payload, from the server's caches when it has them. Also what
 * kicks off the PR's automatic plugins and applied AI features, as opening a
 * PR in any client does — call it before reading their output.
 */
export function getPR(pr: PullRef, options: { skipCache?: boolean } = {}): Promise<PRPayload> {
    return call<PRPayload>('RPCHandler.GetPR', {
        ...prArgs(pr),
        SkipCache: options.skipCache ?? false,
    });
}

/** Refetches the PR from GitHub; `updated` says whether anything changed. */
export function syncPR(pr: PullRef): Promise<SyncPRReply> {
    return call<SyncPRReply>('RPCHandler.SyncPR', prArgs(pr));
}

/** The plugins configured with `[[Plugins]]`. */
export async function listPlugins(): Promise<PluginConfig[]> {
    const reply = await call<{ plugins: PluginConfig[] | null }>('RPCHandler.ListPlugins');
    return reply.plugins ?? [];
}

/** Each plugin's stored result for the PR, by plugin name; a plugin with no entry hasn't run. */
export async function getPluginOutput(pr: PullRef): Promise<Record<string, PluginOutput>> {
    const reply = await call<{ output: Record<string, PluginOutput> | null }>(
        'RPCHandler.GetPluginOutput',
        prArgs(pr)
    );
    return reply.output ?? {};
}

/**
 * Reruns the named plugins, or all of them when `plugins` is omitted or
 * empty. Returns once the rerun is accepted; poll getPluginOutput for results.
 */
export function rerunPlugins(pr: PullRef, plugins?: string[]): Promise<RerunPluginsReply> {
    return call<RerunPluginsReply>('RPCHandler.RerunPlugins', {
        ...prArgs(pr),
        ...(plugins && plugins.length > 0 ? { Plugins: plugins } : {}),
    });
}

/** Every registered AI feature, enabled or not. */
export async function listAIFeatures(): Promise<AIFeature[]> {
    const reply = await call<{ features: AIFeature[] | null }>('RPCHandler.ListAIFeatures');
    return reply.features ?? [];
}

/**
 * Asks for a feature to run for the PR; returns at once. Without `force` a
 * stored result that still covers the PR's inputs answers without a run.
 * Poll getAIOutput while the output reads `pending`.
 */
export function runAIFeature(
    pr: PullRef,
    feature: string,
    options: { force?: boolean } = {}
): Promise<RunAIFeatureReply> {
    return call<RunAIFeatureReply>('RPCHandler.RunAIFeature', {
        ...prArgs(pr),
        Feature: feature,
        Force: options.force ?? false,
    });
}

/**
 * Stored AI results for the PR by feature id: every enabled feature, or just
 * `feature` (enabled or not). A pure read — never starts a run — so poll it.
 */
export async function getAIOutput(
    pr: PullRef,
    feature?: string
): Promise<Record<string, AIFeatureOutput>> {
    const reply = await call<{ output: Record<string, AIFeatureOutput> | null }>(
        'RPCHandler.GetAIOutput',
        { ...prArgs(pr), Feature: feature ?? '' }
    );
    return reply.output ?? {};
}

function prArgs(pr: PullRef): { Owner: string; Repo: string; Number: number } {
    return { Owner: pr.owner, Repo: pr.repo, Number: pr.number };
}

async function send(message: ExtensionRequest): Promise<unknown> {
    try {
        return await chrome.runtime.sendMessage(message);
    } catch (e) {
        throw new RpcError(
            'extension',
            `Could not reach the extension's service worker: ${e instanceof Error ? e.message : String(e)}`
        );
    }
}

function isRpcReply(reply: unknown): reply is RpcReply {
    if (typeof reply !== 'object' || reply === null || !('ok' in reply)) return false;
    // Messaging drops an undefined `result`, so `ok: true` alone is a valid reply.
    if (reply.ok === true) return true;
    return (
        reply.ok === false &&
        'error' in reply &&
        typeof reply.error === 'object' &&
        reply.error !== null &&
        'kind' in reply.error &&
        'message' in reply.error
    );
}
