import { describe, expect, test } from 'bun:test';
import {
    commentsOnFilteredDiff,
    filteredDiffSummary,
    showsFilteredDiff,
    toFullPosition,
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
        expect(text).toContain('Comments on lines outside this range are hidden.');
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

describe('positions on a filtered diff', () => {
    // Row 3 of the filtered src/a.ts is the full diff's row 7; row 2 is a
    // removed line, which maps nowhere.
    const res = result({ positions: { 'src/a.ts': { '0': 0, '1': 5, '3': 7 } } });

    test('toFullPosition turns a row back into a full-diff position', () => {
        expect(toFullPosition(res, 'src/a.ts', 3)).toBe(7);
        expect(toFullPosition(res, 'src/a.ts', 0)).toBe(0);
        expect(toFullPosition(res, 'src/a.ts', 2)).toBeNull();
        expect(toFullPosition(res, 'src/b.ts', 1)).toBeNull();
        // An older server sends no positions at all.
        expect(toFullPosition(result({ positions: undefined }), 'src/a.ts', 3)).toBeNull();
        expect(toFullPosition(null, 'src/a.ts', 3)).toBeNull();
    });

    test('commentsOnFilteredDiff moves shown comments to their rows and drops the rest', () => {
        const comments = [
            { id: '1', path: 'src/a.ts', position: '7' },
            { id: '2', path: 'src/a.ts', position: '6' },
            { id: '3', path: 'src/a.ts', position: '' },
            { id: '4', path: 'src/b.ts', position: '7' },
            { id: '5', path: 'src/a.ts', position: '5' },
        ];
        expect(commentsOnFilteredDiff(comments, res)).toEqual([
            { id: '1', path: 'src/a.ts', position: '3' },
            { id: '3', path: 'src/a.ts', position: '' },
            { id: '5', path: 'src/a.ts', position: '1' },
        ]);
        // The caller's comments are left as they were.
        expect(comments[0].position).toBe('7');
        expect(commentsOnFilteredDiff(comments, result({ positions: null }))).toEqual([]);
    });
});
