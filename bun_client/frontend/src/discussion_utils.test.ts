import { expect, test, describe } from 'bun:test';
import {
    buildDiscussionTimeline,
    buildReplyTree,
    flattenReplyTree,
    groupIntoThreads,
    relativeTime,
    summarizeDiscussion,
    summarizeThread,
} from './discussion_utils';
import type { Comment, ReviewData } from './components/review/types';

const comment = (over: Partial<Comment> & { id: string }): Comment => ({
    author: 'alice',
    body: 'body',
    path: 'src/a.ts',
    position: '5',
    in_reply_to: 0,
    created_at: '2026-01-01T00:00:00Z',
    outdated: false,
    diff_hunk: '',
    ...over,
});

const review = (over: Partial<ReviewData> & { id: number }): ReviewData => ({
    user: 'alice',
    body: '',
    state: 'COMMENTED',
    submitted_at: '2026-01-01T00:00:00Z',
    html_url: '',
    ...over,
});

describe('groupIntoThreads', () => {
    test('keeps a reply to a reply in its root thread', () => {
        const comments = [
            comment({ id: '1' }),
            comment({ id: '2', in_reply_to: 1 }),
            // Replying to a thread targets its last comment, so nesting is real.
            comment({ id: '3', in_reply_to: 2 }),
        ];
        const threads = groupIntoThreads(comments);
        expect(threads.length).toBe(1);
        expect(threads[0].map(c => c.id)).toEqual(['1', '2', '3']);
    });

    test('separates independent roots on the same line', () => {
        const threads = groupIntoThreads([
            comment({ id: '1' }),
            comment({ id: '2' }),
            comment({ id: '3', in_reply_to: 1 }),
        ]);
        expect(threads.length).toBe(2);
        expect(threads[0].map(c => c.id)).toEqual(['1', '3']);
        expect(threads[1].map(c => c.id)).toEqual(['2']);
    });

    test('promotes an orphaned reply to its own thread', () => {
        const threads = groupIntoThreads([comment({ id: '9', in_reply_to: 404 })]);
        expect(threads.length).toBe(1);
        expect(threads[0][0].id).toBe('9');
    });

    test('puts the root first even when replies arrive before it', () => {
        const threads = groupIntoThreads([
            comment({ id: '2', in_reply_to: 1 }),
            comment({ id: '1' }),
        ]);
        expect(threads[0].map(c => c.id)).toEqual(['1', '2']);
    });

    test('does not hang on a reply cycle', () => {
        const threads = groupIntoThreads([
            comment({ id: '1', in_reply_to: 2 }),
            comment({ id: '2', in_reply_to: 1 }),
        ]);
        expect(threads.flat().length).toBe(2);
    });

    test('loses no comments', () => {
        const comments = [
            comment({ id: '1' }),
            comment({ id: '2', in_reply_to: 1 }),
            comment({ id: '3', in_reply_to: 2 }),
            comment({ id: '4' }),
            comment({ id: '5', in_reply_to: 99 }),
        ];
        expect(groupIntoThreads(comments).flat().length).toBe(comments.length);
    });
});

describe('buildReplyTree', () => {
    test('nests each reply under what it answered', () => {
        const thread = [
            comment({ id: '1' }),
            comment({ id: '2', in_reply_to: 1 }),
            comment({ id: '3', in_reply_to: 2 }),
            comment({ id: '4', in_reply_to: 1 }),
        ];
        const roots = buildReplyTree(thread);
        expect(roots.length).toBe(1);
        expect(roots[0].replies.map(n => n.comment.id)).toEqual(['2', '4']);
        expect(roots[0].replies[0].replies.map(n => n.comment.id)).toEqual(['3']);
    });

    test('records depth for indentation', () => {
        const flat = flattenReplyTree(
            buildReplyTree([
                comment({ id: '1' }),
                comment({ id: '2', in_reply_to: 1 }),
                comment({ id: '3', in_reply_to: 2 }),
            ])
        );
        expect(flat.map(n => [n.comment.id, n.depth])).toEqual([
            ['1', 0],
            ['2', 1],
            ['3', 2],
        ]);
    });

    test('hangs a reply with a missing parent off the top level', () => {
        const roots = buildReplyTree([comment({ id: '1' }), comment({ id: '2', in_reply_to: 77 })]);
        expect(roots.map(n => n.comment.id)).toEqual(['1', '2']);
    });

    test('renders in parent-then-replies order', () => {
        const flat = flattenReplyTree(
            buildReplyTree([
                comment({ id: '1' }),
                comment({ id: '2', in_reply_to: 1 }),
                comment({ id: '3', in_reply_to: 1 }),
                comment({ id: '4', in_reply_to: 2 }),
            ])
        );
        expect(flat.map(n => n.comment.id)).toEqual(['1', '2', '4', '3']);
    });

    test('handles an empty thread', () => {
        expect(buildReplyTree([])).toEqual([]);
    });
});

describe('summarizeThread', () => {
    test('treats resolution as a property of the whole thread', () => {
        const summary = summarizeThread([
            comment({ id: '1', resolved: true, resolved_by: 'bob' }),
            comment({ id: '2', in_reply_to: 1, author: 'carol' }),
        ]);
        expect(summary.resolved).toBe(true);
        expect(summary.resolvedBy).toBe('bob');
        expect(summary.participants).toEqual(['alice', 'carol']);
    });

    test('is outdated when any comment in it is', () => {
        const summary = summarizeThread([
            comment({ id: '1' }),
            comment({ id: '2', in_reply_to: 1, outdated: true }),
        ]);
        expect(summary.outdated).toBe(true);
        expect(summary.resolved).toBe(false);
    });
});

describe('buildDiscussionTimeline', () => {
    test('groups a review with the comments it was submitted with', () => {
        const reviews = [
            review({
                id: 900,
                user: 'alice',
                state: 'CHANGES_REQUESTED',
                body: 'A few things',
                submitted_at: '2026-01-02T10:00:00Z',
            }),
        ];
        const comments = [
            comment({ id: '1', review_id: 900, resolved: true, resolved_by: 'bob' }),
            comment({ id: '2', review_id: 900, in_reply_to: 1, author: 'bob', resolved: true }),
            comment({ id: '3', review_id: 900, path: 'src/b.ts' }),
        ];

        const timeline = buildDiscussionTimeline(reviews, comments);
        expect(timeline.length).toBe(1);
        expect(timeline[0].state).toBe('CHANGES_REQUESTED');
        expect(timeline[0].threads.length).toBe(2);
        expect(timeline[0].threads[0].comments.map(c => c.id)).toEqual(['1', '2']);
        expect(timeline[0].threads[0].resolved).toBe(true);
    });

    test('merges outdated comments into their review', () => {
        const reviews = [review({ id: 900, body: 'look here' })];
        const timeline = buildDiscussionTimeline(
            reviews,
            [comment({ id: '1', review_id: 900 })],
            [comment({ id: '2', review_id: 900, path: 'src/gone.ts', outdated: true })]
        );
        expect(timeline[0].threads.length).toBe(2);
        expect(timeline[0].threads.some(t => t.outdated)).toBe(true);
    });

    test('orders entries oldest first', () => {
        const timeline = buildDiscussionTimeline(
            [
                review({ id: 2, body: 'second', submitted_at: '2026-03-01T00:00:00Z' }),
                review({ id: 1, body: 'first', submitted_at: '2026-02-01T00:00:00Z' }),
            ],
            []
        );
        expect(timeline.map(e => e.body)).toEqual(['first', 'second']);
    });

    test('drops empty bookkeeping reviews but keeps ones with comments', () => {
        const timeline = buildDiscussionTimeline(
            [review({ id: 1 }), review({ id: 2 })],
            [comment({ id: '5', review_id: 2 })]
        );
        expect(timeline.map(e => e.key)).toEqual(['review-2']);
    });

    test('keeps threads whose review never came back from the API', () => {
        const timeline = buildDiscussionTimeline(
            [],
            [comment({ id: '1', review_id: 4242, author: 'dana' })]
        );
        expect(timeline.length).toBe(1);
        expect(timeline[0].author).toBe('dana');
        expect(timeline[0].threads.length).toBe(1);
    });

    test('shows a PR-level comment as a standalone entry', () => {
        const timeline = buildDiscussionTimeline(
            [],
            [comment({ id: '1', path: '', position: '', body: 'ping' })]
        );
        expect(timeline[0].kind).toBe('comment');
        expect(timeline[0].body).toBe('ping');
        expect(timeline[0].threads.length).toBe(0);
    });

    test('excludes unsubmitted local comments', () => {
        const timeline = buildDiscussionTimeline(
            [],
            [comment({ id: '1', author: 'local', body: 'draft' })]
        );
        expect(timeline).toEqual([]);
    });

    test('dates a review with no submitted_at from its earliest comment', () => {
        const timeline = buildDiscussionTimeline(
            [review({ id: 7, submitted_at: '0001-01-01T00:00:00Z', body: 'x' })],
            [comment({ id: '1', review_id: 7, created_at: '2026-05-05T00:00:00Z' })]
        );
        expect(timeline[0].timestamp).toBe('2026-05-05T00:00:00Z');
    });
});

describe('summarizeDiscussion', () => {
    test('counts resolved, unresolved and outdated threads', () => {
        const timeline = buildDiscussionTimeline(
            [review({ id: 900, body: 'changes', state: 'CHANGES_REQUESTED' })],
            [
                comment({ id: '1', review_id: 900, resolved: true }),
                comment({ id: '2', review_id: 900, path: 'b.ts' }),
            ],
            [comment({ id: '3', review_id: 900, path: 'c.ts', outdated: true, resolved: true })]
        );
        const summary = summarizeDiscussion(timeline);
        expect(summary.threads).toBe(3);
        expect(summary.resolvedThreads).toBe(2);
        expect(summary.unresolvedThreads).toBe(1);
        expect(summary.outdatedThreads).toBe(1);
    });

    // Resolved and outdated overlap, so they can never be summed. Only the
    // unresolved-outdated count describes threads that still want attention.
    test('separates outdated threads that are resolved from ones that are not', () => {
        const timeline = buildDiscussionTimeline(
            [review({ id: 900, body: 'changes', state: 'CHANGES_REQUESTED' })],
            [comment({ id: '1', review_id: 900, path: 'live.ts' })],
            [
                comment({
                    id: '2',
                    review_id: 900,
                    path: 'settled.ts',
                    outdated: true,
                    resolved: true,
                }),
                comment({ id: '3', review_id: 900, path: 'stale.ts', outdated: true }),
            ]
        );
        const summary = summarizeDiscussion(timeline);
        expect(summary.threads).toBe(3);
        expect(summary.outdatedThreads).toBe(2);
        // Only the unresolved one of the two outdated threads still needs a look.
        expect(summary.unresolvedOutdatedThreads).toBe(1);
        expect(summary.resolvedThreads).toBe(1);
        expect(summary.unresolvedThreads).toBe(2);
        // The partition holds even though the qualifier overlaps it.
        expect(summary.resolvedThreads + summary.unresolvedThreads).toBe(summary.threads);
    });

    test('reports a thread that is both resolved and outdated in both totals', () => {
        const timeline = buildDiscussionTimeline(
            [review({ id: 900, body: 'changes' })],
            [],
            [comment({ id: '1', review_id: 900, outdated: true, resolved: true })]
        );
        const summary = summarizeDiscussion(timeline);
        expect(summary.threads).toBe(1);
        expect(summary.resolvedThreads).toBe(1);
        expect(summary.outdatedThreads).toBe(1);
        expect(summary.unresolvedOutdatedThreads).toBe(0);
        expect(summary.unresolvedThreads).toBe(0);
    });
});

describe('relativeTime', () => {
    const now = new Date('2026-01-10T12:00:00Z');
    test('says yesterday for a day ago', () => {
        expect(relativeTime('2026-01-09T12:00:00Z', now)).toBe('yesterday');
    });
    test('counts days', () => {
        expect(relativeTime('2026-01-06T12:00:00Z', now)).toBe('4 days ago');
    });
    test('counts hours', () => {
        expect(relativeTime('2026-01-10T09:00:00Z', now)).toBe('3 hours ago');
    });
    test('returns empty for missing or zero timestamps', () => {
        expect(relativeTime('', now)).toBe('');
        expect(relativeTime('0001-01-01T00:00:00Z', now)).toBe('');
    });
});
