import { colors } from '../../design';
import GitHubMarkdown from '../GitHubMarkdown';
import CopyUrlButton from './CopyUrlButton';
import { type PRMetadata, stripHtmlComments } from './types';

interface PRHeaderProps {
    metadata: PRMetadata;
    descCollapsed: boolean;
    onToggleDescCollapsed: () => void;
}

// The PR header card: title bar, info grid (branch/author/CI/reviewers),
// labels row, collapsible description, and CI failures.
export default function PRHeader({
    metadata,
    descCollapsed,
    onToggleDescCollapsed,
}: PRHeaderProps) {
    const getCIStatusStyle = () => {
        if (!metadata?.ci_status) return {};
        const status = metadata.ci_status.toLowerCase();
        if (status === 'success' || status === 'passed') {
            return {
                background: colors.bgSuccessDim,
                color: colors.textSuccess,
                borderColor: colors.borderSuccessDim,
            };
        } else if (status === 'pending' || status === 'running') {
            return {
                background: colors.bgWarningDim,
                color: colors.textWarning,
                borderColor: colors.borderWarningDim,
            };
        } else if (status === 'failure' || status === 'failed') {
            return {
                background: colors.bgDangerDim,
                color: colors.textDanger,
                borderColor: colors.borderDangerDim,
            };
        }
        return {
            background: colors.bgTertiary,
            color: colors.textSecondary,
            borderColor: colors.border,
        };
    };

    const getStateStyle = () => {
        if (!metadata?.state) return {};
        const state = metadata.state.toLowerCase();
        if (state === 'open') {
            return { background: colors.bgSuccessDim, color: colors.textSuccess };
        } else if (state === 'closed') {
            return { background: colors.bgDangerDim, color: colors.textDanger };
        } else if (state === 'merged') {
            return { background: colors.bgMergedDim, color: colors.textMerged };
        }
        return {};
    };

    return (
        <div
            className="pr-header"
            style={{
                background:
                    'linear-gradient(135deg, var(--bg-secondary) 0%, var(--bg-primary) 100%)',
                borderRadius: '12px',
                border: '1px solid var(--border)',
                marginBottom: '16px',
                overflow: 'hidden',
            }}
        >
            {/* Title Bar */}
            <div
                style={{
                    padding: '20px 24px',
                    borderBottom: '1px solid var(--border)',
                    background: 'var(--bg-secondary)',
                }}
            >
                <div style={{ display: 'flex', alignItems: 'flex-start', gap: '16px' }}>
                    <div style={{ flex: 1 }}>
                        <div
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '12px',
                                marginBottom: '8px',
                                flexWrap: 'wrap',
                            }}
                        >
                            <span
                                style={{
                                    ...getStateStyle(),
                                    padding: '4px 12px',
                                    borderRadius: '16px',
                                    fontSize: '12px',
                                    fontWeight: 600,
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.5px',
                                }}
                            >
                                {metadata.draft ? '📝 Draft' : metadata.state}
                            </span>
                            <span style={{ color: 'var(--text-secondary)', fontSize: '14px' }}>
                                #{metadata.number}
                            </span>
                        </div>
                        <h1
                            style={{
                                margin: 0,
                                fontSize: '22px',
                                fontWeight: 600,
                                color: 'var(--text-primary)',
                                lineHeight: 1.3,
                            }}
                        >
                            {metadata.title}
                        </h1>
                    </div>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
                        <a
                            href={metadata.url}
                            target="_blank"
                            rel="noopener noreferrer"
                            style={{
                                background: 'transparent',
                                color: 'var(--text-secondary)',
                                border: '1px solid var(--border)',
                                padding: '8px 16px',
                                borderRadius: '6px',
                                cursor: 'pointer',
                                display: 'flex',
                                alignItems: 'center',
                                gap: '6px',
                                textDecoration: 'none',
                                fontSize: '13px',
                            }}
                        >
                            <span>↗</span> GitHub
                        </a>
                        <CopyUrlButton url={metadata.url} />
                    </div>
                </div>
            </div>

            {/* Info Grid */}
            <div
                style={{
                    display: 'grid',
                    gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
                    gap: '1px',
                    background: 'var(--border)',
                }}
            >
                {/* Branch Info */}
                <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                    <div
                        style={{
                            fontSize: '11px',
                            color: 'var(--text-secondary)',
                            marginBottom: '6px',
                            textTransform: 'uppercase',
                            letterSpacing: '0.5px',
                        }}
                    >
                        Branch
                    </div>
                    <div
                        style={{
                            fontFamily: 'var(--font-mono)',
                            fontSize: '13px',
                            color: 'var(--accent)',
                        }}
                    >
                        <span style={{ color: 'var(--text-secondary)' }}>{metadata.base_ref}</span>
                        <span style={{ margin: '0 8px', color: 'var(--text-tertiary)' }}>←</span>
                        <span>{metadata.head_ref}</span>
                    </div>
                </div>

                {/* Author */}
                <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                    <div
                        style={{
                            fontSize: '11px',
                            color: 'var(--text-secondary)',
                            marginBottom: '6px',
                            textTransform: 'uppercase',
                            letterSpacing: '0.5px',
                        }}
                    >
                        Author
                    </div>
                    <div
                        style={{
                            fontSize: '14px',
                            fontWeight: 500,
                            color: 'var(--text-primary)',
                        }}
                    >
                        @{metadata.author}
                    </div>
                </div>

                {/* CI Status */}
                {metadata.ci_status && (
                    <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--text-secondary)',
                                marginBottom: '6px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            CI Status
                        </div>
                        <div
                            style={{
                                display: 'inline-flex',
                                alignItems: 'center',
                                gap: '6px',
                                padding: '4px 10px',
                                borderRadius: '6px',
                                fontSize: '13px',
                                fontWeight: 500,
                                border: '1px solid',
                                ...getCIStatusStyle(),
                            }}
                        >
                            {metadata.ci_status.toLowerCase() === 'success' && '✓'}
                            {metadata.ci_status.toLowerCase() === 'pending' && '○'}
                            {metadata.ci_status.toLowerCase() === 'failure' && '✗'}
                            {metadata.ci_status}
                        </div>
                    </div>
                )}

                {/* Reviewers/Teams */}
                {((metadata.reviewers && metadata.reviewers.length > 0) ||
                    (metadata.requested_teams && metadata.requested_teams.length > 0)) && (
                    <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--text-secondary)',
                                marginBottom: '6px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            Requested Reviewers
                        </div>
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                            {metadata.reviewers?.map((r, i) => (
                                <span
                                    key={`rev-${i}`}
                                    style={{
                                        background: 'var(--bg-tertiary)',
                                        padding: '2px 8px',
                                        borderRadius: '4px',
                                        fontSize: '12px',
                                        color: 'var(--text-primary)',
                                    }}
                                >
                                    @{r}
                                </span>
                            ))}
                            {metadata.requested_teams?.map((t, i) => (
                                <span
                                    key={`team-${i}`}
                                    style={{
                                        background: colors.bgInfoDim,
                                        padding: '2px 8px',
                                        borderRadius: '4px',
                                        fontSize: '12px',
                                        color: colors.accent,
                                        border: `1px solid ${colors.borderInfoDim}`,
                                    }}
                                >
                                    team:{t}
                                </span>
                            ))}
                        </div>
                    </div>
                )}

                {/* Approved By */}
                {metadata.approved_by && metadata.approved_by.length > 0 && (
                    <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--success)',
                                marginBottom: '6px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            ✓ Approved By
                        </div>
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                            {metadata.approved_by.map((r, i) => (
                                <span
                                    key={i}
                                    style={{
                                        background: colors.bgSuccessDim,
                                        padding: '2px 8px',
                                        borderRadius: '4px',
                                        fontSize: '12px',
                                        color: colors.success,
                                        border: `1px solid ${colors.borderSuccessDim}`,
                                    }}
                                >
                                    @{r}
                                </span>
                            ))}
                        </div>
                    </div>
                )}

                {/* Changes Requested By */}
                {metadata.changes_requested_by && metadata.changes_requested_by.length > 0 && (
                    <div
                        style={{
                            padding: '16px 20px',
                            background: 'var(--bg-primary)',
                        }}
                    >
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--danger)',
                                marginBottom: '6px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            ✗ Changes Requested By
                        </div>
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                            {metadata.changes_requested_by.map((r, i) => (
                                <span
                                    key={i}
                                    style={{
                                        background: colors.bgDangerDim,
                                        padding: '2px 8px',
                                        borderRadius: '4px',
                                        fontSize: '12px',
                                        color: colors.danger,
                                        border: `1px solid ${colors.borderDangerDim}`,
                                    }}
                                >
                                    @{r}
                                </span>
                            ))}
                        </div>
                    </div>
                )}

                {/* Commented By */}
                {metadata.commented_by && metadata.commented_by.length > 0 && (
                    <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--text-secondary)',
                                marginBottom: '6px',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                            }}
                        >
                            💬 Commented By
                        </div>
                        <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                            {metadata.commented_by.map((r, i) => (
                                <span
                                    key={i}
                                    style={{
                                        background: 'var(--bg-tertiary)',
                                        padding: '2px 8px',
                                        borderRadius: '4px',
                                        fontSize: '12px',
                                        color: 'var(--text-primary)',
                                    }}
                                >
                                    @{r}
                                </span>
                            ))}
                        </div>
                    </div>
                )}
            </div>

            {/* Labels, Assignees, Milestone Row */}
            {(metadata.labels?.length > 0 ||
                metadata.assignees?.length > 0 ||
                metadata.milestone) && (
                <div
                    style={{
                        padding: '14px 20px',
                        display: 'flex',
                        flexWrap: 'wrap',
                        gap: '20px',
                        borderTop: '1px solid var(--border)',
                        background: 'var(--bg-primary)',
                    }}
                >
                    {metadata.labels && metadata.labels.length > 0 && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                            <span
                                style={{
                                    fontSize: '11px',
                                    color: 'var(--text-secondary)',
                                    textTransform: 'uppercase',
                                }}
                            >
                                Labels:
                            </span>
                            <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
                                {metadata.labels.map((label, i) => (
                                    <span
                                        key={i}
                                        style={{
                                            background: colors.bgMergedDim,
                                            color: colors.textMerged,
                                            padding: '2px 10px',
                                            borderRadius: '12px',
                                            fontSize: '11px',
                                            fontWeight: 500,
                                            border: `1px solid ${colors.borderMergedDim}`,
                                        }}
                                    >
                                        {label}
                                    </span>
                                ))}
                            </div>
                        </div>
                    )}
                    {metadata.assignees && metadata.assignees.length > 0 && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                            <span
                                style={{
                                    fontSize: '11px',
                                    color: 'var(--text-secondary)',
                                    textTransform: 'uppercase',
                                }}
                            >
                                Assignees:
                            </span>
                            <span style={{ fontSize: '13px', color: 'var(--text-primary)' }}>
                                {metadata.assignees.map(a => `@${a}`).join(', ')}
                            </span>
                        </div>
                    )}
                    {metadata.milestone && (
                        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                            <span
                                style={{
                                    fontSize: '11px',
                                    color: 'var(--text-secondary)',
                                    textTransform: 'uppercase',
                                }}
                            >
                                Milestone:
                            </span>
                            <span style={{ fontSize: '13px', color: 'var(--accent)' }}>
                                {metadata.milestone}
                            </span>
                        </div>
                    )}
                </div>
            )}

            {/* Description (collapsible — diff is the focus) */}
            {metadata.body && (
                <div
                    style={{
                        borderTop: '1px solid var(--border)',
                        background: 'var(--bg-primary)',
                    }}
                >
                    <button
                        type="button"
                        onClick={onToggleDescCollapsed}
                        style={{
                            width: '100%',
                            background: 'transparent',
                            border: 'none',
                            padding: '10px 20px',
                            display: 'flex',
                            alignItems: 'center',
                            gap: '8px',
                            cursor: 'pointer',
                            color: 'var(--text-secondary)',
                            fontSize: '11px',
                            textTransform: 'uppercase',
                            letterSpacing: '0.5px',
                            textAlign: 'left',
                        }}
                        aria-expanded={!descCollapsed}
                    >
                        <span
                            style={{
                                display: 'inline-block',
                                transform: descCollapsed ? 'rotate(-90deg)' : 'rotate(0deg)',
                                transition: 'transform 0.15s ease',
                                fontSize: '10px',
                            }}
                        >
                            ▼
                        </span>
                        Description
                        {descCollapsed && (
                            <span
                                style={{
                                    marginLeft: '8px',
                                    fontSize: '11px',
                                    color: 'var(--text-tertiary)',
                                    textTransform: 'none',
                                    letterSpacing: 0,
                                }}
                            >
                                — click to expand
                            </span>
                        )}
                    </button>
                    {!descCollapsed && (
                        <div
                            className="pr-description"
                            style={{
                                fontSize: '14px',
                                lineHeight: 1.6,
                                color: 'var(--text-primary)',
                                maxHeight: '300px',
                                overflow: 'auto',
                                padding: '0 20px 16px',
                            }}
                        >
                            <GitHubMarkdown>{stripHtmlComments(metadata.body)}</GitHubMarkdown>
                        </div>
                    )}
                </div>
            )}

            {/* CI Failures */}
            {metadata.ci_failures && metadata.ci_failures.length > 0 && (
                <div
                    style={{
                        padding: '14px 20px',
                        borderTop: `1px solid ${colors.border}`,
                        background: colors.bgDangerDim,
                    }}
                >
                    <div
                        style={{
                            fontSize: '11px',
                            color: colors.textDanger,
                            marginBottom: '8px',
                            textTransform: 'uppercase',
                            letterSpacing: '0.5px',
                        }}
                    >
                        ✗ CI Failures
                    </div>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                        {metadata.ci_failures.map((failure, i) => (
                            <span
                                key={i}
                                style={{
                                    fontFamily: 'var(--font-mono)',
                                    fontSize: '12px',
                                    color: colors.textDanger,
                                }}
                            >
                                • {failure}
                            </span>
                        ))}
                    </div>
                </div>
            )}
        </div>
    );
}
