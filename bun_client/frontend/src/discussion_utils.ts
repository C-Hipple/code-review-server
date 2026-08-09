import type { Comment, ReviewData } from './components/review/types';

// Pure helpers for turning the server's flat comment list into something you
// can actually follow: real reply trees, and a chronological PR-level timeline
// that groups each review with the comments it was submitted with.

/** A comment plus the replies made to it, recursively. */
export interface ThreadNode {
    comment: Comment;
    replies: ThreadNode[];
    /** How deep this node sits under the thread root (root itself is 0). */
    depth: number;
}

const parseId = (id: string): number => parseInt(id, 10);

/**
 * Group comments into threads, where a thread is a root comment plus every
 * comment that descends from it — directly or through another reply.
 *
 * The previous grouping only collected direct children of the root, so a reply
 * to a reply vanished from the UI entirely. Local comments genuinely nest that
 * way, because replying to a thread targets its *last* comment.
 *
 * A reply whose parent isn't in the input (deleted, filtered, or paginated
 * away) starts its own thread rather than disappearing.
 *
 * Threads come back in the order their roots appear in `comments`; within a
 * thread, comments stay in input order.
 */
export function groupIntoThreads(comments: Comment[]): Comment[][] {
    const byId = new Map<number, Comment>();
    for (const c of comments) byId.set(parseId(c.id), c);

    // Walk up parent links to the thread root. Guard against cycles and against
    // a comment that claims itself as its own parent.
    const rootIdFor = (c: Comment): number => {
        const seen = new Set<number>();
        let cur = c;
        while (cur.in_reply_to) {
            const id = parseId(cur.id);
            if (seen.has(id)) break;
            seen.add(id);
            const parent = byId.get(cur.in_reply_to);
            if (!parent) break;
            cur = parent;
        }
        return parseId(cur.id);
    };

    const threads = new Map<number, Comment[]>();
    const order: number[] = [];
    for (const c of comments) {
        const rootId = rootIdFor(c);
        let bucket = threads.get(rootId);
        if (!bucket) {
            bucket = [];
            threads.set(rootId, bucket);
            order.push(rootId);
        }
        bucket.push(c);
    }

    // Put each thread's root first; the rest keep their input (chronological)
    // order. A thread whose root was filtered out keeps its earliest comment
    // in front, which is already the case from input order.
    return order.map(rootId => {
        const bucket = threads.get(rootId)!;
        const rootIdx = bucket.findIndex(c => parseId(c.id) === rootId);
        if (rootIdx > 0) {
            const [root] = bucket.splice(rootIdx, 1);
            bucket.unshift(root);
        }
        return bucket;
    });
}

/**
 * Turn one thread (root first, as produced by `groupIntoThreads`) into a tree
 * of replies so the UI can indent each reply under what it actually answered.
 *
 * Replies whose parent is missing from the thread hang off the root, which is
 * what GitHub does when it flattens a thread.
 */
export function buildReplyTree(thread: Comment[]): ThreadNode[] {
    if (thread.length === 0) return [];

    const nodes = new Map<number, ThreadNode>();
    for (const c of thread) {
        nodes.set(parseId(c.id), { comment: c, replies: [], depth: 0 });
    }

    const roots: ThreadNode[] = [];
    for (const c of thread) {
        const node = nodes.get(parseId(c.id))!;
        const parent = c.in_reply_to ? nodes.get(c.in_reply_to) : undefined;
        if (parent && parent !== node) {
            parent.replies.push(node);
        } else {
            roots.push(node);
        }
    }

    // Assign depth after the shape is known. Iterative so a pathologically deep
    // thread can't blow the stack, and `seen` keeps a parent cycle from looping.
    const seen = new Set<ThreadNode>();
    const stack: ThreadNode[] = roots.map(n => {
        n.depth = 0;
        return n;
    });
    while (stack.length > 0) {
        const node = stack.pop()!;
        if (seen.has(node)) continue;
        seen.add(node);
        for (const reply of node.replies) {
            reply.depth = node.depth + 1;
            stack.push(reply);
        }
    }

    return roots;
}

/** Flatten a reply tree back into render order (parent, then its replies). */
export function flattenReplyTree(nodes: ThreadNode[]): ThreadNode[] {
    const out: ThreadNode[] = [];
    const visit = (list: ThreadNode[]) => {
        for (const n of list) {
            out.push(n);
            visit(n.replies);
        }
    };
    visit(nodes);
    return out;
}

/** Roll-up of a thread's state, for the summary line on a collapsed thread. */
export interface ThreadSummary {
    rootId: string;
    path: string;
    /** Every comment in the thread, root first. */
    comments: Comment[];
    /** True when GitHub reports the conversation as resolved. */
    resolved: boolean;
    /** Who resolved it, when known. */
    resolvedBy: string;
    /** True when the thread's lines are no longer in the current diff. */
    outdated: boolean;
    /** Participants in first-comment-first order, deduplicated. */
    participants: string[];
}

export function summarizeThread(thread: Comment[]): ThreadSummary {
    const root = thread[0];
    const resolvedComment = thread.find(c => c.resolved);
    const participants: string[] = [];
    for (const c of thread) {
        if (c.author && !participants.includes(c.author)) participants.push(c.author);
    }
    return {
        rootId: root.id,
        path: root.path,
        comments: thread,
        // Resolution is a property of the whole thread, so any comment carrying
        // the flag resolves all of them.
        resolved: Boolean(resolvedComment),
        resolvedBy: resolvedComment?.resolved_by || '',
        outdated: thread.some(c => c.outdated),
        participants,
    };
}

/**
 * One entry in the PR discussion timeline. Either a submitted review (with the
 * threads it opened), or a standalone comment that belongs to no review — a
 * plain PR comment, or an inline comment posted outside a review.
 */
export interface TimelineEntry {
    kind: 'review' | 'comment';
    /** Stable key for React, unique across entries. */
    key: string;
    author: string;
    /** ISO timestamp used for ordering; '' when the server had none. */
    timestamp: string;
    /** APPROVED | CHANGES_REQUESTED | COMMENTED — only for review entries. */
    state: string;
    /** The review's own summary text, or the standalone comment's body. */
    body: string;
    htmlUrl: string;
    /** Threads submitted as part of this review, oldest first. */
    threads: ThreadSummary[];
}

// GitHub records a pending review's submission time, but a review created by
// posting a single inline comment can come back without one. Falling back to
// the earliest comment in the review keeps such entries in the right place.
const earliestTimestamp = (values: string[]): string => {
    const usable = values.filter(v => v && !v.startsWith('0001'));
    if (usable.length === 0) return '';
    return usable.reduce((a, b) => (new Date(a).getTime() <= new Date(b).getTime() ? a : b));
};

const timeValue = (iso: string): number => {
    if (!iso) return Number.MAX_SAFE_INTEGER; // undated entries sort last
    const t = new Date(iso).getTime();
    return Number.isNaN(t) ? Number.MAX_SAFE_INTEGER : t;
};

/**
 * Build the chronological PR discussion: every review with the comment threads
 * it carried, interleaved with standalone comments.
 *
 * `comments` and `outdatedComments` are merged, because a review's threads
 * routinely span both — that's exactly the "these four comments, two of them
 * now outdated" case the panel exists to show.
 */
export function buildDiscussionTimeline(
    reviews: ReviewData[],
    comments: Comment[],
    outdatedComments: Comment[] = []
): TimelineEntry[] {
    const all = [...comments, ...outdatedComments];

    // Local (unsubmitted) comments belong to the diff, not to the record of
    // what has already been said on the PR.
    const published = all.filter(c => c.author !== 'local');

    const threads = groupIntoThreads(published).map(summarizeThread);

    // A thread belongs to the review that opened it, i.e. its root's review.
    const threadsByReview = new Map<number, ThreadSummary[]>();
    const looseThreads: ThreadSummary[] = [];
    for (const t of threads) {
        const reviewId = t.comments[0].review_id || 0;
        if (reviewId === 0) {
            looseThreads.push(t);
            continue;
        }
        const bucket = threadsByReview.get(reviewId);
        if (bucket) bucket.push(t);
        else threadsByReview.set(reviewId, [t]);
    }

    const entries: TimelineEntry[] = [];

    for (const r of reviews) {
        const reviewThreads = threadsByReview.get(r.id) || [];
        // A review with no body and no comments is GitHub bookkeeping (for
        // example the empty review row behind a single inline comment), and
        // shows up as noise in a timeline.
        if (!r.body?.trim() && reviewThreads.length === 0) continue;
        entries.push({
            kind: 'review',
            key: `review-${r.id}`,
            author: r.user,
            timestamp:
                r.submitted_at && !r.submitted_at.startsWith('0001')
                    ? r.submitted_at
                    : earliestTimestamp(reviewThreads.map(t => t.comments[0].created_at)),
            state: r.state || 'COMMENTED',
            body: r.body || '',
            htmlUrl: r.html_url || '',
            threads: reviewThreads,
        });
        threadsByReview.delete(r.id);
    }

    // Threads whose review never came back from the API still deserve a slot,
    // rather than being dropped from the discussion.
    for (const [reviewId, reviewThreads] of threadsByReview) {
        entries.push({
            kind: 'review',
            key: `review-${reviewId}`,
            author: reviewThreads[0].comments[0].author,
            timestamp: earliestTimestamp(reviewThreads.map(t => t.comments[0].created_at)),
            state: 'COMMENTED',
            body: '',
            htmlUrl: '',
            threads: reviewThreads,
        });
    }

    for (const t of looseThreads) {
        const root = t.comments[0];
        entries.push({
            // An inline thread posted outside a review still reads as a review
            // entry: it has file context and replies to show.
            kind: t.path ? 'review' : 'comment',
            key: `comment-${root.id}`,
            author: root.author,
            timestamp: root.created_at,
            state: 'COMMENTED',
            body: t.path ? '' : root.body,
            htmlUrl: root.html_url || '',
            threads: t.path ? [t] : [],
        });
    }

    entries.sort((a, b) => timeValue(a.timestamp) - timeValue(b.timestamp));
    return entries;
}

/**
 * Counts for the collapsed header, so it says something before you open it.
 *
 * Resolved and outdated are independent: a thread can be both, and often is
 * (you resolve a conversation, then the lines it pointed at get rewritten). So
 * resolved/unresolved is the partition — every thread is in exactly one — and
 * outdated is reported as a qualifier on each side rather than a third bucket.
 */
export interface DiscussionSummary {
    entries: number;
    threads: number;
    resolvedThreads: number;
    unresolvedThreads: number;
    /** Outdated threads that are still unresolved — the ones needing attention. */
    unresolvedOutdatedThreads: number;
    /** Every outdated thread, resolved or not. */
    outdatedThreads: number;
}

export function summarizeDiscussion(entries: TimelineEntry[]): DiscussionSummary {
    let threads = 0;
    let resolvedThreads = 0;
    let outdatedThreads = 0;
    let unresolvedOutdatedThreads = 0;
    for (const e of entries) {
        for (const t of e.threads) {
            threads++;
            if (t.resolved) resolvedThreads++;
            if (t.outdated) {
                outdatedThreads++;
                if (!t.resolved) unresolvedOutdatedThreads++;
            }
        }
    }
    return {
        entries: entries.length,
        threads,
        resolvedThreads,
        unresolvedThreads: threads - resolvedThreads,
        unresolvedOutdatedThreads,
        outdatedThreads,
    };
}

/** Human-readable relative age, e.g. "yesterday" or "3 days ago". */
export function relativeTime(iso: string, now: Date = new Date()): string {
    if (!iso || iso.startsWith('0001')) return '';
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return '';
    const seconds = Math.round((now.getTime() - then) / 1000);
    if (seconds < 60) return 'just now';
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return `${minutes} minute${minutes === 1 ? '' : 's'} ago`;
    const hours = Math.round(minutes / 60);
    if (hours < 24) return `${hours} hour${hours === 1 ? '' : 's'} ago`;
    const days = Math.round(hours / 24);
    if (days === 1) return 'yesterday';
    if (days < 30) return `${days} days ago`;
    const months = Math.round(days / 30);
    if (months < 12) return `${months} month${months === 1 ? '' : 's'} ago`;
    const years = Math.round(months / 12);
    return `${years} year${years === 1 ? '' : 's'} ago`;
}
