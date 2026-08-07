import { expect, test, describe } from 'bun:test';
import {
    authorLogin,
    countByState,
    facetsByOpenCount,
    formatRelativeTime,
    groupBySection,
    itemMatchesQuery,
    parseGitHubPRUrl,
    parseTags,
    prState,
    uniqueValues,
} from './pr_list_utils';
import type { ReviewItem } from './pr_list_utils';

function item(overrides: Partial<ReviewItem> = {}): ReviewItem {
    return {
        section: 'Open PRs',
        status: 'TODO',
        tags: 'code-review-server',
        title: 'Add test files',
        owner: 'C-Hipple',
        repo: 'code-review-server',
        number: 83,
        author: 'C-Hipple',
        url: 'https://github.com/C-Hipple/code-review-server/pull/83',
        ...overrides,
    };
}

describe('parseTags', () => {
    test('splits and normalizes the comma-joined tag string', () => {
        expect(parseTags('code-review-server, Merged ')).toEqual(['code-review-server', 'merged']);
    });

    test('returns an empty list for missing tags', () => {
        expect(parseTags(undefined)).toEqual([]);
        expect(parseTags('')).toEqual([]);
    });
});

describe('prState', () => {
    test('TODO is open', () => {
        expect(prState(item({ status: 'TODO' }))).toBe('open');
    });

    test('WAITING is a draft', () => {
        expect(prState(item({ status: 'WAITING' }))).toBe('draft');
    });

    test('DONE with a merged tag is merged', () => {
        expect(prState(item({ status: 'DONE', tags: 'code-review-server,merged' }))).toBe('merged');
    });

    test('DONE without a merged tag is closed', () => {
        expect(prState(item({ status: 'DONE' }))).toBe('closed');
    });

    test('CANCELLED is closed', () => {
        expect(prState(item({ status: 'CANCELLED' }))).toBe('closed');
    });

    test('falls back to the tags for statuses the server does not emit', () => {
        expect(prState(item({ status: 'PROGRESS', tags: 'repo,draft' }))).toBe('draft');
        expect(prState(item({ status: 'PROGRESS', tags: 'repo,merged' }))).toBe('merged');
    });

    test('treats an unknown status as open rather than dropping the item', () => {
        expect(prState(item({ status: 'BLOCKED' }))).toBe('open');
        expect(prState(item({ status: '' }))).toBe('open');
    });

    test('does not confuse a repo named like a tag with the tag itself', () => {
        expect(prState(item({ status: 'TODO', tags: 'mergedeep' }))).toBe('open');
    });
});

describe('countByState', () => {
    test('buckets every item', () => {
        const counts = countByState([
            item({ status: 'TODO' }),
            item({ status: 'TODO' }),
            item({ status: 'WAITING' }),
            item({ status: 'DONE', tags: 'repo,merged' }),
            item({ status: 'DONE' }),
        ]);
        expect(counts).toEqual({ open: 2, draft: 1, merged: 1, closed: 1 });
    });

    test('is all zeroes for no items', () => {
        expect(countByState([])).toEqual({ open: 0, draft: 0, merged: 0, closed: 0 });
    });
});

describe('authorLogin', () => {
    test('strips the parenthesized full name', () => {
        expect(authorLogin('C-Hipple (Chris Hipple)')).toBe('C-Hipple');
    });

    test('leaves a bare login alone', () => {
        expect(authorLogin('test-user-afk')).toBe('test-user-afk');
        expect(authorLogin(undefined)).toBe('');
    });
});

describe('formatRelativeTime', () => {
    const now = new Date('2026-08-06T12:00:00');

    test('sub-minute is "just now"', () => {
        expect(formatRelativeTime('2026-08-06T11:59:30', now)).toBe('just now');
    });

    test('minutes within the hour', () => {
        expect(formatRelativeTime('2026-08-06T11:46:00', now)).toBe('14m');
    });

    test('earlier the same calendar day is "today"', () => {
        expect(formatRelativeTime('2026-08-06T02:00:00', now)).toBe('today');
    });

    test('the previous calendar day is "yesterday"', () => {
        expect(formatRelativeTime('2026-08-05T23:00:00', now)).toBe('yesterday');
    });

    test('days, weeks, months and years', () => {
        expect(formatRelativeTime('2026-08-03T12:00:00', now)).toBe('3d');
        expect(formatRelativeTime('2026-07-20T12:00:00', now)).toBe('2w');
        expect(formatRelativeTime('2026-03-06T12:00:00', now)).toBe('5mo');
        expect(formatRelativeTime('2024-08-06T12:00:00', now)).toBe('2y');
    });

    test('empty for missing, unparseable, or zero timestamps', () => {
        expect(formatRelativeTime(undefined, now)).toBe('');
        expect(formatRelativeTime('', now)).toBe('');
        expect(formatRelativeTime('not a date', now)).toBe('');
        expect(formatRelativeTime('0001-01-01T00:00:00Z', now)).toBe('');
    });
});

describe('itemMatchesQuery', () => {
    test('matches title, repo, owner and author case-insensitively', () => {
        expect(itemMatchesQuery(item(), 'test files')).toBe(true);
        expect(itemMatchesQuery(item(), 'CODE-REVIEW')).toBe(true);
        expect(itemMatchesQuery(item(), 'c-hipple')).toBe(true);
        expect(itemMatchesQuery(item(), 'nothing here')).toBe(false);
    });

    test('matches the PR number by prefix, with or without a leading hash', () => {
        expect(itemMatchesQuery(item({ number: 83 }), '#83')).toBe(true);
        expect(itemMatchesQuery(item({ number: 83 }), '83')).toBe(true);
        expect(itemMatchesQuery(item({ number: 830 }), '83')).toBe(true);
        expect(itemMatchesQuery(item({ number: 83 }), '99')).toBe(false);
    });

    test('an empty query matches everything', () => {
        expect(itemMatchesQuery(item(), '   ')).toBe(true);
    });
});

describe('facetsByOpenCount', () => {
    const items = [
        item({ repo: 'diff-lsp', status: 'TODO' }),
        item({ repo: 'diff-lsp', status: 'TODO' }),
        item({ repo: 'diff-lsp', status: 'DONE', tags: 'diff-lsp,merged' }),
        item({ repo: 'gtdbot', status: 'TODO' }),
        item({ repo: 'archived', status: 'DONE', tags: 'archived,merged' }),
        item({ repo: 'code-review-server', status: 'TODO' }),
    ];

    test('orders by open count, then alphabetically', () => {
        expect(facetsByOpenCount(items, i => i.repo)).toEqual([
            { value: 'diff-lsp', openCount: 2 },
            { value: 'code-review-server', openCount: 1 },
            { value: 'gtdbot', openCount: 1 },
            { value: 'archived', openCount: 0 },
        ]);
    });

    test('keeps values whose PRs are all closed, at a count of zero', () => {
        const facets = facetsByOpenCount(items, i => i.repo);
        expect(facets.find(f => f.value === 'archived')).toEqual({
            value: 'archived',
            openCount: 0,
        });
    });

    test('keys through a projection, so authors fold to their login', () => {
        const authors = [
            item({ author: 'zoe (Zoe Q)', status: 'TODO' }),
            item({ author: 'zoe', status: 'TODO' }),
            item({ author: '', status: 'TODO' }),
        ];
        expect(facetsByOpenCount(authors, i => authorLogin(i.author))).toEqual([
            { value: 'zoe', openCount: 2 },
        ]);
    });

    test('is empty for no items', () => {
        expect(facetsByOpenCount([], i => i.repo)).toEqual([]);
    });
});

describe('uniqueValues', () => {
    test('dedupes, sorts case-insensitively, and drops blanks', () => {
        const items = [
            item({ author: 'zoe' }),
            item({ author: 'Adam' }),
            item({ author: 'zoe' }),
            item({ author: '' }),
        ];
        expect(uniqueValues(items, 'author')).toEqual(['Adam', 'zoe']);
    });
});

describe('groupBySection', () => {
    test('keeps the order the server sent sections in', () => {
        const grouped = groupBySection([
            item({ section: 'Open PRs', number: 1 }),
            item({ section: 'WaitingOnAuthor', number: 2 }),
            item({ section: 'Open PRs', number: 3 }),
        ]);
        expect(grouped.map(s => s.name)).toEqual(['Open PRs', 'WaitingOnAuthor']);
        expect(grouped[0].items.map(i => i.number)).toEqual([1, 3]);
    });

    test('files section-less items under Other', () => {
        expect(groupBySection([item({ section: '' })])[0].name).toBe('Other');
    });
});

describe('parseGitHubPRUrl', () => {
    test('parses a pull request URL', () => {
        expect(parseGitHubPRUrl('https://github.com/C-Hipple/diff-lsp/pull/12')).toEqual({
            owner: 'C-Hipple',
            repo: 'diff-lsp',
            number: 12,
        });
    });

    test('returns null for anything else', () => {
        expect(parseGitHubPRUrl('diff-lsp')).toBeNull();
        expect(parseGitHubPRUrl('https://github.com/C-Hipple/diff-lsp')).toBeNull();
    });
});
