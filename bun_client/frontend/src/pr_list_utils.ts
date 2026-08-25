/**
 * Pure helpers for the review list.
 *
 * The list view groups PRs by lifecycle state (open / draft / merged / closed),
 * which the backend does not send as a field — it has to be derived from the
 * `status` + `tags` pair the way the server itself does in `prStatusOrder`
 * (server/server.go). Everything here is deliberately side-effect free so the
 * derivations can be unit tested without rendering.
 */

import { teamSearchTerms, type RequiredTeam } from './team_utils';

/** Matches the ReviewItem struct from the Go backend. */
export interface ReviewItem {
    section: string;
    status: string;
    /** Comma-joined org tags: always the head repo name, plus "draft" / "merged". */
    tags?: string;
    title: string;
    owner: string;
    repo: string;
    number: number;
    author: string;
    url: string;
    /** LLM review-ease rating: "easy", "medium", "hard", or "". */
    review_ease?: string;
    /** RFC3339 time the item was first recorded by a workflow. */
    created_at?: string;
    /**
     * Teams this PR needs a review from, each with its status and its relation
     * to the current user. Absent until a workflow cycle has resolved them.
     */
    required_teams?: RequiredTeam[];
}

/** The lifecycle states the sidebar buckets PRs into. */
export type PRState = 'open' | 'draft' | 'merged' | 'closed';

export const PR_STATES: readonly PRState[] = ['open', 'draft', 'merged', 'closed'];

export type StateCounts = Record<PRState, number>;

/** Split the backend's comma-joined tag string into normalized tags. */
export function parseTags(tags: string | undefined): string[] {
    return (tags || '')
        .split(',')
        .map(tag => tag.trim().toLowerCase())
        .filter(Boolean);
}

/**
 * Derive a PR's lifecycle state. Mirrors the server's own bucketing: TODO is
 * open, WAITING is a draft, and DONE is merged or closed depending on whether
 * the workflow tagged it "merged". Items from other org sources can carry
 * statuses the server doesn't emit, so the tags are used as a fallback and
 * anything left over is treated as open rather than being dropped.
 */
export function prState(item: ReviewItem): PRState {
    const status = (item.status || '').toUpperCase();
    const tags = parseTags(item.tags);

    if (status === 'WAITING' || tags.includes('draft')) return 'draft';
    if (status === 'DONE' || status === 'CANCELLED') {
        return tags.includes('merged') ? 'merged' : 'closed';
    }
    if (tags.includes('merged')) return 'merged';
    return 'open';
}

/** Count items per lifecycle state. */
export function countByState(items: ReviewItem[]): StateCounts {
    const counts: StateCounts = { open: 0, draft: 0, merged: 0, closed: 0 };
    for (const item of items) {
        counts[prState(item)]++;
    }
    return counts;
}

/**
 * The backend renders authors as `login` or `login (Full Name)`. Rows are dense,
 * so show just the login and keep the full string for the tooltip.
 */
export function authorLogin(author: string | undefined): string {
    return (author || '').replace(/\s*\(.*\)\s*$/, '').trim();
}

/**
 * Compact relative time for a row's timestamp: "just now", "14m", "today",
 * "yesterday", "3d", "2w", "5mo", "2y". Returns '' for missing or unparseable
 * timestamps (including Go's zero time, which arrives as year 1).
 */
export function formatRelativeTime(iso: string | undefined, now: Date = new Date()): string {
    if (!iso) return '';
    const then = new Date(iso);
    const thenMs = then.getTime();
    if (!Number.isFinite(thenMs)) return '';
    // Go serializes an unset time.Time as year 1; treat anything pre-1971 as unset.
    if (then.getFullYear() < 1971) return '';

    const diffMs = now.getTime() - thenMs;
    if (diffMs < 60_000) return 'just now';

    const minutes = Math.floor(diffMs / 60_000);
    if (minutes < 60) return `${minutes}m`;

    const days = calendarDaysBetween(then, now);
    if (days <= 0) return 'today';
    if (days === 1) return 'yesterday';
    if (days < 7) return `${days}d`;
    if (days < 30) return `${Math.floor(days / 7)}w`;
    if (days < 365) return `${Math.floor(days / 30)}mo`;
    return `${Math.floor(days / 365)}y`;
}

/** Whole calendar days from `from` to `to`, in local time. */
function calendarDaysBetween(from: Date, to: Date): number {
    const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    return Math.round((startOfDay(to) - startOfDay(from)) / 86_400_000);
}

/** Absolute timestamp for a row's tooltip. */
export function formatAbsoluteTime(iso: string | undefined): string {
    if (!iso) return '';
    const date = new Date(iso);
    if (!Number.isFinite(date.getTime()) || date.getFullYear() < 1971) return '';
    return date.toLocaleString();
}

/**
 * Free-text match across the fields shown in a row: title, repo, owner, author,
 * required teams, and PR number. A numeric query (with or without a leading
 * `#`) matches the number by prefix, so the list narrows as the number is typed.
 */
export function itemMatchesQuery(item: ReviewItem, query: string): boolean {
    const needle = query.trim().toLowerCase();
    if (!needle) return true;

    const fields = [
        item.title,
        item.repo,
        item.owner,
        item.author,
        ...teamSearchTerms(item.required_teams),
    ];
    if (fields.some(field => (field || '').toLowerCase().includes(needle))) {
        return true;
    }
    const numeric = needle.replace(/^#/, '');
    return /^\d+$/.test(numeric) && String(item.number).startsWith(numeric);
}

export interface Facet {
    value: string;
    /** How many of this value's rows are open PRs. */
    openCount: number;
}

/**
 * Build the checkable facet list for a field — every distinct value, ordered by
 * how many open PRs it has (busiest first) and alphabetically within a tie.
 *
 * Counts are rows, not distinct PRs: a PR that several workflows surface appears
 * once per section, exactly like the state counts in the sidebar above. They are
 * also taken over all items rather than the filtered set, so checking a box
 * never reshuffles the list under the pointer.
 */
export function facetsByOpenCount(items: ReviewItem[], key: (item: ReviewItem) => string): Facet[] {
    const counts = new Map<string, number>();
    for (const item of items) {
        const value = key(item).trim();
        if (!value) continue;
        const open = prState(item) === 'open' ? 1 : 0;
        counts.set(value, (counts.get(value) ?? 0) + open);
    }
    return Array.from(counts, ([value, openCount]) => ({ value, openCount })).sort(
        (a, b) =>
            b.openCount - a.openCount || a.value.toLowerCase().localeCompare(b.value.toLowerCase())
    );
}

/** Unique, case-insensitively sorted values of a field across items. */
export function uniqueValues(items: ReviewItem[], field: keyof ReviewItem): string[] {
    const values = new Set<string>();
    for (const item of items) {
        const value = String(item[field] ?? '').trim();
        if (value) values.add(value);
    }
    return Array.from(values).sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
}

export interface Section {
    name: string;
    items: ReviewItem[];
}

/**
 * Group items into sections, preserving the order the server sent them in
 * (sections are already ordered by priority) and dropping empty groups.
 */
export function groupBySection(items: ReviewItem[]): Section[] {
    const sections: Section[] = [];
    const byName = new Map<string, Section>();
    for (const item of items) {
        const name = item.section || 'Other';
        let section = byName.get(name);
        if (!section) {
            section = { name, items: [] };
            byName.set(name, section);
            sections.push(section);
        }
        section.items.push(item);
    }
    return sections;
}

export interface ParsedPRUrl {
    owner: string;
    repo: string;
    number: number;
}

/** Parse a github.com pull request URL, so pasting one can open it directly. */
export function parseGitHubPRUrl(value: string): ParsedPRUrl | null {
    const match = value.match(/https?:\/\/github\.com\/([^/]+)\/([^/]+)\/pull\/(\d+)/);
    if (!match) return null;
    return { owner: match[1], repo: match[2], number: parseInt(match[3], 10) };
}
