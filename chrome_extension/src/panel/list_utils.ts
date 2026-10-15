// The review list's pure logic: grouping GetAllReviews items into sections,
// filtering them, and the tags a row shows.

import { samePull, type PullRef } from '../github_url';
import type { ReviewEase, ReviewItem } from '../types';

export interface ReviewGroup {
    section: string;
    priority: number;
    items: ReviewItem[];
}

/**
 * Items grouped by section: sections by `section_priority` (lower first),
 * ties in order of first appearance; items within a section in the server's
 * order.
 */
export function groupReviews(items: readonly ReviewItem[]): ReviewGroup[] {
    const groups = new Map<string, ReviewGroup & { first: number }>();
    items.forEach((item, index) => {
        let group = groups.get(item.section);
        if (!group) {
            group = {
                section: item.section,
                priority: item.section_priority,
                items: [],
                first: index,
            };
            groups.set(item.section, group);
        }
        group.items.push(item);
    });
    return [...groups.values()]
        .sort((a, b) => a.priority - b.priority || a.first - b.first)
        .map(({ section, priority, items }) => ({ section, priority, items }));
}

/**
 * Whether an item matches the filter: a case-insensitive substring of its
 * title, `owner/repo`, author or number (`#42` or `42`). An empty filter
 * matches everything.
 */
export function matchesFilter(item: ReviewItem, query: string): boolean {
    const q = query.trim().toLowerCase();
    if (!q) return true;
    const number = String(item.number);
    if (/^#?\d+$/.test(q)) {
        const digits = q.replace(/^#/, '');
        if (number.includes(digits)) return true;
    }
    const haystack = [
        item.title,
        `${item.owner}/${item.repo}`,
        `${item.owner}/${item.repo}#${number}`,
        item.author,
    ];
    return haystack.some(text => text.toLowerCase().includes(q));
}

export function filterReviews(items: readonly ReviewItem[], query: string): ReviewItem[] {
    return items.filter(item => matchesFilter(item, query));
}

/** The comma-joined `tags` field as lowercase tags. */
export function parseTags(tags: string): string[] {
    return tags
        .split(',')
        .map(t => t.trim().toLowerCase())
        .filter(Boolean);
}

export type PRState = 'open' | 'draft' | 'merged' | 'closed';

/**
 * A list item's lifecycle state, as the server buckets it: WAITING or a
 * `draft` tag is a draft, DONE is merged when tagged so and closed otherwise.
 */
export function itemState(item: ReviewItem): PRState {
    const tags = parseTags(item.tags);
    const status = item.status.toUpperCase();
    if (tags.includes('merged')) return 'merged';
    if (status === 'WAITING' || tags.includes('draft')) return 'draft';
    if (status === 'DONE' || status === 'CANCELLED') return 'closed';
    return 'open';
}

export type Tone = 'neutral' | 'accent' | 'success' | 'attention' | 'danger' | 'done';

export interface RowTag {
    key: string;
    label: string;
    tone: Tone;
    title?: string;
}

export const EASE_TONE: Record<ReviewEase, Tone> = {
    easy: 'success',
    medium: 'attention',
    hard: 'danger',
};

/** The pills a row shows after its title: draft / merged, conflict, review ease. */
export function rowTags(item: ReviewItem): RowTag[] {
    const tags = parseTags(item.tags);
    const out: RowTag[] = [];
    if (tags.includes('draft')) out.push({ key: 'draft', label: 'Draft', tone: 'neutral' });
    if (tags.includes('merged')) out.push({ key: 'merged', label: 'Merged', tone: 'done' });
    if (item.merge_conflicts) {
        out.push({
            key: 'conflict',
            label: 'Conflict',
            tone: 'danger',
            title: 'The branch conflicts with its base',
        });
    }
    if (item.review_ease) {
        out.push({
            key: 'ease',
            label: item.review_ease,
            tone: EASE_TONE[item.review_ease] ?? 'neutral',
            title: `Review ease: ${item.review_ease}`,
        });
    }
    return out;
}

/** Whether a list item is the PR `pr` names. */
export function isItem(item: ReviewItem, pr: PullRef | null): boolean {
    return samePull(item, pr);
}

/** `owner/repo#N`. */
export function prLabel(pr: PullRef): string {
    return `${pr.owner}/${pr.repo}#${pr.number}`;
}

/** The list item for `pr`, if it is in the list. */
export function findItem(items: readonly ReviewItem[] | null, pr: PullRef): ReviewItem | null {
    return items?.find(item => samePull(item, pr)) ?? null;
}
