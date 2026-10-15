// The file order the page applies: reading the report, and ranking paths
// against it. The moving of elements needs a page, and is covered by
// e2e/pr_order.e2e.ts.

import { describe, expect, test } from 'bun:test';
import { byRank, directoryRank, fileOrderingFiles, pathRanks, rankOf } from './file_order';
import { FILE_ORDERING, type AIFeatureOutput } from './types';

function output(fields: Partial<AIFeatureOutput> = {}): AIFeatureOutput {
    return {
        feature: FILE_ORDERING,
        name: 'File ordering',
        status: 'success',
        body: { body_type: 'markdown', body_content: '' },
        annotations: null,
        report: null,
        outstanding: null,
        covers_sha: '',
        covers_digest: '',
        current_sha: '',
        current_digest: '',
        stale: false,
        truncated: false,
        updated_at: '',
        ...fields,
    };
}

const ORDER = ['internal/api/router.go', 'docs/api.md'];

describe('reading file-ordering’s report', () => {
    test('the paths of a successful run, in the stored order', () => {
        expect(fileOrderingFiles(output({ report: { files: ORDER } }))).toEqual(ORDER);
    });

    test('nothing to read leaves GitHub’s order alone', () => {
        expect(fileOrderingFiles(undefined)).toBeNull();
        expect(fileOrderingFiles(null)).toBeNull();
        // Another feature's output, a report of another shape, no paths at all.
        expect(
            fileOrderingFiles(output({ feature: 'change-diagram', report: { files: ORDER } }))
        ).toBeNull();
        expect(fileOrderingFiles(output({ report: { mermaid: 'flowchart LR' } }))).toBeNull();
        expect(
            fileOrderingFiles(output({ report: { files: 'internal/api/router.go' } }))
        ).toBeNull();
        expect(fileOrderingFiles(output({ report: { files: [] } }))).toBeNull();
    });

    test('entries that are not paths are dropped, and the rest kept', () => {
        expect(
            fileOrderingFiles(output({ report: { files: ['a.go', '', 7, null, 'b.go'] } }))
        ).toEqual(['a.go', 'b.go']);
    });
});

describe('ranking paths', () => {
    test('a named path ranks where the report puts it', () => {
        const ranks = pathRanks(ORDER);
        expect(rankOf(ranks, 'internal/api/router.go')).toBe(0);
        expect(rankOf(ranks, 'docs/api.md')).toBe(1);
    });

    test('a path the report doesn’t name ranks after every one it does', () => {
        const ranks = pathRanks(ORDER);
        expect(rankOf(ranks, 'README.md')).toBe(Number.POSITIVE_INFINITY);
        expect(rankOf(ranks, null)).toBe(Number.POSITIVE_INFINITY);
    });

    test('paths sort by rank, unnamed ones keeping their order among themselves', () => {
        const ranks = pathRanks(ORDER);
        const files = ['README.md', 'docs/api.md', 'zz.go', 'internal/api/router.go'];
        expect(byRank(files, path => rankOf(ranks, path))).toEqual([
            'internal/api/router.go',
            'docs/api.md',
            'README.md',
            'zz.go',
        ]);
    });

    test('an empty report ranks everything the same, so nothing moves', () => {
        const ranks = pathRanks([]);
        const files = ['b.go', 'a.go'];
        expect(byRank(files, path => rankOf(ranks, path))).toEqual(files);
    });
});

describe('ranking a directory in the file tree', () => {
    test('a directory sits where the latest file it holds sits', () => {
        // The shape that prompted this: journal#8's report is
        // [frontend/src/App.js, README.md, frontend/src/App.test.js], so the
        // frontend/src folder holds the last file and reads after README.md.
        expect(directoryRank([0, 2])).toBe(2);
        expect(directoryRank([2, 0])).toBe(2);
        expect(directoryRank([0])).toBe(0);
        expect(directoryRank([1, 3, 2])).toBe(3);
    });

    test('a directory holding no ranked file ranks after everything', () => {
        expect(directoryRank([])).toBe(Number.POSITIVE_INFINITY);
    });
});
