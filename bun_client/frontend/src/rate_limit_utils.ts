import type { RateLimitHistoryPoint } from './api';

/**
 * Derivations behind the rate limit history charts. These are kept out of the
 * component so the scaling and summarising can be tested without rendering.
 *
 * The series is one point per completed workflow cycle, so its resolution is
 * the server's configured sleep duration. Points whose `limit` is -1 recorded
 * no usable budget reading and must be treated as gaps, never as zero.
 */

/** The per-call-type counters, in the order the breakdown lists them. */
export const CALL_TYPES: Array<{ key: keyof RateLimitHistoryPoint; label: string }> = [
    { key: 'pr_list', label: 'PR list' },
    { key: 'pr_specific', label: 'PR detail' },
    { key: 'diff', label: 'Diff' },
    { key: 'comments', label: 'Review comments' },
    { key: 'issue_comments', label: 'Issue comments' },
    { key: 'reviews', label: 'Reviews' },
    { key: 'review_threads', label: 'Review threads' },
    { key: 'team_reviews', label: 'Team reviews' },
    { key: 'commits', label: 'Commits' },
    { key: 'ci_status', label: 'CI status' },
    { key: 'combined_status', label: 'Combined status' },
    { key: 'check_runs', label: 'Check runs' },
];

/** A point carries a real budget reading only when the server sent a limit. */
export function hasBudget(point: RateLimitHistoryPoint): boolean {
    return point.limit > 0 && point.remaining >= 0;
}

export interface WindowSummary {
    cycles: number;
    totalCalls: number;
    avgCallsPerCycle: number;
    peakCallsPerCycle: number;
    /** Calls per minute across the whole window, not an average of the per-cycle rates. */
    callsPerMinute: number;
    /** The newest point that carried a real budget reading, if any. */
    latestRemaining: number | null;
    latestLimit: number | null;
    latestResetAt: string;
    /** Budget left as a fraction of the limit, or null when unknown. */
    remainingFraction: number | null;
    /** Budget spent between the oldest and newest readings, ignoring resets. */
    spanStart: number | null;
    spanEnd: number | null;
}

export function summarizeWindow(points: RateLimitHistoryPoint[]): WindowSummary {
    const empty: WindowSummary = {
        cycles: 0,
        totalCalls: 0,
        avgCallsPerCycle: 0,
        peakCallsPerCycle: 0,
        callsPerMinute: 0,
        latestRemaining: null,
        latestLimit: null,
        latestResetAt: '',
        remainingFraction: null,
        spanStart: null,
        spanEnd: null,
    };
    if (points.length === 0) return empty;

    const totalCalls = points.reduce((sum, p) => sum + p.total, 0);
    const peakCallsPerCycle = points.reduce((max, p) => Math.max(max, p.total), 0);

    // Rate over the window is total spend divided by the wall-clock span the
    // cycles cover. A single cycle has no span, so it reports its own rate
    // only if the server measured a gap for it.
    const times = points.map(p => Date.parse(p.recorded_at)).filter(t => Number.isFinite(t));
    let spanMinutes = 0;
    if (times.length > 1) {
        spanMinutes = (Math.max(...times) - Math.min(...times)) / 60000;
    }
    const callsPerMinute =
        spanMinutes > 0 ? totalCalls / spanMinutes : (points[0]?.calls_per_minute ?? 0);

    const withBudget = points.filter(hasBudget);
    const latest = withBudget.length > 0 ? withBudget[withBudget.length - 1] : null;
    const oldest = withBudget.length > 0 ? withBudget[0] : null;

    return {
        cycles: points.length,
        totalCalls,
        avgCallsPerCycle: totalCalls / points.length,
        peakCallsPerCycle,
        callsPerMinute,
        latestRemaining: latest ? latest.remaining : null,
        latestLimit: latest ? latest.limit : null,
        latestResetAt: latest ? latest.reset_at : '',
        remainingFraction: latest && latest.limit > 0 ? latest.remaining / latest.limit : null,
        spanStart: oldest ? oldest.remaining : null,
        spanEnd: latest ? latest.remaining : null,
    };
}

export interface BreakdownEntry {
    key: string;
    label: string;
    total: number;
    share: number;
}

/**
 * Totals each call type over the window, largest first. Types that were never
 * called are dropped: an all-zero row tells the reader nothing.
 */
export function callTypeBreakdown(points: RateLimitHistoryPoint[]): BreakdownEntry[] {
    const grandTotal = points.reduce((sum, p) => sum + p.total, 0);
    return CALL_TYPES.map(({ key, label }) => {
        const total = points.reduce((sum, p) => sum + (p[key] as number), 0);
        return { key: String(key), label, total, share: grandTotal > 0 ? total / grandTotal : 0 };
    })
        .filter(entry => entry.total > 0)
        .sort((a, b) => b.total - a.total);
}

export interface PlotBox {
    width: number;
    height: number;
    padLeft: number;
    padRight: number;
    padTop: number;
    padBottom: number;
}

/**
 * Maps a timestamp onto the plot's x range. A window whose points all share one
 * timestamp — or a window with a single point — is centred rather than divided
 * by zero.
 */
export function xForTime(t: number, minT: number, maxT: number, box: PlotBox): number {
    const plotWidth = box.width - box.padLeft - box.padRight;
    if (!(maxT > minT)) return box.padLeft + plotWidth / 2;
    return box.padLeft + ((t - minT) / (maxT - minT)) * plotWidth;
}

/** Maps a value onto the plot's y range, with y growing downward in SVG. */
export function yForValue(v: number, minV: number, maxV: number, box: PlotBox): number {
    const plotHeight = box.height - box.padTop - box.padBottom;
    if (!(maxV > minV)) return box.padTop + plotHeight / 2;
    const clamped = Math.min(Math.max(v, minV), maxV);
    return box.padTop + plotHeight - ((clamped - minV) / (maxV - minV)) * plotHeight;
}

/**
 * Splits the series into runs of consecutive points that carry a real budget
 * reading. Each run is drawn as its own polyline, so a cycle that recorded no
 * reading leaves a visible gap instead of a line dropping to the axis.
 */
export function budgetSegments(points: RateLimitHistoryPoint[]): RateLimitHistoryPoint[][] {
    const segments: RateLimitHistoryPoint[][] = [];
    let current: RateLimitHistoryPoint[] = [];
    for (const point of points) {
        if (hasBudget(point)) {
            current.push(point);
        } else if (current.length > 0) {
            segments.push(current);
            current = [];
        }
    }
    if (current.length > 0) segments.push(current);
    return segments;
}

/**
 * Axis ticks at 1/2/5 x a power of ten, covering 0..max. Always returns at
 * least two ticks so an axis is drawn even for an empty or flat series.
 */
export function niceTicks(max: number, count = 4): number[] {
    if (!Number.isFinite(max) || max <= 0) return [0, 1];
    const magnitude = Math.pow(10, Math.floor(Math.log10(max / count)));
    const normalized = max / count / magnitude;
    const step = (normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10) * magnitude;
    const ticks: number[] = [];
    for (let value = 0; value < max; value += step) {
        ticks.push(Math.round(value * 1e6) / 1e6);
    }
    ticks.push(Math.round((ticks[ticks.length - 1] + step) * 1e6) / 1e6);
    return ticks;
}

/** Bar width from the tightest gap between cycles, so gaps stay visible. */
export function barWidthFor(points: RateLimitHistoryPoint[], box: PlotBox): number {
    const plotWidth = box.width - box.padLeft - box.padRight;
    if (points.length < 2) return Math.min(32, plotWidth / 3);
    const times = points.map(p => Date.parse(p.recorded_at)).filter(t => Number.isFinite(t));
    if (times.length < 2) return Math.min(32, plotWidth / 3);
    const span = Math.max(...times) - Math.min(...times);
    if (span <= 0) return Math.min(32, plotWidth / 3);
    let smallestGap = Infinity;
    for (let i = 1; i < times.length; i++) {
        const gap = times[i] - times[i - 1];
        if (gap > 0) smallestGap = Math.min(smallestGap, gap);
    }
    if (!Number.isFinite(smallestGap)) return Math.min(32, plotWidth / 3);
    return Math.min(32, Math.max(2, (smallestGap / span) * plotWidth * 0.7));
}

/** Clock time for an axis label, in the viewer's local zone. */
export function formatClock(iso: string): string {
    const t = Date.parse(iso);
    if (!Number.isFinite(t)) return '';
    return new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/** Compact counts for axis labels: 4980 reads as "5.0k". */
export function formatCount(value: number): string {
    if (!Number.isFinite(value)) return '—';
    if (Math.abs(value) >= 1000) return `${(value / 1000).toFixed(1)}k`;
    return String(Math.round(value * 100) / 100);
}

/** How long until the hourly budget resets, e.g. "in 24m". */
export function formatResetIn(iso: string, now: number = Date.now()): string {
    const t = Date.parse(iso);
    if (!Number.isFinite(t)) return '';
    const minutes = Math.round((t - now) / 60000);
    if (minutes <= 0) return 'now';
    if (minutes < 60) return `in ${minutes}m`;
    return `in ${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}
