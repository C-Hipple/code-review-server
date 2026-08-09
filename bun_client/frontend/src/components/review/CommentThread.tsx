import Markdown from 'react-markdown';
import { colors } from '../../design';
import { buildReplyTree, flattenReplyTree, summarizeThread } from '../../discussion_utils';
import ThreadStateBadges from './ThreadStateBadges';
import type { Comment } from './types';

interface CommentThreadProps {
    thread: Comment[]; // root comment first, then replies in posting order
    // True when the user is currently replying to or editing a comment in this thread.
    isActive: boolean;
    // Pin the card to the visible left edge while the code scrolls horizontally on mobile.
    stickyOnMobile?: boolean;
    onClick: () => void;
    onDeleteComment: (id: number) => void;
}

// Replies are indented by this much per level, capped so a long back-and-forth
// doesn't squeeze the text off the right edge of the card.
const INDENT_PER_LEVEL = 18;
const MAX_INDENT_LEVEL = 4;

// A single review-comment thread card: root comment plus its replies, indented
// under whatever each reply actually answered. Clicking the card replies to the
// thread (or edits it, for local comments).
export default function CommentThread({
    thread,
    isActive,
    stickyOnMobile,
    onClick,
    onDeleteComment,
}: CommentThreadProps) {
    const rc = thread[0];
    const isLocalComment = rc.author === 'local';
    const summary = summarizeThread(thread);
    // Flatten the reply tree back to render order so each row keeps its depth
    // but the card stays a simple vertical list.
    const nodes = flattenReplyTree(buildReplyTree(thread));

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
                // A resolved conversation is settled; fade it back so unresolved
                // threads are what catches the eye while scanning a diff.
                opacity: summary.resolved ? 0.72 : 1,
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
                    gap: '8px',
                    flexWrap: 'wrap',
                }}
            >
                <span style={{ display: 'flex', alignItems: 'center', gap: '6px' }}>
                    <span>
                        {rc.author} commented
                        {rc.created_at && !rc.created_at.startsWith('0001') && (
                            <span style={{ marginLeft: '6px', opacity: 0.7 }}>
                                {new Date(rc.created_at).toLocaleString()}
                            </span>
                        )}
                    </span>
                    <ThreadStateBadges
                        resolved={summary.resolved}
                        resolvedBy={summary.resolvedBy}
                        outdated={summary.outdated}
                        replyCount={thread.length - 1}
                    />
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
            {nodes.map(({ comment: c, depth }, i) => {
                const indentLevel = Math.min(depth, MAX_INDENT_LEVEL);
                return (
                    <div
                        key={c.id}
                        style={{
                            padding: '10px',
                            borderBottom: i < nodes.length - 1 ? '1px solid var(--border)' : 'none',
                            // Indent by depth and mark the level with a left rail,
                            // so nesting reads at a glance the way it does in a
                            // threaded mail client.
                            marginLeft: `${indentLevel * INDENT_PER_LEVEL}px`,
                            borderLeft: depth > 0 ? '2px solid var(--border)' : 'none',
                        }}
                    >
                        {depth > 0 && (
                            <div
                                style={{
                                    fontSize: '11px',
                                    color: 'var(--accent)',
                                    marginBottom: '5px',
                                    display: 'flex',
                                    gap: '8px',
                                    alignItems: 'center',
                                }}
                            >
                                <span style={{ opacity: 0.6 }}>↳</span>
                                <span>{c.author} replied</span>
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
                            <div className="markdown-content" style={{ flex: 1, minWidth: 0 }}>
                                <Markdown>{c.body}</Markdown>
                            </div>
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
                );
            })}
        </div>
    );
}
