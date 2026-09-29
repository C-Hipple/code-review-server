import type { AIFeatureInfo, AIFeatureOutput, RunAIFeatureReply } from './ai_utils';
import type { ConfigReply, WorkflowEntry } from './config_utils';

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
 * disk; sending `Workflows` replaces the whole list.
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
