/**
 * AI features
 *
 * The server registers AI features (docs/ai_features.md), switches them on in
 * config, and serves each one's result for a PR through ListAIFeatures,
 * RunAIFeature and GetAIOutput. A result follows the plugin response contract
 * (body + annotations) plus a typed report. Two features have a report this
 * client renders itself: comments-addressed, and change-diagram, whose report
 * is raw Mermaid source drawn with the mermaid library. Anything else
 * (feature-flags, say) renders its markdown body, and is counted on its
 * toolbar button by the `outstanding` list it serves.
 *
 * Applied features (file-ordering, review-ease) have no report: the server
 * applies their results to the diff's file order and the review list's ease
 * pill itself, so the client offers no button for them (see reportFeatures).
 *
 * The helpers here are the client's single interpretation of those replies.
 */

import type { StatusVariant } from './design';
import type { PluginBody, PRAnnotation } from './plugin_utils';

export const COMMENTS_ADDRESSED = 'comments-addressed';
export const CHANGE_DIAGRAM = 'change-diagram';

/** A registered feature, merged with how the server's config sets it up. */
export interface AIFeatureInfo {
    id: string;
    name: string;
    description: string;
    enabled: boolean;
    automatic: boolean;
    mode: string;
    modes: string[];
    provider: string;
    /**
     * The server applies the feature's result to what it already serves —
     * the diff's file order, the review list's ease rating — so there is no
     * report to open. Absent from servers that predate it.
     */
    applied?: boolean;
}

/**
 * The features to offer a report for: every one the server's config enables,
 * less the applied ones, whose results show up in the diff and the review
 * list instead.
 */
export function reportFeatures(features: AIFeatureInfo[]): AIFeatureInfo[] {
    return features.filter(f => f.enabled && !f.applied);
}

/**
 * Status of a feature's result for one PR. `pending` means a run is in
 * flight (the output may still carry the previous result); `not-run` means
 * there has never been one.
 */
export type AIStatus = 'pending' | 'not-run' | 'success' | 'error' | 'insufficient-input';

/** One feature's result for a PR, as GetAIOutput serves it. */
export interface AIFeatureOutput {
    feature: string;
    name: string;
    status: AIStatus | string;
    body: PluginBody;
    annotations: PRAnnotation[];
    /** The feature's typed report; null when it has none. */
    report: unknown;
    /** What still needs attention, for features that track that; null otherwise. */
    outstanding: unknown;
    covers_sha: string;
    covers_digest: string;
    current_sha: string;
    current_digest: string;
    /** The result was computed from inputs (code or discussion) that have since changed. */
    stale: boolean;
    /** Some input was cut to fit what the model was sent. */
    truncated: boolean;
    updated_at: string;
}

export interface RunAIFeatureReply {
    okay: boolean;
    outcome: 'started' | 'up-to-date' | 'already-running' | 'disabled' | 'unknown-feature' | string;
    message: string;
    output: AIFeatureOutput | null;
}

// --- comments-addressed ---------------------------------------------------

export type ItemStatus = 'addressed' | 'outstanding' | 'unclear';
export type ItemSource = 'github' | 'model';

/** The verdict on one review thread or conversation comment. */
export interface ReportItem {
    /** The comment that opened the thread (or the conversation comment); matches Comment.id. */
    root_comment_id: string;
    /** GitHub's thread node ID; null for conversation comments and threads with no known state. */
    thread_id: string | null;
    kind: 'thread' | 'conversation';
    status: ItemStatus;
    /** Who decided the status: GitHub's thread state, or the model's judgment. */
    source: ItemSource;
    rationale: string;
    /** What the model said about an item it wasn't allowed to decide. */
    model_note?: string;
    author: string;
    excerpt: string;
    path?: string;
    line?: number;
    outdated: boolean;
    resolved: boolean;
    resolved_by?: string;
    replies: number;
    last_author: string;
    last_activity: string;
    html_url?: string;
    /**
     * The code a thread was left on: the last lines of GitHub's diff hunk,
     * ending at the commented line. Only on threads that aren't addressed.
     */
    code_context?: string;
}

export interface ChangeRequest {
    reviewer: string;
    review_id: number;
    submitted_at: string;
    excerpt: string;
    html_url?: string;
}

export type Verdict =
    | 'all-addressed'
    | 'outstanding'
    | 'unclear'
    | 'no-comments'
    | 'insufficient-input';

export interface CommentsReport {
    verdict: Verdict;
    summary: string;
    counts: {
        total: number;
        addressed: number;
        outstanding: number;
        unclear: number;
        by_model: number;
    };
    items: ReportItem[];
    change_requests: ChangeRequest[];
    model: {
        consulted: boolean;
        provider?: string;
        model?: string;
        mode: string;
        asked: number;
        turns?: number;
        tool_calls?: number;
        note?: string;
    };
    truncated: boolean;
    missing: string[];
}

/**
 * The comments-addressed report in an output, or null when the output isn't
 * one (another feature, no result yet, or a shape this client doesn't know).
 */
export function commentsReport(output: AIFeatureOutput | null | undefined): CommentsReport | null {
    if (!output || output.feature !== COMMENTS_ADDRESSED) return null;
    const report = output.report as Partial<CommentsReport> | null;
    if (!report || typeof report.verdict !== 'string' || !Array.isArray(report.items)) {
        return null;
    }
    return {
        ...report,
        change_requests: report.change_requests ?? [],
        missing: report.missing ?? [],
    } as CommentsReport;
}

/**
 * What needs attention in a comments-addressed output: outstanding items
 * first, then unclear ones. Taken from the served `outstanding` list, which
 * holds every such item whether or not it can be anchored to a diff line.
 */
export function attentionItems(output: AIFeatureOutput | null | undefined): ReportItem[] {
    if (Array.isArray(output?.outstanding)) return output.outstanding as ReportItem[];
    const report = commentsReport(output);
    if (!report) return [];
    return [
        ...report.items.filter(i => i.status === 'outstanding'),
        ...report.items.filter(i => i.status === 'unclear'),
    ];
}

/**
 * The count for a feature's toolbar button: how many items need attention.
 * comments-addressed counts its outstanding and unclear items; any other
 * feature that serves an `outstanding` list (feature-flags: the changes that
 * run without a flag, then the unclear ones) is counted by that list once a
 * run has succeeded. Null when there is nothing to count yet — no result, a
 * run that failed or lacked input, or a feature that keeps no such list.
 */
export function attentionCount(output: AIFeatureOutput | null | undefined): number | null {
    if (commentsReport(output)) return attentionItems(output).length;
    if (output?.status === 'success' && Array.isArray(output.outstanding)) {
        return output.outstanding.length;
    }
    return null;
}

// --- change-diagram -------------------------------------------------------

/** The change-diagram report: the diagram's raw Mermaid source. */
export interface ChangeDiagramReport {
    mermaid: string;
    /** The keyword the diagram declares itself with, e.g. `flowchart`. */
    diagram_type: string;
}

/**
 * The change-diagram report in an output, or null when the output isn't one
 * (another feature, no result yet, or a run that failed).
 */
export function changeDiagram(
    output: AIFeatureOutput | null | undefined
): ChangeDiagramReport | null {
    if (!output || output.feature !== CHANGE_DIAGRAM) return null;
    const report = output.report as Partial<ChangeDiagramReport> | null;
    if (!report || typeof report.mermaid !== 'string' || report.mermaid.trim() === '') {
        return null;
    }
    return { mermaid: report.mermaid, diagram_type: report.diagram_type ?? '' };
}

export function isPending(output: AIFeatureOutput | null | undefined): boolean {
    return output?.status === 'pending';
}

/**
 * Whether opening a feature should start a run: it has never run for this
 * PR, or its result no longer describes it. The server answers a run for
 * unchanged inputs from its cache, so this never costs a needless model call.
 */
export function shouldRunOnOpen(output: AIFeatureOutput | null | undefined): boolean {
    if (!output || isPending(output)) return false;
    return output.status === 'not-run' || output.stale;
}

/** How long to wait before the next poll of a pending run. */
export function pollDelayMs(attempt: number): number {
    return attempt < 10 ? 1500 : 3000;
}

/** Give up polling a run after this long; the server's own timeout is five minutes. */
export const MAX_POLL_MS = 6 * 60 * 1000;

export function statusVariant(status: string): StatusVariant {
    switch (status) {
        case 'success':
            return 'success';
        case 'pending':
            return 'info';
        case 'error':
            return 'danger';
        case 'insufficient-input':
            return 'warning';
        default:
            return 'neutral';
    }
}

export function statusLabel(status: string): string {
    switch (status) {
        case 'not-run':
            return 'Not run';
        case 'insufficient-input':
            return 'Insufficient input';
        case 'pending':
            return 'Running';
        default:
            return status;
    }
}

export function verdictVariant(verdict: Verdict): StatusVariant {
    switch (verdict) {
        case 'all-addressed':
            return 'success';
        case 'outstanding':
            return 'danger';
        case 'unclear':
        case 'insufficient-input':
            return 'warning';
        default:
            return 'neutral';
    }
}

export function verdictLabel(verdict: Verdict): string {
    switch (verdict) {
        case 'all-addressed':
            return 'All addressed';
        case 'outstanding':
            return 'Outstanding';
        case 'unclear':
            return 'Unclear';
        case 'no-comments':
            return 'No comments';
        case 'insufficient-input':
            return 'Insufficient input';
        default:
            return verdict;
    }
}

export function itemVariant(status: ItemStatus): StatusVariant {
    switch (status) {
        case 'addressed':
            return 'success';
        case 'outstanding':
            return 'danger';
        default:
            return 'warning';
    }
}

export function sourceLabel(source: ItemSource): string {
    return source === 'model' ? 'model' : 'GitHub';
}

/** Where an item lives: "src/main.ts:4", "src/old.ts (outdated)", or "conversation". */
export function itemLocation(item: ReportItem): string {
    if (item.kind !== 'thread' || !item.path) return 'conversation';
    const where = item.line ? `${item.path}:${item.line}` : item.path;
    return item.outdated ? `${where} (outdated)` : where;
}

/** Whether the review view can take the reviewer to an item's thread. */
export function canJumpTo(item: ReportItem): boolean {
    return item.kind === 'thread' && !!item.path;
}
