import { describe, expect, test } from 'bun:test';
import {
    filteredDiffSummary,
    showsFilteredDiff,
    type FilteredDiffResult,
} from './diff_filter_utils';

const result = (over: Partial<FilteredDiffResult>): FilteredDiffResult => ({
    filter: 'since-last-review',
    status: 'ready',
    head_sha: 'ffffffffff',
    base_sha: 'aaaaaaaaaa',
    baseline: {
        sha: 'aaaaaaaaaa',
        at: '2026-10-01T12:00:00Z',
        kind: 'review',
        detail: 'your approving review',
        inferred: false,
    },
    diff: 'diff --git a/x b/x\n',
    commits: ['bbbbbbbbbb', 'ffffffffff'],
    message: '',
    computed_at: '2026-10-01T12:05:00Z',
    ...over,
});

describe('showsFilteredDiff', () => {
    test('only a ready result replaces the full diff', () => {
        expect(showsFilteredDiff(result({}))).toBe(true);
        // Nothing changed since: an empty diff is still the answer to show.
        expect(showsFilteredDiff(result({ diff: '' }))).toBe(true);
        for (const status of ['no-baseline', 'rebased', 'error'] as const) {
            expect(showsFilteredDiff(result({ status, diff: '' }))).toBe(false);
        }
        expect(showsFilteredDiff(null)).toBe(false);
    });
});

describe('filteredDiffSummary', () => {
    test('names the range and the commits in it', () => {
        const { text, variant } = filteredDiffSummary(result({}), 'Since my last review');
        expect(variant).toBe('info');
        expect(text).toContain('changes since your approving review');
        expect(text).toContain('aaaaaaa → fffffff');
        expect(text).toContain('2 new commits');
        expect(text).toContain('full diff to comment');
    });

    test('says when the baseline was estimated', () => {
        const res = result({
            commits: ['ffffffffff'],
            baseline: {
                sha: 'aaaaaaaaaa',
                at: '',
                kind: 'comment',
                detail: 'your comment',
                inferred: true,
            },
        });
        const { text } = filteredDiffSummary(res, 'Since my last interaction');
        expect(text).toContain('your comment (estimated from its time)');
        expect(text).toContain('1 new commit.');
    });

    test('says when nothing changed', () => {
        const { text, variant } = filteredDiffSummary(
            result({ diff: '', commits: [] }),
            'Since my last review'
        );
        expect(variant).toBe('info');
        expect(text).toBe(
            'Since my last review: nothing has changed since your approving review (aaaaaaa).'
        );
    });

    test('explains why the full diff stays on screen', () => {
        expect(
            filteredDiffSummary(
                result({ status: 'no-baseline', message: "You haven't reviewed this PR yet." }),
                'Since my last review'
            )
        ).toEqual({
            text: "You haven't reviewed this PR yet. Showing the full diff.",
            variant: 'warning',
        });
        expect(
            filteredDiffSummary(result({ status: 'rebased', message: 'It was force-pushed.' }), 'x')
                .variant
        ).toBe('warning');
        const failed = filteredDiffSummary(
            result({ status: 'error', message: 'GitHub is down.' }),
            'Since my last review'
        );
        expect(failed.variant).toBe('danger');
        expect(failed.text).toContain('GitHub is down.');
    });
});
