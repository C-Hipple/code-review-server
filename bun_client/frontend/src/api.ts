import type { AIFeatureInfo, AIFeatureOutput, RunAIFeatureReply } from './ai_utils';
import type {
    AIFeatureEntry,
    AISettings,
    ConfigReply,
    PluginEntry,
    WorkflowEntry,
} from './config_utils';

export const API_BASE =
    typeof window !== 'undefined' &&
    (window.location.port === '5173' || window.location.port === '5174')
        ? `${window.location.protocol}//${window.location.hostname}:5172`
        : '';
const RPC_URL = `${API_BASE}/api/rpc`;

export interface RpcResponse<T> {
    result: T;
    error: any;
    id: number;
}

const SPECIALIZED_ENDPOINTS: Record<string, string> = {
    'RPCHandler.GetPR': '/api/get-pr',
    'RPCHandler.GetAdjacentPR': '/api/get-adjacent-pr',
    'RPCHandler.AddComment': '/api/add-comment',
    'RPCHandler.EditComment': '/api/edit-comment',
    'RPCHandler.DeleteComment': '/api/delete-comment',
    'RPCHandler.SyncPR': '/api/sync-pr',
    'RPCHandler.SubmitReview': '/api/submit-review',
    'RPCHandler.GetAllReviews': '/api/reviews',
    'RPCHandler.ListPlugins': '/api/list-plugins',
    'RPCHandler.GetPluginOutput': '/api/get-plugin-output',
    'RPCHandler.RerunPlugins': '/api/rerun-plugins',
    'RPCHandler.GetHunkContext': '/api/get-hunk-context',
    'RPCHandler.GetConfig': '/api/get-config',
    'RPCHandler.UpdateConfig': '/api/update-config',
    'RPCHandler.ListAIFeatures': '/api/list-ai-features',
    'RPCHandler.RunAIFeature': '/api/run-ai-feature',
    'RPCHandler.GetAIOutput': '/api/get-ai-output',
    'RPCHandler.StartGitHubLink': '/api/start-github-link',
    'RPCHandler.GetGitHubLink': '/api/get-github-link',
    'RPCHandler.UnlinkGitHub': '/api/unlink-github',
};

export interface GetHunkContextArgs {
    Owner: string;
    Repo: string;
    Number: number;
    Filename: string;
    Side: 'old' | 'new';
    AnchorLine: number;
    Direction: 'before' | 'after';
    Count: number;
    OrigStart: number;
    OrigLength: number;
    NewStart: number;
    NewLength: number;
    HunkHeader: string;
}

export interface GetHunkContextReply {
    lines: string[];
    start_line: number;
    end_line: number;
    range_header: string;
}

export async function getHunkContext(args: GetHunkContextArgs): Promise<GetHunkContextReply> {
    return rpcCall<GetHunkContextReply>('RPCHandler.GetHunkContext', [args]);
}

/**
 * Fetches the server's TOML configuration along with the workflow type and
 * filter registries the config editor builds its pickers from.
 */
export async function getConfig(): Promise<ConfigReply> {
    return rpcCall<ConfigReply>('RPCHandler.GetConfig', [{}]);
}

/**
 * Saves a partial configuration change. Fields left out keep whatever is on
 * disk; sending `Workflows`, `Plugins` or `AIFeatures` replaces the whole list,
 * and sending `AI` the whole `[AI]` table.
 *
 * A config the server rejects comes back as `okay: false` with `errors`
 * populated rather than as a thrown error — the file is left untouched.
 */
export async function updateConfig(args: UpdateConfigArgs): Promise<ConfigReply> {
    return rpcCall<ConfigReply>('RPCHandler.UpdateConfig', [args]);
}

export interface UpdateConfigArgs {
    Repos?: string[];
    SleepDuration?: number;
    JiraDomain?: string;
    GithubUsername?: string;
    RepoLocation?: string;
    AutoWorktree?: boolean;
    DesktopNotifications?: boolean;
    SectionPriority?: Record<string, number>;
    SectionSorting?: Record<string, string>;
    Workflows?: WorkflowEntry[];
    Plugins?: PluginEntry[];
    AI?: AISettings;
    AIFeatures?: AIFeatureEntry[];
}

export interface PRRef {
    Owner: string;
    Repo: string;
    Number: number;
}

/** Every registered AI feature, enabled or not, as the server's config sets it up. */
export async function listAIFeatures(): Promise<AIFeatureInfo[]> {
    const reply = await rpcCall<{ features: AIFeatureInfo[] | null }>('RPCHandler.ListAIFeatures', [
        {},
    ]);
    return reply.features ?? [];
}

/**
 * Asks for an AI feature to run for a PR. The run happens in the background:
 * poll getAIOutput while the output reads `pending`. Unless `Force` is set, a
 * stored result that still covers the PR's inputs answers without a run.
 */
export async function runAIFeature(
    args: PRRef & { Feature: string; Force?: boolean }
): Promise<RunAIFeatureReply> {
    return rpcCall<RunAIFeatureReply>('RPCHandler.RunAIFeature', [
        { ...args, Force: !!args.Force },
    ]);
}

/**
 * Stored AI feature results for a PR, keyed by feature ID: one feature when
 * `Feature` is set, otherwise every enabled one. Never starts a run.
 */
export async function getAIOutput(
    args: PRRef & { Feature?: string }
): Promise<Record<string, AIFeatureOutput>> {
    const reply = await rpcCall<{ output: Record<string, AIFeatureOutput> | null }>(
        'RPCHandler.GetAIOutput',
        [{ ...args, Feature: args.Feature ?? '' }]
    );
    return reply.output ?? {};
}

/**
 * Where a user's GitHub link stands on a shared server (docs/protocol.md):
 * `pending` while a code waits to be entered on GitHub; `expired`, `denied`,
 * `mismatch` and `error` for a link that ended without linking; `unavailable`
 * when the server (or this connection) cannot link accounts at all.
 */
export type GitHubLinkStatus =
    | 'unavailable'
    | 'unlinked'
    | 'pending'
    | 'linked'
    | 'expired'
    | 'denied'
    | 'mismatch'
    | 'error';

export interface StartGitHubLinkReply {
    /** True when a code is waiting for the user (`status` is `pending`). */
    okay: boolean;
    status: GitHubLinkStatus;
    /** What the user enters at `verification_uri`, e.g. `WDJB-MJHT`. */
    user_code: string;
    verification_uri: string;
    /** When the code expires, RFC 3339. */
    expires_at: string;
    /** Seconds between polls of GetGitHubLink that GitHub asks for. */
    interval: number;
    message: string;
}

export interface GetGitHubLinkReply {
    /** False only when `status` is `unavailable`. */
    okay: boolean;
    status: GitHubLinkStatus;
    /** The linked account's login whenever one is linked, also while a new link is pending. */
    github_login: string;
    user_code: string;
    verification_uri: string;
    expires_at: string;
    message: string;
}

/**
 * Starts linking the user's GitHub account through GitHub's device flow, or
 * returns the link already waiting. Poll getGitHubLink until it is no longer
 * pending.
 */
export async function startGitHubLink(): Promise<StartGitHubLinkReply> {
    return rpcCall<StartGitHubLinkReply>('RPCHandler.StartGitHubLink', [{}]);
}

/** Where the user's GitHub link stands. Only reads. */
export async function getGitHubLink(): Promise<GetGitHubLinkReply> {
    return rpcCall<GetGitHubLinkReply>('RPCHandler.GetGitHubLink', [{}]);
}

/** Removes the user's GitHub link; `message` says whether its token was revoked. */
export async function unlinkGitHub(): Promise<{ okay: boolean; message: string }> {
    return rpcCall<{ okay: boolean; message: string }>('RPCHandler.UnlinkGitHub', [{}]);
}

/**
 * What the bridge says about its connection (GET /api/session). Running its
 * own crs it is `{ remote: false }`; connected to a shared server, who it is
 * signed in as there.
 */
export interface BridgeSession {
    remote: boolean;
    /** The shared server's host:port. */
    address?: string;
    /** The login the bridge registered as; '' for the server's administrator token. */
    login?: string;
    is_admin?: boolean;
    /** Their own GitHub account is linked, so their reviews go out as them. */
    github_linked?: boolean;
    /** The server can link GitHub accounts. */
    github_link_available?: boolean;
    /** Why the bridge could not read the session just now. */
    error?: string;
}

export async function getBridgeSession(): Promise<BridgeSession> {
    const res = await fetch(`${API_BASE}/api/session`);
    if (!res.ok) throw new Error(`/api/session answered ${res.status}`);
    return (await res.json()) as BridgeSession;
}

export async function rpcCall<T>(method: string, params: any[]): Promise<T> {
    const id = Date.now();
    const specializedPath = SPECIALIZED_ENDPOINTS[method];

    const url = specializedPath ? `${API_BASE}${specializedPath}` : RPC_URL;
    const body = specializedPath
        ? method === 'RPCHandler.GetAllReviews' || method === 'RPCHandler.ListPlugins'
            ? {}
            : params[0]
        : { method, params, id };

    const response = await fetch(url, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
        },
        body: JSON.stringify(body),
    });

    const data: any = await response.json();

    // For specialized endpoints, the server returns { result: ... }
    // For generic RPC, it returns { result: { result: ... } } because bridge.call returns res.result
    // Wait, let's check server.ts implementation.
    // bridge.call returns res.result (the Go result field).
    // server.ts wraps it: return new Response(JSON.stringify({ result }), ...)
    // So both return { result: ... } where ... is the Go reply struct.

    if (data.error) {
        throw new Error(JSON.stringify(data.error));
    }
    return data.result;
}

/**
 * The human-readable part of an error rpcCall threw. The Go server's error
 * arrives as a JSON-encoded string (or an object carrying `message`), so
 * unwrap it rather than showing the quotes and braces.
 */
export function rpcErrorMessage(e: unknown): string {
    const raw = e instanceof Error ? e.message : String(e);
    try {
        const parsed = JSON.parse(raw);
        if (typeof parsed === 'string') return parsed;
        if (typeof parsed?.message === 'string') return parsed.message;
    } catch {
        // Not JSON — a network failure or the like, already readable.
    }
    return raw;
}

/** How a shared server (`crs --listen`) turned a call down. */
export interface RpcRefusal {
    /**
     * `permission-denied`: the method is for the server's administrators.
     * `link-github`: a write to GitHub — submitting a review, merging — needs
     * the user's own GitHub account, which is not linked (or no longer
     * works); linking it and trying again will do.
     * `refused`: the server won't act under its own GitHub account for a
     * user connected to it, and cannot link theirs. Final.
     */
    kind: 'permission-denied' | 'link-github' | 'refused';
    /** The server's explanation, after the prefix. */
    detail: string;
}

// Most specific first: a link-github refusal is also a refusal.
const REFUSAL_PREFIXES = [
    ['permission denied:', 'permission-denied'],
    ['refused: link-github:', 'link-github'],
    ['refused:', 'refused'],
] as const;

/**
 * Reads a shared server's refusal off an error rpcCall threw, by the prefix
 * the server puts on it (docs/protocol.md). Null for any other error —
 * including every error a server run locally gives, since it refuses nothing.
 */
export function rpcRefusal(e: unknown): RpcRefusal | null {
    const message = rpcErrorMessage(e);
    for (const [prefix, kind] of REFUSAL_PREFIXES) {
        if (message.startsWith(prefix)) {
            return { kind, detail: message.slice(prefix.length).trim() };
        }
    }
    return null;
}

/**
 * Whether the bridge lost its connection to a shared server before the server
 * answered the call (remote_rpc.ts, `connection lost:`). The call reached the
 * server, which may have acted on it: a write's outcome is unknown, not failed.
 */
export function rpcConnectionLost(e: unknown): boolean {
    return rpcErrorMessage(e).startsWith('connection lost:');
}

/**
 * Whether SubmitReview refused a review because pending comments it was shown
 * with are gone (`drafts changed:`): already sent by an earlier submit, or
 * deleted with the user. Nothing was submitted; the PR wants reloading.
 */
export function rpcDraftsChanged(e: unknown): boolean {
    return rpcErrorMessage(e).startsWith('drafts changed:');
}

/** What to say when a `permission-denied` refusal stops `action` ("rerun plugins"). */
export function adminOnlyMessage(action: string): string {
    return `Only an administrator of this server can ${action}.`;
}

/**
 * Read file contents from a repository
 */
export async function readFile(repoPath: string, filePath: string): Promise<string> {
    const res = await fetch(`${API_BASE}/api/read-file`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repoPath, filePath }),
    });
    const data = await res.json();
    if (data.error) throw new Error(data.error);
    return data.content;
}

/**
 * List all files in a repository
 */
export async function listFiles(repoPath: string): Promise<string[]> {
    const res = await fetch(`${API_BASE}/api/list-files`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ repoPath }),
    });
    const data = await res.json();
    if (data.error) throw new Error(data.error);
    return data.files;
}

/**
 * One completed workflow cycle: the GitHub API calls it made, broken down by
 * call type, and the rate limit budget left when it finished.
 *
 * `remaining` and `limit` are -1 when that cycle recorded no usable budget
 * reading — charts should leave a gap rather than plot it as an exhausted
 * budget. `gap_minutes` and `calls_per_minute` are 0 for the first point in a
 * window, which has no predecessor to measure the rate against.
 */
export interface RateLimitHistoryPoint {
    recorded_at: string;
    pr_list: number;
    pr_specific: number;
    comments: number;
    issue_comments: number;
    ci_status: number;
    diff: number;
    reviews: number;
    combined_status: number;
    check_runs: number;
    commits: number;
    review_threads: number;
    team_reviews: number;
    reactions: number;
    total: number;
    remaining: number;
    limit: number;
    reset_at: string;
    gap_minutes: number;
    calls_per_minute: number;
}

export interface RateLimitHistoryReply {
    /** The window the server actually used, after defaulting and clamping. */
    hours_back: number;
    since: string;
    points: RateLimitHistoryPoint[];
}

/**
 * Fetches the recorded GitHub API spend and rate limit budget per workflow
 * cycle, oldest first. The series resolution is the server's configured sleep
 * duration, since one row is written per completed cycle.
 *
 * Omitting `hoursBack` lets the server apply its own default (3 hours).
 */
export async function getRateLimitHistory(hoursBack?: number): Promise<RateLimitHistoryReply> {
    return rpcCall<RateLimitHistoryReply>('RPCHandler.GetRateLimitHistory', [
        { hours_back: hoursBack ?? 0 },
    ]);
}
