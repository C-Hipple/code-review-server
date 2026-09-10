import { colors } from '../../design';
import type { PendingCommentPreview } from '../../review_preview_utils';
import { reviewVerdictLabel } from '../../review_preview_utils';
import GitHubMarkdown from '../GitHubMarkdown';
import DiffContextLines from './DiffContextLines';
import type { DiffTheme } from './diff_theme';

interface ReviewPreviewProps {
    // APPROVE / REQUEST_CHANGES / COMMENT — the event about to be submitted.
    reviewEvent: string;
    // The review body as it currently stands in the form above.
    reviewBody: string;
    // Who the review will be posted as. Blank when the server has no username.
    username?: string;
    // Every unsubmitted comment, in diff order, with its context.
    previews: PendingCommentPreview[];
    diffTheme: DiffTheme;
}

const verdictColor = (event: string): string => {
    if (event === 'APPROVE') return colors.success;
    if (event === 'REQUEST_CHANGES') return colors.danger;
    return colors.accent;
};

const verdictBg = (event: string): string => {
    if (event === 'APPROVE') return colors.bgSuccessDim;
    if (event === 'REQUEST_CHANGES') return colors.bgDangerDim;
    return colors.bgInfoDim;
};

// What submitting posts to GitHub, shown under the feedback box: the verdict and
// body as they are being typed, then every comment left on the diff, each with
// the lines it was left on rendered the way the diff renders them.
export default function ReviewPreview({
    reviewEvent,
    reviewBody,
    username,
    previews,
    diffTheme,
}: ReviewPreviewProps) {
    const accent = verdictColor(reviewEvent);

    return (
        <div
            style={{
                border: '1px solid var(--border)',
                borderRadius: '6px',
                background: 'var(--bg-primary)',
                overflow: 'hidden',
            }}
        >
            <div
                style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    gap: '8px',
                    flexWrap: 'wrap',
                    padding: '6px 10px',
                    background: 'var(--bg-secondary)',
                    borderBottom: '1px solid var(--border)',
                    fontSize: '11px',
                    color: 'var(--text-secondary)',
                    textTransform: 'uppercase',
                    letterSpacing: '0.04em',
                }}
            >
                <span>Review Preview</span>
                <span>
                    {previews.length === 0
                        ? 'no comments'
                        : `${previews.length} comment${previews.length === 1 ? '' : 's'}`}
                </span>
            </div>

            <div
                style={{
                    padding: '10px',
                    display: 'flex',
                    flexDirection: 'column',
                    gap: '8px',
                    borderBottom: previews.length > 0 ? '1px solid var(--border)' : 'none',
                }}
            >
                <span
                    style={{
                        alignSelf: 'flex-start',
                        fontSize: '12px',
                        fontWeight: 600,
                        color: accent,
                        background: verdictBg(reviewEvent),
                        border: `1px solid ${accent}`,
                        borderRadius: '999px',
                        padding: '2px 10px',
                    }}
                >
                    {reviewVerdictLabel(reviewEvent, username)}
                </span>
                {reviewBody.trim() ? (
                    <div className="markdown-content" style={{ fontSize: '13px' }}>
                        <GitHubMarkdown>{reviewBody}</GitHubMarkdown>
                    </div>
                ) : (
                    <span
                        style={{
                            fontSize: '12px',
                            color: 'var(--text-tertiary)',
                            fontStyle: 'italic',
                        }}
                    >
                        No review body
                    </span>
                )}
            </div>

            {previews.length > 0 && (
                <div
                    style={{
                        display: 'flex',
                        flexDirection: 'column',
                        gap: '10px',
                        padding: '10px',
                    }}
                >
                    {previews.map(p => (
                        <div
                            key={p.comment.id}
                            style={{
                                border: '1px solid var(--border)',
                                borderRadius: '6px',
                                overflow: 'hidden',
                                background: 'var(--bg-primary)',
                            }}
                        >
                            <div
                                style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'space-between',
                                    gap: '8px',
                                    flexWrap: 'wrap',
                                    padding: '5px 10px',
                                    background: 'var(--bg-secondary)',
                                    borderBottom: '1px solid var(--border)',
                                    fontSize: '11px',
                                    color: 'var(--text-secondary)',
                                }}
                            >
                                <span
                                    style={{
                                        fontFamily: 'var(--font-mono)',
                                        color: 'var(--text-primary)',
                                        wordBreak: 'break-all',
                                    }}
                                >
                                    {p.file || 'unknown file'}
                                    {p.lineNo !== null && `:${p.lineNo}`}
                                </span>
                                {p.replyTo && (
                                    <span
                                        style={{
                                            fontSize: '10px',
                                            color: colors.accent,
                                            background: colors.bgInfoDim,
                                            padding: '2px 6px',
                                            borderRadius: '4px',
                                        }}
                                    >
                                        ↩ reply to {p.replyTo.author}
                                    </span>
                                )}
                            </div>

                            {p.context.length > 0 ? (
                                <div style={{ borderBottom: '1px solid var(--border)' }}>
                                    <DiffContextLines
                                        lines={p.context}
                                        file={p.file}
                                        diffTheme={diffTheme}
                                        anchorIndex={p.anchorIndex}
                                    />
                                </div>
                            ) : (
                                <div
                                    style={{
                                        padding: '6px 10px',
                                        borderBottom: '1px solid var(--border)',
                                        fontSize: '11px',
                                        color: 'var(--text-tertiary)',
                                        fontStyle: 'italic',
                                    }}
                                >
                                    {p.fileLevel
                                        ? 'Comment on the file'
                                        : 'Context unavailable — the line is no longer in the diff'}
                                </div>
                            )}

                            <div
                                className="markdown-content"
                                style={{ padding: '10px', fontSize: '13px' }}
                            >
                                <GitHubMarkdown>{p.comment.body}</GitHubMarkdown>
                            </div>
                        </div>
                    ))}
                </div>
            )}
        </div>
    );
}
