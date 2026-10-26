import { Button } from '../../design';
import { filteredDiffSummary, type FilteredDiffResult } from '../../diff_filter_utils';

interface FilteredDiffBannerProps {
    result: FilteredDiffResult;
    filterName: string;
    onShowFullDiff: () => void;
}

const VARIANT_COLORS = {
    info: {
        color: 'var(--accent)',
        background: 'var(--bg-info-dim)',
        border: 'var(--border-info-dim)',
    },
    warning: {
        color: 'var(--warning)',
        background: 'var(--bg-warning-dim)',
        border: 'var(--warning)',
    },
    danger: { color: 'var(--danger)', background: 'var(--bg-danger-dim)', border: 'var(--danger)' },
} as const;

// Says which range of commits the diff below shows, or why it still shows the
// whole PR, with a way back to the full diff.
export default function FilteredDiffBanner({
    result,
    filterName,
    onShowFullDiff,
}: FilteredDiffBannerProps) {
    const { text, variant } = filteredDiffSummary(result, filterName);
    const palette = VARIANT_COLORS[variant];
    return (
        <div
            aria-live="polite"
            data-testid="filtered-diff-banner"
            style={{
                display: 'flex',
                alignItems: 'center',
                gap: '12px',
                padding: '8px 16px',
                borderBottom: '1px solid var(--border)',
                background: palette.background,
                borderLeft: `3px solid ${palette.border}`,
                fontSize: '13px',
                color: 'var(--text-primary)',
            }}
        >
            <span style={{ color: palette.color }}>◷</span>
            <span style={{ flex: 1, minWidth: 0 }}>{text}</span>
            <Button size="sm" variant="secondary" onClick={onShowFullDiff}>
                Show full diff
            </Button>
        </div>
    );
}
