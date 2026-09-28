import { describe, expect, test } from 'bun:test';
import {
    attentionCount,
    attentionItems,
    canJumpTo,
    commentsReport,
    isPending,
    itemLocation,
    pollDelayMs,
    shouldRunOnOpen,
    statusLabel,
    statusVariant,
    verdictLabel,
    verdictVariant,
    type AIFeatureOutput,
    type CommentsReport,
    type ReportItem,
} from './ai_utils';

function item(
    fields: Partial<ReportItem> & Pick<ReportItem, 'root_comment_id' | 'status'>
): ReportItem {
    return {
        thread_id: null,
        kind: 'thread',
        source: 'github',
        rationale: '',
        author: 'bob',
        excerpt: 'Rename this.',
        path: 'src/main.ts',
        line: 4,
        outdated: false,
        resolved: false,
        replies: 0,
        last_author: 'bob',
        last_activity: '2026-09-01T10:00:00Z',
        ...fields,
    };
}

function report(items: ReportItem[]): CommentsReport {
    return {
        verdict: 'outstanding',
        summary: '1 outstanding',
        counts: { total: items.length, addressed: 0, outstanding: 1, unclear: 0, by_model: 0 },
        items,
        change_requests: [],
        model: { consulted: false, mode: 'oneshot', asked: 0 },
        truncated: false,
        missing: [],
    };
}

function output(fields: Partial<AIFeatureOutput> = {}): AIFeatureOutput {
    return {
        feature: 'comments-addressed',
        name: 'Comments addressed?',
        status: 'success',
        body: { body_type: 'markdown', body_content: '' },
        annotations: [],
        report: null,
        outstanding: null,
        covers_sha: 'sha-1',
        covers_digest: 'd1',
        current_sha: 'sha-1',
        current_digest: 'd1',
        stale: false,
        truncated: false,
        updated_at: '2026-09-01T10:00:00Z',
        ...fields,
    };
}

describe('commentsReport', () => {
    test('reads the typed report of a comments-addressed output', () => {
        const r = report([item({ root_comment_id: '5001', status: 'outstanding' })]);
        expect(commentsReport(output({ report: r }))?.verdict).toBe('outstanding');
    });

    test('fills lists an older server left out', () => {
        const { change_requests: _cr, missing: _m, ...partial } = report([]);
        const r = commentsReport(output({ report: partial }));
        expect(r?.change_requests).toEqual([]);
        expect(r?.missing).toEqual([]);
    });

    test('is null for other features, no result, or an unknown shape', () => {
        const r = report([]);
        expect(commentsReport(output({ feature: 'mermaid', report: r }))).toBeNull();
        expect(commentsReport(output({ report: null }))).toBeNull();
        expect(commentsReport(output({ report: { verdict: 'x' } }))).toBeNull();
        expect(commentsReport(undefined)).toBeNull();
    });
});

describe('attention items', () => {
    const open = item({ root_comment_id: '1', status: 'outstanding' });
    const unsure = item({ root_comment_id: '2', status: 'unclear' });
    const done = item({ root_comment_id: '3', status: 'addressed' });

    test('prefers the served outstanding list', () => {
        const o = output({ report: report([done, unsure, open]), outstanding: [open, unsure] });
        expect(attentionItems(o).map(i => i.root_comment_id)).toEqual(['1', '2']);
        expect(attentionCount(o)).toBe(2);
    });

    test('derives the list from the report when none was served, outstanding first', () => {
        const o = output({ report: report([unsure, done, open]) });
        expect(attentionItems(o).map(i => i.root_comment_id)).toEqual(['1', '2']);
    });

    test('has nothing to count before there is a report', () => {
        expect(attentionCount(output({ status: 'not-run' }))).toBeNull();
        expect(attentionCount(output({ report: report([done]), outstanding: [] }))).toBe(0);
    });
});

describe('shouldRunOnOpen', () => {
    test('runs a feature that never ran, or whose result is stale', () => {
        expect(shouldRunOnOpen(output({ status: 'not-run', updated_at: '' }))).toBe(true);
        expect(shouldRunOnOpen(output({ stale: true }))).toBe(true);
    });

    test('leaves a current result, and a run already in flight, alone', () => {
        expect(shouldRunOnOpen(output())).toBe(false);
        expect(shouldRunOnOpen(output({ status: 'pending', stale: true }))).toBe(false);
        expect(shouldRunOnOpen(undefined)).toBe(false);
    });

    test('isPending', () => {
        expect(isPending(output({ status: 'pending' }))).toBe(true);
        expect(isPending(output())).toBe(false);
        expect(isPending(null)).toBe(false);
    });
});

describe('presentation', () => {
    test('item locations', () => {
        expect(itemLocation(item({ root_comment_id: '1', status: 'unclear' }))).toBe(
            'src/main.ts:4'
        );
        expect(
            itemLocation(item({ root_comment_id: '1', status: 'unclear', line: 0, outdated: true }))
        ).toBe('src/main.ts (outdated)');
        expect(
            itemLocation(item({ root_comment_id: '1', status: 'unclear', kind: 'conversation' }))
        ).toBe('conversation');
    });

    test('only threads with a file can be jumped to', () => {
        expect(canJumpTo(item({ root_comment_id: '1', status: 'unclear' }))).toBe(true);
        expect(
            canJumpTo(item({ root_comment_id: '1', status: 'unclear', kind: 'conversation' }))
        ).toBe(false);
        expect(canJumpTo(item({ root_comment_id: '1', status: 'unclear', path: '' }))).toBe(false);
    });

    test('labels and variants', () => {
        expect(statusVariant('insufficient-input')).toBe('warning');
        expect(statusVariant('error')).toBe('danger');
        expect(statusLabel('not-run')).toBe('Not run');
        expect(verdictVariant('all-addressed')).toBe('success');
        expect(verdictVariant('outstanding')).toBe('danger');
        expect(verdictLabel('no-comments')).toBe('No comments');
    });

    test('polling backs off', () => {
        expect(pollDelayMs(0)).toBeLessThan(pollDelayMs(20));
    });
});
