import { describe, expect, test } from 'bun:test';
import type { ReviewItem } from '../types';
import {
    filterReviews,
    findItem,
    groupReviews,
    itemState,
    matchesFilter,
    parseTags,
    rowTags,
} from './list_utils';

function item(overrides: Partial<ReviewItem>): ReviewItem {
    return {
        section: 'Needs review',
        section_priority: 1,
        status: 'TODO',
        tags: 'widgets',
        title: 'A change',
        owner: 'acme',
        repo: 'widgets',
        number: 1,
        author: 'mona',
        url: 'https://github.com/acme/widgets/pull/1',
        release_status: '',
        review_ease: '',
        created_at: '2026-10-01T00:00:00Z',
        required_teams: null,
        comment_count: 0,
        merge_conflicts: false,
        ...overrides,
    };
}

describe('groupReviews', () => {
    test('orders sections by priority, then first appearance; items keep server order', () => {
        const items = [
            item({ section: 'Mine', section_priority: 2, number: 1 }),
            item({ section: 'Review', section_priority: 1, number: 2 }),
            item({ section: 'Team', section_priority: 2, number: 3 }),
            item({ section: 'Mine', section_priority: 2, number: 4 }),
            item({ section: 'Review', section_priority: 1, number: 5 }),
            item({ section: 'Done', section_priority: 9, number: 6 }),
        ];
        const groups = groupReviews(items);
        expect(groups.map(g => g.section)).toEqual(['Review', 'Mine', 'Team', 'Done']);
        expect(groups.map(g => g.items.map(i => i.number))).toEqual([[2, 5], [1, 4], [3], [6]]);
        expect(groups[1].priority).toBe(2);
    });

    test('an empty list has no groups', () => {
        expect(groupReviews([])).toEqual([]);
    });
});

describe('filter', () => {
    const pr = item({
        title: 'Rate-limit the API',
        owner: 'Acme',
        repo: 'widgets',
        number: 1312,
        author: 'Octocat',
    });

    test.each([
        ['', true],
        ['   ', true],
        ['rate-LIMIT', true],
        ['acme/widgets', true],
        ['widg', true],
        ['acme/widgets#1312', true],
        ['octo', true],
        ['1312', true],
        ['#1312', true],
        ['#13', true],
        ['billing', false],
        ['#99', false],
    ])('%p matches: %p', (query, expected) => {
        expect(matchesFilter(pr, query)).toBe(expected);
    });

    test('filterReviews keeps order', () => {
        const items = [
            item({ number: 1, title: 'Fix cache' }),
            item({ number: 2, title: 'Add docs' }),
            item({ number: 3, title: 'Fix typo' }),
        ];
        expect(filterReviews(items, 'fix').map(i => i.number)).toEqual([1, 3]);
    });
});

describe('tags and state', () => {
    test('parseTags splits and lowercases', () => {
        expect(parseTags(' widgets, Draft ,,merged')).toEqual(['widgets', 'draft', 'merged']);
    });

    test('rowTags: draft, merged, conflict and review ease, in that order', () => {
        expect(
            rowTags(item({ tags: 'widgets,draft', merge_conflicts: true, review_ease: 'hard' }))
        ).toEqual([
            { key: 'draft', label: 'Draft', tone: 'neutral' },
            {
                key: 'conflict',
                label: 'Conflict',
                tone: 'danger',
                title: 'The branch conflicts with its base',
            },
            { key: 'ease', label: 'hard', tone: 'danger', title: 'Review ease: hard' },
        ]);
        expect(rowTags(item({ tags: 'widgets,merged' })).map(t => t.key)).toEqual(['merged']);
        expect(rowTags(item({ review_ease: 'easy' }))[0].tone).toBe('success');
        expect(rowTags(item({ review_ease: 'medium' }))[0].tone).toBe('attention');
        // The head repo's name is a tag too, but not one a row shows.
        expect(rowTags(item({ tags: 'widgets' }))).toEqual([]);
    });

    test('itemState', () => {
        expect(itemState(item({}))).toBe('open');
        expect(itemState(item({ status: 'WAITING' }))).toBe('draft');
        expect(itemState(item({ tags: 'x,draft' }))).toBe('draft');
        expect(itemState(item({ status: 'DONE', tags: 'x,merged' }))).toBe('merged');
        expect(itemState(item({ status: 'DONE' }))).toBe('closed');
    });

    test('findItem matches owner and repo case-insensitively', () => {
        const items = [item({ number: 7 }), item({ number: 8 })];
        expect(findItem(items, { owner: 'ACME', repo: 'Widgets', number: 8 })?.number).toBe(8);
        expect(findItem(items, { owner: 'acme', repo: 'widgets', number: 9 })).toBeNull();
        expect(findItem(null, { owner: 'acme', repo: 'widgets', number: 8 })).toBeNull();
    });
});
