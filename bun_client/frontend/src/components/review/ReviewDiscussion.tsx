import { useState } from 'react';
import Markdown from 'react-markdown';
import { colors } from '../../design';
import { type ReviewData, stripHtmlComments } from './types';

// Chronological list of submitted reviews that carry a body.
export default function ReviewDiscussion({ reviews }: { reviews: ReviewData[] }) {
    // Starts collapsed so the diff stays close to the top of the page.
    const [collapsed, setCollapsed] = useState(true);
    const withBody = reviews.filter(r => r.body && r.body.trim());
    if (withBody.length === 0) return null;

    return (
        <div
            style={{
                background: 'var(--bg-secondary)',
                borderRadius: '8px',
                border: '1px solid var(--border)',
                marginBottom: '16px',
                overflow: 'hidden',
            }}
        >
            <button
                type="button"
                onClick={() => setCollapsed(c => !c)}
                aria-expanded={!collapsed}
                style={{
                    width: '100%',
                    padding: '12px 16px',
                    borderTop: 'none',
                    borderLeft: 'none',
                    borderRight: 'none',
                    borderBottom: collapsed ? 'none' : '1px solid var(--border)',
                    background: 'var(--bg-primary)',
                    fontSize: '13px',
                    fontWeight: 500,
                    color: 'var(--text-secondary)',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '8px',
                    cursor: 'pointer',
                    textAlign: 'left',
                }}
            >
                <span
                    style={{
                        display: 'inline-block',
                        transform: collapsed ? 'rotate(-90deg)' : 'rotate(0deg)',
                        transition: 'transform 0.15s ease',
                        fontSize: '10px',
                    }}
                >
                    ▼
                </span>
                Review Discussion ({withBody.length})
                {collapsed && (
                    <span
                        style={{
                            fontSize: '11px',
                            color: 'var(--text-tertiary)',
                            fontWeight: 400,
                        }}
                    >
                        — click to expand
                    </span>
                )}
            </button>
            <div
                style={{
                    display: collapsed ? 'none' : 'flex',
                    flexDirection: 'column',
                }}
            >
                {withBody
                    .sort(
                        (a, b) =>
                            new Date(a.submitted_at).getTime() - new Date(b.submitted_at).getTime()
                    )
                    .map(r => (
                        <div
                            key={r.id}
                            style={{
                                padding: '14px 16px',
                                borderBottom: '1px solid var(--border)',
                            }}
                        >
                            <div
                                style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '8px',
                                    marginBottom: '8px',
                                }}
                            >
                                <span
                                    style={{
                                        fontWeight: 600,
                                        fontSize: '13px',
                                        color: 'var(--text-primary)',
                                    }}
                                >
                                    @{r.user}
                                </span>
                                <span
                                    style={{
                                        fontSize: '11px',
                                        padding: '1px 6px',
                                        borderRadius: '4px',
                                        fontWeight: 500,
                                        ...(r.state === 'APPROVED'
                                            ? {
                                                  background: colors.bgSuccessDim,
                                                  color: colors.success,
                                                  border: `1px solid ${colors.borderSuccessDim}`,
                                              }
                                            : r.state === 'CHANGES_REQUESTED'
                                              ? {
                                                    background: colors.bgDangerDim,
                                                    color: colors.danger,
                                                    border: `1px solid ${colors.borderDangerDim}`,
                                                }
                                              : {
                                                    background: 'var(--bg-tertiary)',
                                                    color: 'var(--text-secondary)',
                                                    border: '1px solid var(--border)',
                                                }),
                                    }}
                                >
                                    {r.state === 'APPROVED'
                                        ? 'Approved'
                                        : r.state === 'CHANGES_REQUESTED'
                                          ? 'Changes Requested'
                                          : 'Commented'}
                                </span>
                                <span
                                    style={{
                                        fontSize: '11px',
                                        color: 'var(--text-secondary)',
                                        marginLeft: 'auto',
                                    }}
                                >
                                    {new Date(r.submitted_at).toLocaleString()}
                                </span>
                                {r.html_url && (
                                    <a
                                        href={r.html_url}
                                        target="_blank"
                                        rel="noopener noreferrer"
                                        style={{
                                            fontSize: '11px',
                                            color: 'var(--accent)',
                                            textDecoration: 'none',
                                        }}
                                    >
                                        View
                                    </a>
                                )}
                            </div>
                            <div
                                className="pr-description"
                                style={{
                                    fontSize: '14px',
                                    lineHeight: 1.6,
                                    color: 'var(--text-primary)',
                                }}
                            >
                                <Markdown>{stripHtmlComments(r.body)}</Markdown>
                            </div>
                        </div>
                    ))}
            </div>
        </div>
    );
}
