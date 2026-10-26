// Filtered diffs: the PR's diff narrowed to the commits after a baseline the
// server picks — the changes since the reviewer's last review, say. See
// docs/protocol.md#filtered-diffs.

/** A filter the server offers, as ListDiffFilters lists it. */
export interface DiffFilterInfo {
    id: string;
    name: string;
    description: string;
}

/** The interaction a filtered diff starts from. */
export interface FilteredDiffBaseline {
    sha: string;
    at: string;
    /** "review", "review_comment" or "comment". */
    kind: string;
    /** e.g. "your approving review". */
    detail: string;
    /** The interaction named no commit, so the head at the time stands in. */
    inferred: boolean;
}

export type FilteredDiffStatus = 'ready' | 'no-baseline' | 'rebased' | 'error';

/** GetFilteredDiff's reply. */
export interface FilteredDiffResult {
    filter: string;
    status: FilteredDiffStatus;
    head_sha: string;
    base_sha: string;
    baseline: FilteredDiffBaseline | null;
    /** Rendered like GetPR's diff; empty unless status is "ready". */
    diff: string;
    /** SHAs of the commits after the baseline, oldest first. */
    commits: string[] | null;
    /**
     * Per file, a row of `diff` (its comment position) to the position of the
     * same line in the PR's full diff, which is what comments are positioned
     * in. Rows it leaves out (removed lines, lines the full diff doesn't
     * show) take no comment. Absent from an older server.
     */
    positions?: Record<string, Record<string, number>> | null;
    message: string;
    computed_at: string;
}

export const shortSha = (sha: string): string => sha.slice(0, 7);

/**
 * The position in the PR's full diff of row `pos` of the filtered diff's
 * `file`, or null when that row shows no line of the full diff.
 */
export function toFullPosition(
    result: FilteredDiffResult | null,
    file: string,
    pos: number
): number | null {
    const full = result?.positions?.[file]?.[String(pos)];
    return typeof full === 'number' ? full : null;
}

/**
 * Comments placed on a filtered diff: each one whose line it shows, at that
 * line's row there. A comment's position counts rows of the full diff; the
 * rest are on lines the filtered diff doesn't show, and are left out.
 */
export function commentsOnFilteredDiff<T extends { path: string; position: string }>(
    comments: T[],
    result: FilteredDiffResult | null
): T[] {
    const rows = new Map<string, Map<number, number>>();
    for (const [file, map] of Object.entries(result?.positions ?? {})) {
        const inverse = new Map<number, number>();
        for (const [row, full] of Object.entries(map)) inverse.set(full, Number(row));
        rows.set(file, inverse);
    }
    const placed: T[] = [];
    for (const c of comments) {
        const inverse = rows.get(c.path);
        if (!inverse) continue;
        // A whole-file comment stays one wherever the file is shown.
        if (c.position === '') {
            placed.push(c);
            continue;
        }
        const row = inverse.get(Number(c.position));
        if (row !== undefined) placed.push({ ...c, position: String(row) });
    }
    return placed;
}

/**
 * Whether the review shows the filtered diff for this result. Only a ready
 * result has one; for anything else the review stays on the full diff and
 * the banner says why.
 */
export const showsFilteredDiff = (result: FilteredDiffResult | null): boolean =>
    result !== null && result.status === 'ready';

export interface FilteredDiffSummary {
    text: string;
    variant: 'info' | 'warning' | 'danger';
}

/** What the banner above the diff says about a filtered diff. */
export function filteredDiffSummary(
    result: FilteredDiffResult,
    filterName: string
): FilteredDiffSummary {
    switch (result.status) {
        case 'ready': {
            const detail = result.baseline?.detail ?? 'the baseline';
            const from = shortSha(result.base_sha);
            if (!result.diff.trim()) {
                return {
                    text: `${filterName}: nothing has changed since ${detail} (${from}).`,
                    variant: 'info',
                };
            }
            const n = result.commits?.length ?? 0;
            const commits = n > 0 ? `, ${n} new commit${n === 1 ? '' : 's'}` : '';
            const guess = result.baseline?.inferred ? ' (estimated from its time)' : '';
            return {
                text: `${filterName}: changes since ${detail}${guess} — ${from} → ${shortSha(result.head_sha)}${commits}. Comments on lines outside this range are hidden.`,
                variant: 'info',
            };
        }
        case 'no-baseline':
            return { text: `${result.message} Showing the full diff.`, variant: 'warning' };
        case 'rebased':
            return { text: `${result.message} Showing the full diff.`, variant: 'warning' };
        default:
            return {
                text: `Couldn't load "${filterName}": ${result.message} Showing the full diff.`,
                variant: 'danger',
            };
    }
}
