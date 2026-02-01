import React, { useState, useEffect, useRef, useMemo } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark, oneLight, gruvboxDark, gruvboxLight, solarizedlight, solarizedDarkAtom, dracula, nord, nightOwl } from 'react-syntax-highlighter/dist/esm/styles/prism';
import { colors, shadows } from '../design';
import { readFile } from '../api';
import type { Theme } from '../design';

interface CodeViewerModalProps {
    isOpen: boolean;
    onClose: () => void;
    filePath: string;
    repoPath: string;
    initialLine?: number;
    theme: Theme;
}

// Map file extensions to Prism language identifiers
const getLanguageFromFilename = (filename: string): string => {
    const ext = filename.split('.').pop()?.toLowerCase() || '';
    const languageMap: Record<string, string> = {
        'js': 'javascript', 'jsx': 'jsx', 'ts': 'typescript', 'tsx': 'tsx',
        'py': 'python', 'rb': 'ruby', 'go': 'go', 'rs': 'rust',
        'java': 'java', 'kt': 'kotlin', 'c': 'c', 'h': 'c',
        'cpp': 'cpp', 'cc': 'cpp', 'hpp': 'cpp', 'cs': 'csharp',
        'html': 'html', 'css': 'css', 'scss': 'scss', 'json': 'json',
        'yaml': 'yaml', 'yml': 'yaml', 'toml': 'toml', 'xml': 'xml',
        'sh': 'bash', 'bash': 'bash', 'sql': 'sql', 'md': 'markdown',
        'el': 'lisp', 'lisp': 'lisp', 'hs': 'haskell', 'ml': 'ocaml',
        'ex': 'elixir', 'exs': 'elixir', 'clj': 'clojure', 'swift': 'swift',
        'php': 'php', 'lua': 'lua', 'vim': 'vim', 'proto': 'protobuf',
    };
    const basename = filename.split('/').pop()?.toLowerCase() || '';
    if (basename === 'dockerfile') return 'docker';
    if (basename === 'makefile') return 'makefile';
    return languageMap[ext] || 'text';
};

export default function CodeViewerModal({
    isOpen,
    onClose,
    filePath,
    repoPath,
    initialLine = 1,
    theme,
}: CodeViewerModalProps) {
    const [content, setContent] = useState<string>('');
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);

    // Modal position and size
    const [position, setPosition] = useState({ x: 100, y: 100 });
    const [size, setSize] = useState({ width: 800, height: 600 });
    const [isDragging, setIsDragging] = useState(false);
    const [isResizing, setIsResizing] = useState<string | null>(null);
    const [dragOffset, setDragOffset] = useState({ x: 0, y: 0 });

    const modalRef = useRef<HTMLDivElement>(null);
    const contentRef = useRef<HTMLDivElement>(null);
    const targetLineRef = useRef<HTMLDivElement>(null);

    // Fetch file content
    useEffect(() => {
        if (!isOpen || !filePath) return;

        const fetchContent = async () => {
            setLoading(true);
            setError(null);
            try {
                // filePath is an absolute path from the LSP, pass it directly
                const text = await readFile(repoPath, filePath);
                setContent(text);
            } catch (e) {
                setError(e instanceof Error ? e.message : 'Failed to load file');
            } finally {
                setLoading(false);
            }
        };

        fetchContent();
    }, [isOpen, filePath, repoPath]);

    // Scroll to target line when content loads
    useEffect(() => {
        if (content && targetLineRef.current) {
            setTimeout(() => {
                targetLineRef.current?.scrollIntoView({ behavior: 'smooth', block: 'center' });
            }, 100);
        }
    }, [content, initialLine]);

    // Handle ESC key
    useEffect(() => {
        if (!isOpen) return;

        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                onClose();
            }
        };

        window.addEventListener('keydown', handleKeyDown);
        return () => window.removeEventListener('keydown', handleKeyDown);
    }, [isOpen, onClose]);

    // Handle dragging
    useEffect(() => {
        if (!isDragging) return;

        const handleMouseMove = (e: MouseEvent) => {
            setPosition({
                x: e.clientX - dragOffset.x,
                y: e.clientY - dragOffset.y,
            });
        };

        const handleMouseUp = () => setIsDragging(false);

        window.addEventListener('mousemove', handleMouseMove);
        window.addEventListener('mouseup', handleMouseUp);
        return () => {
            window.removeEventListener('mousemove', handleMouseMove);
            window.removeEventListener('mouseup', handleMouseUp);
        };
    }, [isDragging, dragOffset]);

    // Handle resizing
    useEffect(() => {
        if (!isResizing) return;

        const handleMouseMove = (e: MouseEvent) => {
            const minWidth = 400;
            const minHeight = 300;

            if (isResizing.includes('e')) {
                setSize(s => ({ ...s, width: Math.max(minWidth, e.clientX - position.x) }));
            }
            if (isResizing.includes('s')) {
                setSize(s => ({ ...s, height: Math.max(minHeight, e.clientY - position.y) }));
            }
            if (isResizing.includes('w')) {
                const newWidth = Math.max(minWidth, size.width + (position.x - e.clientX));
                if (newWidth > minWidth) {
                    setPosition(p => ({ ...p, x: e.clientX }));
                    setSize(s => ({ ...s, width: newWidth }));
                }
            }
            if (isResizing.includes('n')) {
                const newHeight = Math.max(minHeight, size.height + (position.y - e.clientY));
                if (newHeight > minHeight) {
                    setPosition(p => ({ ...p, y: e.clientY }));
                    setSize(s => ({ ...s, height: newHeight }));
                }
            }
        };

        const handleMouseUp = () => setIsResizing(null);

        window.addEventListener('mousemove', handleMouseMove);
        window.addEventListener('mouseup', handleMouseUp);
        return () => {
            window.removeEventListener('mousemove', handleMouseMove);
            window.removeEventListener('mouseup', handleMouseUp);
        };
    }, [isResizing, position, size]);

    const startDrag = (e: React.MouseEvent) => {
        if ((e.target as HTMLElement).closest('button')) return;
        setIsDragging(true);
        setDragOffset({
            x: e.clientX - position.x,
            y: e.clientY - position.y,
        });
    };

    // Get theme for syntax highlighting
    const syntaxTheme = useMemo(() => {
        switch (theme) {
            case 'light': return oneLight;
            case 'gruvbox-dark': return gruvboxDark;
            case 'gruvbox-light': return gruvboxLight;
            case 'solarized-light': return solarizedlight;
            case 'solarized-dark': return solarizedDarkAtom;
            case 'dracula': return dracula;
            case 'nord': return nord;
            case 'night-owl': return nightOwl;
            default: return oneDark;
        }
    }, [theme]);

    if (!isOpen) return null;

    const filename = filePath.split('/').pop() || filePath;
    const language = getLanguageFromFilename(filename);
    const lines = content.split('\n');

    return (
        <div
            style={{
                position: 'fixed',
                top: 0,
                left: 0,
                right: 0,
                bottom: 0,
                zIndex: 999,
                pointerEvents: 'none',
            }}
        >
            <div
                ref={modalRef}
                style={{
                    position: 'absolute',
                    top: position.y,
                    left: position.x,
                    width: size.width,
                    height: size.height,
                    background: 'var(--bg-secondary)',
                    border: '1px solid var(--border)',
                    borderRadius: '8px',
                    boxShadow: shadows.lg,
                    display: 'flex',
                    flexDirection: 'column',
                    pointerEvents: 'auto',
                    overflow: 'hidden',
                }}
            >
                {/* Header - draggable */}
                <div
                    style={{
                        padding: '12px 16px',
                        background: 'var(--bg-tertiary)',
                        borderBottom: '1px solid var(--border)',
                        display: 'flex',
                        alignItems: 'center',
                        justifyContent: 'space-between',
                        cursor: 'move',
                        userSelect: 'none',
                    }}
                    onMouseDown={startDrag}
                >
                    <div style={{ display: 'flex', alignItems: 'center', gap: '8px', overflow: 'hidden' }}>
                        <span style={{ fontSize: '14px' }}>📄</span>
                        <span
                            style={{
                                fontFamily: 'var(--font-mono)',
                                fontSize: '13px',
                                color: 'var(--text-primary)',
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                            }}
                            title={filePath}
                        >
                            {filename}
                        </span>
                        {initialLine && (
                            <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                                (line {initialLine})
                            </span>
                        )}
                    </div>
                    <button
                        onClick={onClose}
                        style={{
                            background: 'transparent',
                            border: 'none',
                            color: 'var(--text-secondary)',
                            fontSize: '20px',
                            cursor: 'pointer',
                            padding: '0 4px',
                            lineHeight: 1,
                        }}
                    >
                        ×
                    </button>
                </div>

                {/* Content */}
                <div
                    ref={contentRef}
                    style={{
                        flex: 1,
                        overflow: 'auto',
                        background: 'var(--bg-primary)',
                    }}
                >
                    {loading && (
                        <div style={{ padding: '20px', color: 'var(--text-secondary)' }}>
                            Loading...
                        </div>
                    )}
                    {error && (
                        <div style={{ padding: '20px', color: colors.danger }}>
                            Error: {error}
                        </div>
                    )}
                    {!loading && !error && content && (
                        <div style={{ fontFamily: 'var(--font-mono)', fontSize: '13px' }}>
                            {lines.map((line, idx) => {
                                const lineNum = idx + 1;
                                const isTargetLine = lineNum === initialLine;
                                return (
                                    <div
                                        key={idx}
                                        ref={isTargetLine ? targetLineRef : undefined}
                                        style={{
                                            display: 'flex',
                                            background: isTargetLine ? colors.bgWarningDim : 'transparent',
                                            borderLeft: isTargetLine ? `3px solid ${colors.warning}` : '3px solid transparent',
                                        }}
                                    >
                                        <span
                                            style={{
                                                width: '50px',
                                                minWidth: '50px',
                                                padding: '0 8px',
                                                textAlign: 'right',
                                                color: 'var(--text-tertiary)',
                                                background: 'var(--bg-secondary)',
                                                borderRight: '1px solid var(--border)',
                                                userSelect: 'none',
                                            }}
                                        >
                                            {lineNum}
                                        </span>
                                        <span style={{ padding: '0 8px', flex: 1, whiteSpace: 'pre' }}>
                                            <SyntaxHighlighter
                                                language={language}
                                                style={syntaxTheme}
                                                customStyle={{
                                                    background: 'transparent',
                                                    margin: 0,
                                                    padding: 0,
                                                    display: 'inline',
                                                }}
                                                PreTag="span"
                                                CodeTag="span"
                                            >
                                                {line || ' '}
                                            </SyntaxHighlighter>
                                        </span>
                                    </div>
                                );
                            })}
                        </div>
                    )}
                </div>

                {/* Resize handles */}
                {/* Right edge */}
                <div
                    style={{
                        position: 'absolute',
                        top: 0,
                        right: 0,
                        width: '5px',
                        height: '100%',
                        cursor: 'ew-resize',
                    }}
                    onMouseDown={() => setIsResizing('e')}
                />
                {/* Bottom edge */}
                <div
                    style={{
                        position: 'absolute',
                        bottom: 0,
                        left: 0,
                        width: '100%',
                        height: '5px',
                        cursor: 'ns-resize',
                    }}
                    onMouseDown={() => setIsResizing('s')}
                />
                {/* Bottom-right corner */}
                <div
                    style={{
                        position: 'absolute',
                        bottom: 0,
                        right: 0,
                        width: '15px',
                        height: '15px',
                        cursor: 'nwse-resize',
                    }}
                    onMouseDown={() => setIsResizing('se')}
                />
                {/* Left edge */}
                <div
                    style={{
                        position: 'absolute',
                        top: 0,
                        left: 0,
                        width: '5px',
                        height: '100%',
                        cursor: 'ew-resize',
                    }}
                    onMouseDown={() => setIsResizing('w')}
                />
                {/* Top edge */}
                <div
                    style={{
                        position: 'absolute',
                        top: 0,
                        left: 0,
                        width: '100%',
                        height: '5px',
                        cursor: 'ns-resize',
                    }}
                    onMouseDown={() => setIsResizing('n')}
                />
            </div>
        </div>
    );
}
