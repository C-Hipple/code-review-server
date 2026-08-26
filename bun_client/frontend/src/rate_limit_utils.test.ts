import { describe, expect, test } from 'bun:test';
import type { RateLimitHistoryPoint } from './api';
import {
    barWidthFor,
    budgetSegments,
    callTypeBreakdown,
    formatCount,
    formatResetIn,
    hasBudget,
    niceTicks,
    summarizeWindow,
    xForTime,
    yForValue,
} from './rate_limit_utils';
import type { PlotBox } from './rate_limit_utils';

const BOX: PlotBox = {
    width: 720,
    height: 220,
    padLeft: 52,
    padRight: 12,
    padTop: 14,
    padBottom: 28,
};

function point(overrides: Partial<RateLimitHistoryPoint> = {}): RateLimitHistoryPoint {
    return {
        recorded_at: '2026-08-26T12:00:00Z',
        pr_list: 0,
        pr_specific: 0,
        comments: 0,
        issue_comments: 0,
        ci_status: 0,
        diff: 0,
        reviews: 0,
        combined_status: 0,
        check_runs: 0,
        commits: 0,
        review_threads: 0,
        team_reviews: 0,
        total: 0,
        remaining: 5000,
        limit: 5000,
        reset_at: '',
        gap_minutes: 0,
        calls_per_minute: 0,
        ...overrides,
    };
}

describe('hasBudget', () => {
    test('a real reading counts', () => {
        expect(hasBudget(point({ remaining: 4200, limit: 5000 }))).toBe(true);
    });

    test('the unknown sentinel does not', () => {
        expect(hasBudget(point({ remaining: -1, limit: -1 }))).toBe(false);
    });

    test('an exhausted budget is still a reading', () => {
        expect(hasBudget(point({ remaining: 0, limit: 5000 }))).toBe(true);
    });
});

describe('summarizeWindow', () => {
    test('an empty window reports nothing rather than NaN', () => {
        const summary = summarizeWindow([]);
        expect(summary.cycles).toBe(0);
        expect(summary.totalCalls).toBe(0);
        expect(summary.avgCallsPerCycle).toBe(0);
        expect(summary.callsPerMinute).toBe(0);
        expect(summary.latestRemaining).toBeNull();
        expect(summary.remainingFraction).toBeNull();
    });

    test('totals, peak and rate come from the whole window', () => {
        const summary = summarizeWindow([
            point({ recorded_at: '2026-08-26T12:00:00Z', total: 40, remaining: 4960 }),
            point({ recorded_at: '2026-08-26T12:10:00Z', total: 60, remaining: 4900 }),
            point({ recorded_at: '2026-08-26T12:20:00Z', total: 100, remaining: 4800 }),
        ]);
        expect(summary.cycles).toBe(3);
        expect(summary.totalCalls).toBe(200);
        expect(summary.peakCallsPerCycle).toBe(100);
        expect(summary.avgCallsPerCycle).toBeCloseTo(200 / 3);
        // 200 calls spread over the 20 minutes the window spans.
        expect(summary.callsPerMinute).toBeCloseTo(10);
    });

    test('the latest budget comes from the newest real reading, not the newest point', () => {
        const summary = summarizeWindow([
            point({ recorded_at: '2026-08-26T12:00:00Z', remaining: 4900, limit: 5000 }),
            point({
                recorded_at: '2026-08-26T12:10:00Z',
                remaining: 4000,
                limit: 5000,
                reset_at: 'x',
            }),
            point({ recorded_at: '2026-08-26T12:20:00Z', remaining: -1, limit: -1 }),
        ]);
        expect(summary.latestRemaining).toBe(4000);
        expect(summary.latestLimit).toBe(5000);
        expect(summary.remainingFraction).toBeCloseTo(0.8);
        expect(summary.latestResetAt).toBe('x');
    });

    test('a window with no readings at all leaves the budget unknown', () => {
        const summary = summarizeWindow([point({ remaining: -1, limit: -1, total: 5 })]);
        expect(summary.latestRemaining).toBeNull();
        expect(summary.remainingFraction).toBeNull();
        expect(summary.totalCalls).toBe(5);
    });
});

describe('callTypeBreakdown', () => {
    test('sums each type, drops unused ones and sorts by size', () => {
        const entries = callTypeBreakdown([
            point({ diff: 5, pr_list: 1, review_threads: 3, total: 9 }),
            point({ diff: 5, pr_list: 1, review_threads: 0, total: 6 }),
        ]);
        expect(entries.map(e => e.key)).toEqual(['diff', 'review_threads', 'pr_list']);
        expect(entries[0].total).toBe(10);
        expect(entries[0].share).toBeCloseTo(10 / 15);
        expect(entries.some(e => e.key === 'commits')).toBe(false);
    });

    test('an empty window produces no rows', () => {
        expect(callTypeBreakdown([])).toEqual([]);
    });
});

describe('budgetSegments', () => {
    test('cycles without a reading break the line into separate runs', () => {
        const segments = budgetSegments([
            point({ recorded_at: '2026-08-26T12:00:00Z', remaining: 4900 }),
            point({ recorded_at: '2026-08-26T12:10:00Z', remaining: -1, limit: -1 }),
            point({ recorded_at: '2026-08-26T12:20:00Z', remaining: 4700 }),
            point({ recorded_at: '2026-08-26T12:30:00Z', remaining: 4600 }),
        ]);
        expect(segments.length).toBe(2);
        expect(segments[0].length).toBe(1);
        expect(segments[1].length).toBe(2);
    });

    test('an unbroken series is one segment', () => {
        expect(budgetSegments([point(), point()]).length).toBe(1);
    });

    test('a series with no readings has no segments', () => {
        expect(budgetSegments([point({ remaining: -1, limit: -1 })])).toEqual([]);
    });
});

describe('scales', () => {
    test('time maps across the plot width', () => {
        const min = Date.parse('2026-08-26T12:00:00Z');
        const max = Date.parse('2026-08-26T13:00:00Z');
        expect(xForTime(min, min, max, BOX)).toBeCloseTo(BOX.padLeft);
        expect(xForTime(max, min, max, BOX)).toBeCloseTo(BOX.width - BOX.padRight);
        expect(xForTime((min + max) / 2, min, max, BOX)).toBeCloseTo(
            BOX.padLeft + (BOX.width - BOX.padLeft - BOX.padRight) / 2
        );
    });

    test('a degenerate time range centres instead of dividing by zero', () => {
        const t = Date.parse('2026-08-26T12:00:00Z');
        const x = xForTime(t, t, t, BOX);
        expect(Number.isFinite(x)).toBe(true);
        expect(x).toBeCloseTo(BOX.padLeft + (BOX.width - BOX.padLeft - BOX.padRight) / 2);
    });

    test('values map with y growing downward, and clamp to the range', () => {
        const bottom = yForValue(0, 0, 100, BOX);
        const top = yForValue(100, 0, 100, BOX);
        expect(top).toBeLessThan(bottom);
        expect(bottom).toBeCloseTo(BOX.height - BOX.padBottom);
        expect(top).toBeCloseTo(BOX.padTop);
        expect(yForValue(500, 0, 100, BOX)).toBeCloseTo(top);
        expect(yForValue(-5, 0, 100, BOX)).toBeCloseTo(bottom);
    });
});

describe('niceTicks', () => {
    test('covers the maximum and starts at zero', () => {
        const ticks = niceTicks(4870);
        expect(ticks[0]).toBe(0);
        expect(ticks[ticks.length - 1]).toBeGreaterThanOrEqual(4870);
    });

    test('degenerate maxima still yield an axis', () => {
        expect(niceTicks(0)).toEqual([0, 1]);
        expect(niceTicks(Number.NaN)).toEqual([0, 1]);
        expect(niceTicks(-5)).toEqual([0, 1]);
    });

    test('ticks are evenly spaced and ascending', () => {
        const ticks = niceTicks(100);
        const step = ticks[1] - ticks[0];
        for (let i = 1; i < ticks.length; i++) {
            expect(ticks[i] - ticks[i - 1]).toBeCloseTo(step);
        }
    });
});

describe('barWidthFor', () => {
    test('regular cycles get a positive width that fits the plot', () => {
        const points = [
            point({ recorded_at: '2026-08-26T12:00:00Z' }),
            point({ recorded_at: '2026-08-26T12:10:00Z' }),
            point({ recorded_at: '2026-08-26T12:20:00Z' }),
        ];
        const width = barWidthFor(points, BOX);
        expect(width).toBeGreaterThan(0);
        expect(width).toBeLessThanOrEqual(32);
    });

    test('a single point still gets a drawable width', () => {
        expect(barWidthFor([point()], BOX)).toBeGreaterThan(0);
    });

    test('points sharing one timestamp do not produce NaN', () => {
        const width = barWidthFor([point(), point()], BOX);
        expect(Number.isFinite(width)).toBe(true);
        expect(width).toBeGreaterThan(0);
    });
});

describe('formatting', () => {
    test('counts abbreviate above a thousand', () => {
        expect(formatCount(4980)).toBe('5.0k');
        expect(formatCount(12)).toBe('12');
        expect(formatCount(1.25)).toBe('1.25');
        expect(formatCount(Number.NaN)).toBe('—');
    });

    test('reset countdown reads in minutes and hours', () => {
        const now = Date.parse('2026-08-26T12:00:00Z');
        expect(formatResetIn('2026-08-26T12:24:00Z', now)).toBe('in 24m');
        expect(formatResetIn('2026-08-26T13:30:00Z', now)).toBe('in 1h 30m');
        expect(formatResetIn('2026-08-26T11:00:00Z', now)).toBe('now');
        expect(formatResetIn('', now)).toBe('');
    });
});
