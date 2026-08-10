import { colors } from '../../design';

interface ThreadStateBadgesProps {
    resolved: boolean;
    resolvedBy?: string;
    outdated: boolean;
    /** Number of replies under the root; omitted or 0 hides the badge. */
    replyCount?: number;
}

const badgeStyle = (fg: string, bg: string, border: string) => ({
    fontSize: '10px',
    fontWeight: 600,
    padding: '1px 6px',
    borderRadius: '9px',
    whiteSpace: 'nowrap' as const,
    color: fg,
    background: bg,
    border: `1px solid ${border}`,
});

// The two facts you need about a review thread at a glance: has the
// conversation been resolved, and does it still point at live code.
// Resolution comes from GitHub's GraphQL API; when the server could not fetch
// it, `resolved` is false and no badge shows — absence means "unknown", so this
// deliberately never renders an "unresolved" badge.
export default function ThreadStateBadges({
    resolved,
    resolvedBy,
    outdated,
    replyCount = 0,
}: ThreadStateBadgesProps) {
    if (!resolved && !outdated && replyCount <= 0) return null;

    return (
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}>
            {replyCount > 0 && (
                <span
                    style={badgeStyle(
                        'var(--text-secondary)',
                        'var(--bg-tertiary)',
                        'var(--border)'
                    )}
                    title={`${replyCount} ${replyCount === 1 ? 'reply' : 'replies'} in this thread`}
                >
                    ↩ {replyCount}
                </span>
            )}
            {resolved && (
                <span
                    style={badgeStyle(colors.success, colors.bgSuccessDim, colors.borderSuccessDim)}
                    title={
                        resolvedBy
                            ? `Conversation resolved by ${resolvedBy}`
                            : 'Conversation resolved'
                    }
                >
                    ✓ Resolved
                </span>
            )}
            {outdated && (
                <span
                    style={badgeStyle(colors.warning, colors.bgWarningDim, colors.borderWarningDim)}
                    title="These lines are no longer in the current diff"
                >
                    ⧗ Outdated
                </span>
            )}
        </span>
    );
}
