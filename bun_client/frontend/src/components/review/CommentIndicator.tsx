import { useState } from 'react';
import { colors, shadows } from '../../design';
import type { Comment } from './types';

interface CommentIndicatorProps {
    thread: Comment[];
    visible: boolean;
    onToggle: () => void;
}

// Small badge shown by a commented line/file when its thread is hidden.
// Hovering previews the thread read-only; clicking toggles the full
// interactive thread open/closed.
export default function CommentIndicator({ thread, visible, onToggle }: CommentIndicatorProps) {
    const [hovered, setHovered] = useState(false);
    return (
        <span
            onMouseEnter={() => setHovered(true)}
            onMouseLeave={() => setHovered(false)}
            onClick={e => {
                e.stopPropagation();
                onToggle();
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
                background: visible ? colors.accent : colors.bgInfoDim,
                color: visible ? '#fff' : colors.accent,
                fontSize: '10px',
                fontWeight: 600,
                cursor: 'pointer',
                userSelect: 'none',
            }}
            title={visible ? 'Hide comment thread' : 'Click to show comment thread'}
        >
            <span>💬</span>
            {thread.length > 1 && <span>{thread.length}</span>}
            {hovered && !visible && (
                <div
                    onClick={e => e.stopPropagation()}
                    style={{
                        position: 'absolute',
                        bottom: '100%',
                        left: 0,
                        marginBottom: '6px',
                        background: 'var(--bg-secondary)',
                        border: '1px solid var(--border)',
                        borderRadius: '6px',
                        padding: '8px',
                        width: '320px',
                        maxWidth: '80vw',
                        maxHeight: '260px',
                        overflowY: 'auto',
                        boxShadow: shadows.md,
                        zIndex: 100,
                        fontSize: '12px',
                        fontWeight: 400,
                        color: 'var(--text-primary)',
                        cursor: 'default',
                        textAlign: 'left',
                        whiteSpace: 'normal',
                    }}
                >
                    {thread.map(c => (
                        <div key={c.id} style={{ marginBottom: '6px' }}>
                            <div
                                style={{
                                    color: 'var(--text-secondary)',
                                    fontSize: '11px',
                                    marginBottom: '2px',
                                }}
                            >
                                {c.author}
                            </div>
                            <div style={{ whiteSpace: 'pre-wrap' }}>{c.body}</div>
                        </div>
                    ))}
                </div>
            )}
        </span>
    );
}
