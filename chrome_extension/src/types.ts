// Wire shapes: the server's JSON-RPC replies (docs/protocol.md), and the
// messages extension pages exchange with the service worker. Field names
// follow the server's JSON tags exactly — snake_case for replies, Go's own
// capitalised names where a struct has no JSON tags (PluginConfig).

import type { ErrorInfo, HostReady } from './native_protocol';

// --- Server replies -------------------------------------------------------

/** A team asked to review a PR (TeamReviewStatus). */
export interface TeamReviewStatus {
    name: string;
    slug: string;
    org: string;
    status: 'pending' | 'approved' | 'changes_requested' | 'reviewed' | string;
    mine: boolean;
    personal: boolean;
}

export type ReviewEase = 'easy' | 'medium' | 'hard';

/** One PR in the review list (GetAllReviews `items`). */
export interface ReviewItem {
    section: string;
    /** Lower sorts first. */
    section_priority: number;
    /** `TODO`, `WAITING` or `DONE`. */
    status: string;
    /** Comma-joined tags: the head repo's name, plus `draft` / `merged`. */
    tags: string;
    title: string;
    owner: string;
    repo: string;
    number: number;
    author: string;
    url: string;
    release_status: string;
    /** Empty unless review-ease is enabled and has rated the PR. */
    review_ease: ReviewEase | '';
    /** RFC 3339. */
    created_at: string;
    required_teams: TeamReviewStatus[] | null;
    comment_count: number;
    merge_conflicts: boolean;
}

export interface GetAllReviewsReply {
    /** The list as org-mode text, for the Emacs client. */
    content: string;
    items: ReviewItem[] | null;
}

/** A PR's metadata as GetPR serves it (PRMetadata). */
export interface PRMetadata {
    number: number;
    title: string;
    author: string;
    base_ref: string;
    head_ref: string;
    /** `open`, `closed`, ... */
    state: string;
    milestone: string;
    labels: string[] | null;
    assignees: string[] | null;
    reviewers: string[] | null;
    requested_teams: string[] | null;
    approved_by: string[] | null;
    changes_requested_by: string[] | null;
    commented_by: string[] | null;
    draft: boolean;
    ci_status: string;
    ci_failures: string[] | null;
    body: string;
    url: string;
    repo_path: string;
    worktree_path: string;
    release_status: string;
    review_ease: ReviewEase | '';
    changed_files: number;
    additions: number;
    deletions: number;
}

/** An annotation on a diff line, as a plugin declares it (Annotation). */
export interface PluginAnnotation {
    filename: string;
    /** 1-based line on the new side of the diff. */
    line: number;
    /** Free-form: `info`, `warning`, `error`, ... */
    severity: string;
    content: string;
}

/** An annotation tagged with what produced it (PRAnnotation). */
export interface PRAnnotation extends PluginAnnotation {
    /** `plugin` or `ai`. */
    source?: 'plugin' | 'ai' | string;
    /** The plugin's name; empty when source is `ai`. */
    plugin?: string;
    /** The AI feature's id; absent when source is `plugin`. */
    feature?: string;
}

/**
 * The PR payload GetPR and SyncPR reply with. The extension reads the
 * metadata and annotations; the rest is typed loosely because it never
 * renders the diff or the discussion.
 */
export interface PRPayload {
    okay: boolean;
    metadata: PRMetadata | null;
    /** Plugin annotations only; AI annotations come from GetAIOutput. */
    annotations: PRAnnotation[];
    content: string;
    diff: string;
    feedback: string;
    comments: unknown[];
    outdated_comments: unknown[];
    reviews: unknown[];
    commits: unknown[];
    images: unknown[];
}

export interface SyncPRReply extends PRPayload {
    /** The sync pulled in a new head SHA or new comments. */
    updated: boolean;
}

/**
 * A configured plugin (config.Plugin). The struct has no JSON tags, so the
 * fields keep Go's names.
 */
export interface PluginConfig {
    Name: string;
    Command: string;
    IncludeDiff: boolean;
    IncludeHeaders: boolean;
    IncludeComments: boolean;
    IncludeBranch: boolean;
    /** Runs only when asked (RerunPlugins), never automatically; reports `deferred` until then. */
    OnlyOnDemand: boolean;
    Provider: string;
    Model: string;
}

export interface PluginBody {
    body_type: 'markdown' | 'html' | string;
    body_content: string;
}

/** `deferred` means an on-demand plugin nobody has asked for yet. */
export type PluginStatus = 'pending' | 'success' | 'error' | 'deferred';

/** One plugin's stored result for a PR. A plugin with no entry has not run. */
export interface PluginOutput {
    /** The plugin's raw stdout. */
    result: string;
    status: PluginStatus | string;
    body: PluginBody;
    annotations: PluginAnnotation[] | null;
}

export interface RerunPluginsReply {
    okay: boolean;
    message: string;
    /** Results as they stood at dispatch; poll GetPluginOutput for the real thing. */
    output: Record<string, PluginOutput> | null;
}

/** A registered AI feature and how the server's config sets it up (ai.Info). */
export interface AIFeature {
    id: string;
    name: string;
    description: string;
    enabled: boolean;
    automatic: boolean;
    /** `oneshot` or `agent`. */
    mode: string;
    modes: string[] | null;
    /** `gemini`, `openrouter` or `command`. */
    provider: string;
    /**
     * The server applies the result to what it serves (file order, review
     * ease) instead of offering a report to open.
     */
    applied: boolean;
}

export type AIStatus = 'pending' | 'not-run' | 'success' | 'error' | 'insufficient-input';

/** One AI feature's result for a PR (AIFeatureOutput). */
export interface AIFeatureOutput {
    feature: string;
    name: string;
    /** `pending` may still carry the previous result's body and report. */
    status: AIStatus | string;
    body: PluginBody;
    annotations: PRAnnotation[] | null;
    /** The feature's typed report (see the *Report types below); null when none. */
    report: unknown;
    /** What still needs attention, for features that track it; null otherwise. */
    outstanding: unknown;
    covers_sha: string;
    covers_digest: string;
    current_sha: string;
    current_digest: string;
    /** The stored result no longer matches the PR's head SHA or discussion. */
    stale: boolean;
    /** Some input was cut to fit the model's prompt. */
    truncated: boolean;
    /** RFC 3339; empty when there is no stored result. */
    updated_at: string;
}

export type RunAIFeatureOutcome =
    'started' | 'up-to-date' | 'already-running' | 'disabled' | 'unknown-feature';

export interface RunAIFeatureReply {
    /** False when the feature can't run: unknown or not enabled. */
    okay: boolean;
    outcome: RunAIFeatureOutcome | string;
    message: string;
    /** `pending` when a run started; null for an unknown feature. */
    output: AIFeatureOutput | null;
}

// Feature ids with a report shape the extension knows.
export const CHANGE_DIAGRAM = 'change-diagram';
export const FILE_ORDERING = 'file-ordering';
export const REVIEW_EASE = 'review-ease';

/** change-diagram's report: raw Mermaid source the client renders itself. */
export interface ChangeDiagramReport {
    mermaid: string;
    /** The diagram's declared type, e.g. `flowchart`. */
    diagram_type: string;
}

/** file-ordering's report: the suggested reading order, one path per file. */
export interface FileOrderingReport {
    files: string[];
}

/** review-ease's report. */
export interface ReviewEaseReport {
    rating: ReviewEase;
}

// --- Extension messages ---------------------------------------------------

/**
 * The server methods the service worker forwards. Anything else is refused,
 * so a bug in the panel can't reach mutations like SubmitReview or MergePR;
 * add a method here (and a function in rpc.ts) to use it.
 */
export const RPC_METHODS = [
    'RPCHandler.Hello',
    'RPCHandler.GetAllReviews',
    'RPCHandler.GetPR',
    'RPCHandler.SyncPR',
    'RPCHandler.ListPlugins',
    'RPCHandler.GetPluginOutput',
    'RPCHandler.RerunPlugins',
    'RPCHandler.ListAIFeatures',
    'RPCHandler.RunAIFeature',
    'RPCHandler.GetAIOutput',
] as const;

export type RpcMethod = (typeof RPC_METHODS)[number];

export function isRpcMethod(method: unknown): method is RpcMethod {
    return (RPC_METHODS as readonly unknown[]).includes(method);
}

/** Page → service worker: call a server method. `params` is the args object, unwrapped. */
export interface RpcRequestMessage {
    type: 'crs-rpc';
    method: RpcMethod;
    params: Record<string, unknown>;
}

/** Page → service worker: how the native connection stands. */
export interface StatusRequestMessage {
    type: 'crs-status';
}

export type ExtensionRequest = RpcRequestMessage | StatusRequestMessage;

export type RpcReply = { ok: true; result: unknown } | { ok: false; error: ErrorInfo };

/** The service worker's answer to `crs-status`. */
export interface HostStatus {
    /** A native port is open and the host has reported the server started. */
    connected: boolean;
    /** What the host reported on its last successful start. */
    host?: Omit<HostReady, 'event'>;
    /** The last connection-level failure; cleared when the host next reports ready. */
    lastError?: ErrorInfo;
}

/** Service worker → content script: the toolbar button (or Alt+Shift+R) was pressed. */
export interface ToggleMessage {
    type: 'crs-toggle';
}

/** Panel (in the modal's iframe) → content script, by window.postMessage. */
export interface PanelToPageMessage {
    source: 'crs-panel';
    type: 'close';
}
