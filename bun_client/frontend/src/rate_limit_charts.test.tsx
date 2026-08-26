import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { RateLimitDashboard } from './components/RateLimitHistory';
import type { RateLimitHistoryPoint } from './api';

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

/** A window shaped like a real one: a budget drawdown, then the hourly reset. */
const SERIES: RateLimitHistoryPoint[] = [
    point({
        recorded_at: '2026-08-26T12:00:00Z',
        pr_list: 4,
        diff: 20,
        total: 24,
        remaining: 4900,
    }),
    point({
        recorded_at: '2026-08-26T12:10:00Z',
        pr_list: 4,
        diff: 30,
        review_threads: 6,
        total: 40,
        remaining: 4860,
        gap_minutes: 10,
        calls_per_minute: 4,
    }),
    point({
        recorded_at: '2026-08-26T12:20:00Z',
        pr_list: 4,
        total: 4,
        remaining: 4856,
        gap_minutes: 10,
        calls_per_minute: 0.4,
    }),
    // GitHub's hourly reset puts the budget back.
    point({
        recorded_at: '2026-08-26T13:00:00Z',
        pr_list: 4,
        diff: 12,
        total: 16,
        remaining: 4984,
        gap_minutes: 40,
        calls_per_minute: 0.4,
    }),
];

const render = (points: RateLimitHistoryPoint[]) =>
    renderToStaticMarkup(<RateLimitDashboard points={points} />);

/** SVG coordinates must always be finite; a NaN silently blanks the chart. */
function expectNoBadCoordinates(html: string) {
    expect(html).not.toContain('NaN');
    expect(html).not.toContain('Infinity');
    expect(html).not.toContain('undefined');
}

describe('RateLimitDashboard', () => {
    test('draws both charts from a realistic window', () => {
        const html = render(SERIES);
        expectNoBadCoordinates(html);
        expect(html).toContain('Rate limit remaining');
        expect(html).toContain('API calls per cycle');
        // One line for the budget series, one bar per cycle.
        expect(html).toContain('<polyline');
        expect((html.match(/<rect/g) ?? []).length).toBeGreaterThanOrEqual(SERIES.length);
    });

    test('summarises the window in the tiles', () => {
        const html = render(SERIES);
        // 24 + 40 + 4 + 16 calls across four cycles.
        expect(html).toContain('84');
        expect(html).toContain('4,984 / 5,000');
        expect(html).toContain('over 4 cycles');
    });

    test('breaks the spend down by call type, largest first', () => {
        const html = render(SERIES);
        const diffAt = html.indexOf('Diff');
        const prListAt = html.indexOf('PR list');
        expect(diffAt).toBeGreaterThan(-1);
        expect(prListAt).toBeGreaterThan(-1);
        // 62 diff calls outrank 16 PR-list calls.
        expect(diffAt).toBeLessThan(prListAt);
        // Never-called types stay out of the list.
        expect(html).not.toContain('Check runs');
    });

    test('a cycle with no budget reading leaves a gap rather than a drop to zero', () => {
        const withGap = [
            SERIES[0],
            point({ recorded_at: '2026-08-26T12:10:00Z', total: 10, remaining: -1, limit: -1 }),
            point({ recorded_at: '2026-08-26T12:20:00Z', total: 10, remaining: 4800 }),
        ];
        const html = render(withGap);
        expectNoBadCoordinates(html);
        // Two separate runs of readings means two polylines, not one line
        // dipping through zero.
        expect((html.match(/<polyline/g) ?? []).length).toBe(2);
    });

    test('a window with no readings at all still renders the calls chart', () => {
        const html = render([
            point({ recorded_at: '2026-08-26T12:00:00Z', total: 10, remaining: -1, limit: -1 }),
            point({ recorded_at: '2026-08-26T12:10:00Z', total: 20, remaining: -1, limit: -1 }),
        ]);
        expectNoBadCoordinates(html);
        expect(html).toContain('No cycle in this window recorded a rate limit reading.');
        expect(html).toContain('API calls per cycle');
    });

    test('a single cycle does not divide by zero', () => {
        const html = render([point({ recorded_at: '2026-08-26T12:00:00Z', total: 7 })]);
        expectNoBadCoordinates(html);
        expect(html).toContain('over 1 cycle');
    });

    // The filled area is read as "fraction of budget left", which only holds if
    // full plot height means a full budget rather than a rounded number above it.
    test('the budget axis tops out at the limit, not above it', () => {
        const html = render(SERIES);
        // niceTicks(5000) would otherwise stretch the axis to 6.0k.
        expect(html).not.toContain('6.0k');
        // The topmost reading sits near the top of the plot, not five sixths up.
        const topOfPlot = 14; // CHART_BOX.padTop
        const cys = [...html.matchAll(/<circle[^>]*cy="([\d.]+)"/g)].map(m => Number(m[1]));
        expect(cys.length).toBeGreaterThan(0);
        expect(Math.min(...cys)).toBeLessThan(topOfPlot + 30);
    });

    test('a cycle that made no calls still renders a bar', () => {
        const html = render([
            point({ recorded_at: '2026-08-26T12:00:00Z', total: 0 }),
            point({ recorded_at: '2026-08-26T12:10:00Z', total: 0 }),
        ]);
        expectNoBadCoordinates(html);
        expect((html.match(/<rect/g) ?? []).length).toBeGreaterThanOrEqual(2);
    });
});
