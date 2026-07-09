import Markdown from 'react-markdown';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { Button, colors, shadows } from '../../design';
import type { DiffTheme } from './diff_theme';
import type { Comment } from './types';

interface OutdatedCommentsPanelProps {
    file: string;
    outdatedComments: Comment[];
    diffTheme: DiffTheme;
    onClose: () => void;
}

// Right-hand drawer listing a file's outdated comments with their diff context.
export default function OutdatedCommentsPanel({
    file,
    outdatedComments,
    diffTheme,
    onClose,
}: OutdatedCommentsPanelProps) {
    return (
        <div
            style={{
                position: 'fixed' as const,
                top: 0,
                left: 0,
                right: 0,
                bottom: 0,
                background: colors.overlayBg,
                display: 'flex',
                justifyContent: 'flex-end',
                zIndex: 100,
            }}
            onClick={onClose}
        >
            <div
                style={{
                    background: 'var(--bg-secondary)',
                    width: '600px',
                    maxWidth: '90vw',
                    height: '100vh',
                    borderLeft: '1px solid var(--border)',
                    boxShadow: shadows.lg,
                    display: 'flex',
                    flexDirection: 'column',
                    overflow: 'hidden',
                }}
                onClick={e => e.stopPropagation()}
            >
                <div
                    style={{
                        display: 'flex',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        padding: '20px 24px',
                        background: 'var(--bg-secondary)',
                        zIndex: 1,
                        borderBottom: '1px solid var(--border)',
                    }}
                >
                    <div style={{ display: 'flex', flexDirection: 'column' }}>
                        <h2 style={{ fontSize: '18px', margin: 0, color: 'var(--warning)' }}>
                            Outdated Comments
                        </h2>
                        <span
                            style={{
                                fontSize: '12px',
                                color: 'var(--text-secondary)',
                                marginTop: '4px',
                            }}
                        >
                            {file}
                        </span>
                    </div>
                    <Button onClick={onClose} variant="secondary" size="sm">
                        Close (Esc)
                    </Button>
                </div>
                <div style={{ flex: 1, overflowY: 'auto', padding: '24px' }}>
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '20px' }}>
                        {outdatedComments
                            .filter(c => c.path === file)
                            .map(c => (
                                <div
                                    key={c.id}
                                    style={{
                                        background: 'var(--bg-primary)',
                                        border: '1px solid var(--border)',
                                        borderRadius: '8px',
                                        overflow: 'hidden',
                                    }}
                                >
                                    <div
                                        style={{
                                            padding: '10px 15px',
                                            background: 'var(--bg-secondary)',
                                            borderBottom: '1px solid var(--border)',
                                            display: 'flex',
                                            justifyContent: 'space-between',
                                            alignItems: 'center',
                                        }}
                                    >
                                        <span
                                            style={{
                                                fontWeight: 600,
                                                fontSize: '14px',
                                                color: 'var(--text-primary)',
                                            }}
                                        >
                                            {c.author}
                                        </span>
                                        <span
                                            style={{
                                                fontSize: '11px',
                                                color: 'var(--text-secondary)',
                                            }}
                                        >
                                            {new Date(c.created_at).toLocaleString()}
                                        </span>
                                    </div>
                                    {c.diff_hunk && (
                                        <details
                                            open
                                            style={{
                                                background: 'var(--bg-secondary)',
                                                borderBottom: '1px solid var(--border)',
                                            }}
                                        >
                                            <summary
                                                style={{
                                                    padding: '8px 15px',
                                                    cursor: 'pointer',
                                                    fontSize: '12px',
                                                    fontWeight: 600,
                                                    color: 'var(--text-secondary)',
                                                    userSelect: 'none',
                                                    outline: 'none',
                                                }}
                                            >
                                                Context
                                            </summary>
                                            <div
                                                style={{
                                                    padding: '12px 15px',
                                                    background: 'var(--bg-primary)',
                                                    borderTop: '1px solid var(--border)',
                                                    fontSize: '12px',
                                                    overflowX: 'auto',
                                                }}
                                            >
                                                <SyntaxHighlighter
                                                    language="diff"
                                                    style={diffTheme}
                                                    customStyle={{
                                                        background: 'transparent',
                                                        margin: 0,
                                                        padding: 0,
                                                        fontSize: '12px',
                                                    }}
                                                >
                                                    {c.diff_hunk}
                                                </SyntaxHighlighter>
                                            </div>
                                        </details>
                                    )}
                                    <div
                                        className="markdown-content"
                                        style={{
                                            padding: '15px',
                                            fontSize: '14px',
                                            lineHeight: '1.6',
                                            color: 'var(--text-primary)',
                                        }}
                                    >
                                        <Markdown>{c.body}</Markdown>
                                    </div>
                                </div>
                            ))}
                    </div>
                </div>
            </div>
        </div>
    );
}
