import { colors } from '../../design';

interface MobileReviewBarProps {
    loading: boolean;
    onAction: (event: 'APPROVE' | 'REQUEST_CHANGES' | 'COMMENT') => void;
}

// Mobile sticky review bar — thumb-reachable Approve / Request Changes /
// Comment. Each opens the existing Submit Review modal pre-set to the chosen
// event so the whole flow is reused.
export default function MobileReviewBar({ loading, onAction }: MobileReviewBarProps) {
    return (
        <div
            style={{
                position: 'fixed',
                left: 0,
                right: 0,
                bottom: 0,
                zIndex: 50,
                display: 'flex',
                gap: '8px',
                padding: '8px',
                paddingBottom: 'calc(8px + env(safe-area-inset-bottom, 0px))',
                background: 'var(--bg-secondary)',
                borderTop: '1px solid var(--border)',
                boxShadow: '0 -2px 8px rgba(0, 0, 0, 0.15)',
            }}
        >
            {(
                [
                    { event: 'APPROVE', label: '✓ Approve', bg: colors.success },
                    {
                        event: 'REQUEST_CHANGES',
                        label: '✗ Request',
                        bg: colors.danger,
                    },
                    { event: 'COMMENT', label: '💬 Comment', bg: 'var(--accent)' },
                ] as const
            ).map(action => (
                <button
                    key={action.event}
                    onClick={() => onAction(action.event)}
                    disabled={loading}
                    style={{
                        flex: 1,
                        minHeight: '48px',
                        padding: '10px 8px',
                        background: action.bg,
                        color: 'white',
                        border: 'none',
                        borderRadius: '8px',
                        fontSize: '14px',
                        fontWeight: 600,
                        cursor: loading ? 'not-allowed' : 'pointer',
                        opacity: loading ? 0.5 : 1,
                    }}
                >
                    {action.label}
                </button>
            ))}
        </div>
    );
}
