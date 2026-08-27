import { useCallback, useEffect, useMemo, useState } from 'react';
import { getRateLimitHistory } from '../api';
import type { RateLimitHistoryPoint } from '../api';
import {
    barWidthFor,
    budgetSegments,
    callTypeBreakdown,
    formatClock,
    formatCount,
    formatResetIn,
    hasBudget,
    niceTicks,
    summarizeWindow,
    xForTime,
    yForValue,
} from '../rate_limit_utils';
import type { PlotBox } from '../rate_limit_utils';
import { Button, Select, colors, spacing, borderRadius, fontSize } from '../design';

/**
 * GitHub rate limit history: what each workflow cycle spent, and what was left
 * of the hourly budget when it finished.
 *
 * The server records one row per completed cycle, so the resolution here is the
 * configured sleep duration — this is not a live sampler, and a window shorter
 * than one cycle can legitimately be empty.
 */

const WINDOW_OPTIONS = [
    { value: '1', label: 'Last hour' },
    { value: '3', label: 'Last 3 hours' },
    { value: '6', label: 'Last 6 hours' },
    { value: '12', label: 'Last 12 hours' },
    { value: '24', label: 'Last 24 hours' },
    { value: '168', label: 'Last 7 days' },
];

const DEFAULT_WINDOW = '3';

const CHART_BOX: PlotBox = {
    width: 720,
    height: 220,
    padLeft: 52,
    padRight: 12,
    padTop: 14,
    padBottom: 28,
};

export default function RateLimitHistory() {
    const [hours, setHours] = useState(DEFAULT_WINDOW);
    const [points, setPoints] = useState<RateLimitHistoryPoint[]>([]);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [fetchedAt, setFetchedAt] = useState(0);

    const load = useCallback(async (windowHours: string) => {
        setLoading(true);
        setError('');
        try {
            const reply = await getRateLimitHistory(Number(windowHours));
            setPoints(reply.points ?? []);
            setFetchedAt(Date.now());
        } catch (e: unknown) {
            setError(e instanceof Error ? e.message : String(e));
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        load(hours);
    }, [load, hours]);

    return (
        <div
            style={{ display: 'flex', flexDirection: 'column', gap: spacing.lg, padding: '4px 0' }}
        >
            <div
                style={{
                    display: 'flex',
                    alignItems: 'flex-end',
                    gap: spacing.md,
                    flexWrap: 'wrap',
                }}
            >
                <Select
                    label="Time range"
                    value={hours}
                    options={WINDOW_OPTIONS}
                    onChange={e => setHours(e.target.value)}
                />
                <Button variant="secondary" onClick={() => load(hours)} disabled={loading}>
                    {loading ? 'Loading…' : 'Refresh'}
                </Button>
                {fetchedAt > 0 && !loading && (
                    <span style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                        Updated {formatClock(new Date(fetchedAt).toISOString())}
                    </span>
                )}
            </div>

            {error && (
                <div
                    style={{
                        background: colors.bgDangerDim,
                        border: `1px solid ${colors.borderDangerDim}`,
                        color: colors.textDanger,
                        borderRadius: borderRadius.md,
                        padding: spacing.md,
                        fontSize: fontSize.sm,
                    }}
                >
                    Could not load rate limit history: {error}
                </div>
            )}

            {!error && points.length === 0 && !loading && <EmptyState />}

            {points.length > 0 && <RateLimitDashboard points={points} />}
        </div>
    );
}

/**
 * The charts themselves, split from the fetching above so they can be rendered
 * from seeded data in tests.
 */
export function RateLimitDashboard({ points }: { points: RateLimitHistoryPoint[] }) {
    const summary = useMemo(() => summarizeWindow(points), [points]);
    const breakdown = useMemo(() => callTypeBreakdown(points), [points]);

    return (
        <>
            <SummaryTiles summary={summary} />
            <ChartPanel
                title="Rate limit remaining"
                subtitle="Budget left after each workflow cycle. The sawtooth is GitHub's hourly reset."
            >
                <RemainingChart points={points} />
            </ChartPanel>
            <ChartPanel
                title="API calls per cycle"
                subtitle="GitHub calls each cycle made. Bar spacing follows real time, so a paused server leaves a gap."
            >
                <CallsChart points={points} />
            </ChartPanel>
            <Breakdown entries={breakdown} total={summary.totalCalls} />
        </>
    );
}

function EmptyState() {
    return (
        <div
            style={{
                border: `1px dashed ${colors.border}`,
                borderRadius: borderRadius.lg,
                padding: spacing['2xl'],
                textAlign: 'center',
                color: colors.textSecondary,
                fontSize: fontSize.sm,
                lineHeight: 1.6,
            }}
        >
            <div style={{ fontSize: '22px', marginBottom: spacing.sm }}>📉</div>
            No workflow cycles recorded in this window.
            <div style={{ color: colors.textTertiary, marginTop: spacing.xs }}>
                One point is written per completed cycle, so a window shorter than the configured
                sleep duration can be empty. Try a longer range.
            </div>
        </div>
    );
}

function ChartPanel({
    title,
    subtitle,
    children,
}: {
    title: string;
    subtitle: string;
    children: React.ReactNode;
}) {
    return (
        <section
            style={{
                border: `1px solid ${colors.border}`,
                borderRadius: borderRadius.lg,
                background: colors.bgSecondary,
                padding: spacing.lg,
            }}
        >
            <h3
                style={{
                    margin: 0,
                    fontSize: fontSize.base,
                    fontWeight: 600,
                    color: colors.textPrimary,
                }}
            >
                {title}
            </h3>
            <p
                style={{
                    margin: `${spacing.xs} 0 ${spacing.md}`,
                    fontSize: fontSize.sm,
                    color: colors.textTertiary,
                }}
            >
                {subtitle}
            </p>
            {children}
        </section>
    );
}

function SummaryTiles({ summary }: { summary: ReturnType<typeof summarizeWindow> }) {
    const pct =
        summary.remainingFraction === null ? null : Math.round(summary.remainingFraction * 100);
    // Below a quarter of the budget the throttling in git_tools starts to bite,
    // so the tile earns a warning colour well before the budget is gone.
    const budgetColor =
        pct === null
            ? colors.textPrimary
            : pct < 15
              ? colors.textDanger
              : pct < 25
                ? colors.textWarning
                : colors.textSuccess;

    const tiles: Array<{ label: string; value: string; hint?: string; color?: string }> = [
        {
            label: 'Budget remaining',
            value:
                summary.latestRemaining === null
                    ? '—'
                    : `${summary.latestRemaining.toLocaleString()} / ${(summary.latestLimit ?? 0).toLocaleString()}`,
            hint:
                pct === null
                    ? 'no reading recorded'
                    : `${pct}% left${summary.latestResetAt ? ` · resets ${formatResetIn(summary.latestResetAt)}` : ''}`,
            color: budgetColor,
        },
        {
            label: 'Calls in window',
            value: summary.totalCalls.toLocaleString(),
            hint: `over ${summary.cycles} cycle${summary.cycles === 1 ? '' : 's'}`,
        },
        {
            label: 'Calls per minute',
            value: formatCount(Math.round(summary.callsPerMinute * 100) / 100),
            hint: 'averaged across the window',
        },
        {
            label: 'Peak cycle',
            value: summary.peakCallsPerCycle.toLocaleString(),
            hint: `avg ${formatCount(Math.round(summary.avgCallsPerCycle * 10) / 10)} per cycle`,
        },
    ];

    return (
        <div
            style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))',
                gap: spacing.md,
            }}
        >
            {tiles.map(tile => (
                <div
                    key={tile.label}
                    style={{
                        border: `1px solid ${colors.border}`,
                        borderRadius: borderRadius.lg,
                        background: colors.bgSecondary,
                        padding: spacing.md,
                    }}
                >
                    <div
                        style={{
                            fontSize: fontSize.xs,
                            textTransform: 'uppercase',
                            letterSpacing: '0.04em',
                            color: colors.textTertiary,
                        }}
                    >
                        {tile.label}
                    </div>
                    <div
                        style={{
                            fontSize: '20px',
                            fontWeight: 600,
                            marginTop: spacing.xs,
                            color: tile.color ?? colors.textPrimary,
                        }}
                    >
                        {tile.value}
                    </div>
                    {tile.hint && (
                        <div
                            style={{
                                fontSize: fontSize.xs,
                                color: colors.textTertiary,
                                marginTop: '2px',
                            }}
                        >
                            {tile.hint}
                        </div>
                    )}
                </div>
            ))}
        </div>
    );
}

/** Shared axis furniture: horizontal gridlines with their value labels. */
function YAxis({ ticks, max, box }: { ticks: number[]; max: number; box: PlotBox }) {
    return (
        <g>
            {ticks.map(tick => {
                const y = yForValue(tick, 0, max, box);
                return (
                    <g key={tick}>
                        <line
                            x1={box.padLeft}
                            x2={box.width - box.padRight}
                            y1={y}
                            y2={y}
                            stroke={colors.border}
                            strokeWidth={1}
                            strokeDasharray={tick === 0 ? undefined : '3 3'}
                        />
                        <text
                            x={box.padLeft - 8}
                            y={y + 4}
                            textAnchor="end"
                            fontSize={11}
                            fill={colors.textTertiary}
                        >
                            {formatCount(tick)}
                        </text>
                    </g>
                );
            })}
        </g>
    );
}

/** Time labels along the bottom: first, middle and last cycle in the window. */
function XAxisLabels({
    points,
    minT,
    maxT,
    box,
}: {
    points: RateLimitHistoryPoint[];
    minT: number;
    maxT: number;
    box: PlotBox;
}) {
    const indices =
        points.length <= 2
            ? points.map((_, i) => i)
            : [0, Math.floor((points.length - 1) / 2), points.length - 1];
    return (
        <g>
            {indices.map(i => {
                const point = points[i];
                const x = xForTime(Date.parse(point.recorded_at), minT, maxT, box);
                return (
                    <text
                        key={point.recorded_at + i}
                        x={x}
                        y={box.height - 8}
                        textAnchor={i === 0 ? 'start' : i === points.length - 1 ? 'end' : 'middle'}
                        fontSize={11}
                        fill={colors.textTertiary}
                    >
                        {formatClock(point.recorded_at)}
                    </text>
                );
            })}
        </g>
    );
}

function timeExtent(points: RateLimitHistoryPoint[]): [number, number] {
    const times = points.map(p => Date.parse(p.recorded_at)).filter(t => Number.isFinite(t));
    if (times.length === 0) return [0, 0];
    return [Math.min(...times), Math.max(...times)];
}

function RemainingChart({ points }: { points: RateLimitHistoryPoint[] }) {
    const box = CHART_BOX;
    const [minT, maxT] = timeExtent(points);
    const segments = budgetSegments(points);
    const readings = points.filter(hasBudget);

    if (readings.length === 0) {
        return (
            <div style={{ fontSize: fontSize.sm, color: colors.textTertiary }}>
                No cycle in this window recorded a rate limit reading.
            </div>
        );
    }

    // The axis tops out at the budget ceiling itself, not at a rounded number
    // above it: the filled area is meant to read as the fraction of budget
    // left, which only works when full height means a full budget. Gridlines
    // above the ceiling are dropped rather than stretching the axis to reach
    // them.
    const axisMax = Math.max(...readings.map(p => p.limit));
    const ticks = niceTicks(axisMax).filter(tick => tick <= axisMax);

    return (
        <svg
            viewBox={`0 0 ${box.width} ${box.height}`}
            width="100%"
            role="img"
            aria-label="GitHub rate limit remaining over time"
            style={{ display: 'block', overflow: 'visible' }}
        >
            <YAxis ticks={ticks} max={axisMax} box={box} />
            {segments.map(segment => {
                const line = segment
                    .map(
                        p =>
                            `${xForTime(Date.parse(p.recorded_at), minT, maxT, box)},${yForValue(
                                p.remaining,
                                0,
                                axisMax,
                                box
                            )}`
                    )
                    .join(' ');
                const first = xForTime(Date.parse(segment[0].recorded_at), minT, maxT, box);
                const last = xForTime(
                    Date.parse(segment[segment.length - 1].recorded_at),
                    minT,
                    maxT,
                    box
                );
                const baseline = yForValue(0, 0, axisMax, box);
                return (
                    <g key={segment[0].recorded_at}>
                        <polygon
                            points={`${first},${baseline} ${line} ${last},${baseline}`}
                            fill={colors.accent}
                            opacity={0.14}
                        />
                        <polyline
                            points={line}
                            fill="none"
                            stroke={colors.accent}
                            strokeWidth={2}
                            strokeLinejoin="round"
                            strokeLinecap="round"
                        />
                    </g>
                );
            })}
            {readings.map(p => (
                <circle
                    key={p.recorded_at}
                    cx={xForTime(Date.parse(p.recorded_at), minT, maxT, box)}
                    cy={yForValue(p.remaining, 0, axisMax, box)}
                    r={2.5}
                    fill={colors.accent}
                >
                    <title>
                        {`${formatClock(p.recorded_at)} — ${p.remaining.toLocaleString()} of ${p.limit.toLocaleString()} left`}
                    </title>
                </circle>
            ))}
            <XAxisLabels points={points} minT={minT} maxT={maxT} box={box} />
        </svg>
    );
}

function CallsChart({ points }: { points: RateLimitHistoryPoint[] }) {
    const box = CHART_BOX;
    const [minT, maxT] = timeExtent(points);
    const max = Math.max(...points.map(p => p.total), 1);
    const ticks = niceTicks(max);
    const axisMax = ticks[ticks.length - 1];
    const barWidth = barWidthFor(points, box);
    const baseline = yForValue(0, 0, axisMax, box);

    return (
        <svg
            viewBox={`0 0 ${box.width} ${box.height}`}
            width="100%"
            role="img"
            aria-label="GitHub API calls per workflow cycle"
            style={{ display: 'block', overflow: 'visible' }}
        >
            <YAxis ticks={ticks} max={axisMax} box={box} />
            {points.map(p => {
                const x = xForTime(Date.parse(p.recorded_at), minT, maxT, box);
                const y = yForValue(p.total, 0, axisMax, box);
                return (
                    <rect
                        key={p.recorded_at}
                        x={x - barWidth / 2}
                        y={y}
                        width={barWidth}
                        // A cycle that made no calls still gets a hairline, so
                        // it reads as a recorded zero rather than a missing bar.
                        height={Math.max(baseline - y, 1)}
                        rx={1.5}
                        fill={colors.accent}
                        opacity={0.75}
                    >
                        <title>
                            {`${formatClock(p.recorded_at)} — ${p.total.toLocaleString()} calls${
                                p.calls_per_minute > 0
                                    ? ` (${formatCount(Math.round(p.calls_per_minute * 100) / 100)}/min)`
                                    : ''
                            }`}
                        </title>
                    </rect>
                );
            })}
            <XAxisLabels points={points} minT={minT} maxT={maxT} box={box} />
        </svg>
    );
}

function Breakdown({
    entries,
    total,
}: {
    entries: ReturnType<typeof callTypeBreakdown>;
    total: number;
}) {
    if (entries.length === 0) return null;
    return (
        <section
            style={{
                border: `1px solid ${colors.border}`,
                borderRadius: borderRadius.lg,
                background: colors.bgSecondary,
                padding: spacing.lg,
            }}
        >
            <h3
                style={{
                    margin: `0 0 ${spacing.md}`,
                    fontSize: fontSize.base,
                    fontWeight: 600,
                    color: colors.textPrimary,
                }}
            >
                Where the calls went
            </h3>
            <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.sm }}>
                {entries.map(entry => (
                    <div
                        key={entry.key}
                        style={{ display: 'flex', alignItems: 'center', gap: spacing.md }}
                    >
                        <div
                            style={{
                                width: '140px',
                                flexShrink: 0,
                                fontSize: fontSize.sm,
                                color: colors.textSecondary,
                            }}
                        >
                            {entry.label}
                        </div>
                        <div
                            style={{
                                flex: 1,
                                height: '8px',
                                background: colors.bgTertiary,
                                borderRadius: borderRadius.pill,
                                overflow: 'hidden',
                            }}
                        >
                            <div
                                style={{
                                    width: `${Math.max(entry.share * 100, 1)}%`,
                                    height: '100%',
                                    background: colors.accent,
                                    opacity: 0.75,
                                }}
                            />
                        </div>
                        <div
                            style={{
                                width: '110px',
                                flexShrink: 0,
                                textAlign: 'right',
                                fontSize: fontSize.sm,
                                color: colors.textPrimary,
                                fontVariantNumeric: 'tabular-nums',
                            }}
                        >
                            {entry.total.toLocaleString()}{' '}
                            <span style={{ color: colors.textTertiary }}>
                                ({Math.round(entry.share * 100)}%)
                            </span>
                        </div>
                    </div>
                ))}
            </div>
            <div
                style={{
                    marginTop: spacing.md,
                    paddingTop: spacing.sm,
                    borderTop: `1px solid ${colors.border}`,
                    fontSize: fontSize.sm,
                    color: colors.textTertiary,
                }}
            >
                {total.toLocaleString()} calls total
            </div>
        </section>
    );
}
