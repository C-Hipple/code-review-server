import { useState } from 'react';
import { colors, shadows } from '../../design';
import { displayAuthor } from '../../comment_tray_utils';
import type { Comment } from './types';

interface CommentIndicatorProps {
    thread: Comment[];
    // The comment tray is open on one of this line's threads.
    active: boolean;
    // Preview the comments on hover; off while the tray is open beside the diff.
    preview: boolean;
    onOpen: () => void;
}

// Small badge by a commented line or file. Clicking it opens the comment tray
// on the line's thread (again: closes it); hovering previews the comments.
export default function CommentIndicator({
    thread,
    active,
    preview,
    onOpen,
}: CommentIndicatorProps) {
    const [hovered, setHovered] = useState(false);
    return (
        <span
            onMouseEnter={() => setHovered(true)}
            onMouseLeave={() => setHovered(false)}
            onClick={e => {
                e.stopPropagation();
                onOpen();
            }}
            style={{
                position: 'relative',
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: '2px',
                minWidth: '20px',
                height: '18px',
                padding: '0 5px',
                borderRadius: '9px',
                background: active ? colors.accent : colors.bgInfoDim,
                color: active ? '#fff' : colors.accent,
                fontSize: '10px',
                fontWeight: 600,
                cursor: 'pointer',
                userSelect: 'none',
            }}
            title={active ? 'Close the comment tray' : 'Show comments in the side tray'}
        >
            <span>💬</span>
            {thread.length > 1 && <span>{thread.length}</span>}
            {hovered && preview && !active && (
                <div
                    onClick={e => e.stopPropagation()}
                    style={{
                        position: 'absolute',
                        bottom: '100%',
                        left: 0,
                        marginBottom: '6px',
                        background: 'var(--bg-secondary)',
                        border: '1px solid var(--border)',
                        borderRadius: '8px',
                        padding: '8px 10px',
                        width: '320px',
                        maxWidth: '80vw',
                        maxHeight: '260px',
                        overflowY: 'auto',
                        boxShadow: shadows.md,
                        zIndex: 100,
                        fontSize: '12px',
                        fontWeight: 400,
                        fontFamily: 'var(--font-sans)',
                        color: 'var(--text-primary)',
                        cursor: 'default',
                        textAlign: 'left',
                        whiteSpace: 'normal',
                    }}
                >
                    {thread.map(c => (
                        <div key={c.id} style={{ marginBottom: '8px' }}>
                            <div style={{ fontWeight: 600, marginBottom: '2px' }}>
                                {displayAuthor(c.author)}
                            </div>
                            <div
                                style={{
                                    whiteSpace: 'pre-wrap',
                                    color: 'var(--text-secondary)',
                                    display: '-webkit-box',
                                    WebkitLineClamp: 4,
                                    WebkitBoxOrient: 'vertical',
                                    overflow: 'hidden',
                                }}
                            >
                                {c.body}
                            </div>
                        </div>
                    ))}
                    <div style={{ fontSize: '11px', color: 'var(--text-tertiary)' }}>
                        Click to open in the comment tray
                    </div>
                </div>
            )}
        </span>
    );
}
