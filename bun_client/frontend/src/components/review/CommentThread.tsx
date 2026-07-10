import { colors } from '../../design';
import type { Comment } from './types';

interface CommentThreadProps {
    thread: Comment[]; // root comment first, then replies
    // True when the user is currently replying to or editing a comment in this thread.
    isActive: boolean;
    // Pin the card to the visible left edge while the code scrolls horizontally on mobile.
    stickyOnMobile?: boolean;
    onClick: () => void;
    onDeleteComment: (id: number) => void;
}

// A single review-comment thread card: root comment plus its replies.
// Clicking the card replies to the thread (or edits it, for local comments).
export default function CommentThread({
    thread,
    isActive,
    stickyOnMobile,
    onClick,
    onDeleteComment,
}: CommentThreadProps) {
    const rc = thread[0];
    const isLocalComment = rc.author === 'local';

    return (
        <div
            style={{
                margin: '10px 20px',
                ...(stickyOnMobile
                    ? {
                          position: 'sticky',
                          left: 0,
                          maxWidth: 'calc(100vw - 48px)',
                      }
                    : {}),
                border: isActive ? '2px solid var(--accent)' : '1px solid var(--border)',
                borderRadius: '6px',
                background: 'var(--bg-primary)',
                overflow: 'hidden',
                cursor: 'pointer',
                transition: 'border-color 0.15s ease',
            }}
            onClick={e => {
                e.stopPropagation();
                onClick();
            }}
            className="hover-thread"
            title={isLocalComment ? 'Click to edit this comment' : 'Click to reply to this thread'}
        >
            <div
                style={{
                    background: 'var(--bg-secondary)',
                    padding: '5px 10px',
                    fontSize: '11px',
                    borderBottom: '1px solid var(--border)',
                    color: 'var(--text-secondary)',
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                }}
            >
                <span>
                    {rc.author} commented
                    {rc.created_at && !rc.created_at.startsWith('0001') && (
                        <span style={{ marginLeft: '6px', opacity: 0.7 }}>
                            {new Date(rc.created_at).toLocaleString()}
                        </span>
                    )}
                </span>
                <div
                    style={{
                        display: 'flex',
                        alignItems: 'center',
                        gap: '8px',
                    }}
                >
                    <span
                        style={{
                            fontSize: '10px',
                            color: colors.accent,
                            opacity: 0.7,
                            background: colors.bgInfoDim,
                            padding: '2px 6px',
                            borderRadius: '4px',
                        }}
                    >
                        {isLocalComment ? '✎ click to edit' : '↩ click to reply'}
                    </span>
                    <span>ID: {rc.id}</span>
                </div>
            </div>
            {thread.map(c => (
                <div
                    key={c.id}
                    style={{
                        padding: '10px',
                        borderBottom:
                            c.id !== thread[thread.length - 1].id
                                ? '1px solid var(--border)'
                                : 'none',
                    }}
                >
                    {c !== rc && (
                        <div
                            style={{
                                fontSize: '11px',
                                color: 'var(--accent)',
                                marginBottom: '5px',
                                display: 'flex',
                                gap: '8px',
                            }}
                        >
                            <span>Reply by {c.author}:</span>
                            {c.created_at && !c.created_at.startsWith('0001') && (
                                <span
                                    style={{
                                        opacity: 0.7,
                                        color: 'var(--text-secondary)',
                                    }}
                                >
                                    {new Date(c.created_at).toLocaleString()}
                                </span>
                            )}
                        </div>
                    )}
                    <div
                        style={{
                            display: 'flex',
                            justifyContent: 'space-between',
                            alignItems: 'flex-start',
                            gap: '8px',
                        }}
                    >
                        <div style={{ whiteSpace: 'pre-wrap', flex: 1 }}>{c.body}</div>
                        {c.author === 'local' && (
                            <button
                                onClick={e => {
                                    e.stopPropagation();
                                    onDeleteComment(parseInt(c.id, 10));
                                }}
                                style={{
                                    background: 'none',
                                    border: 'none',
                                    color: 'var(--text-secondary)',
                                    cursor: 'pointer',
                                    fontSize: '12px',
                                    padding: '0 4px',
                                    opacity: 0.6,
                                    flexShrink: 0,
                                }}
                                title="Delete local comment"
                            >
                                ✕
                            </button>
                        )}
                    </div>
                </div>
            ))}
        </div>
    );
}
