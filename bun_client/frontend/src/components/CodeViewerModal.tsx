import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import {
    oneDark,
    oneLight,
    gruvboxDark,
    gruvboxLight,
    solarizedlight,
    solarizedDarkAtom,
    dracula,
    nord,
    nightOwl,
} from 'react-syntax-highlighter/dist/esm/styles/prism';
import { colors, shadows } from '../design';
import { readFile, listFiles } from '../api';
import type { Theme } from '../design';
import { useLsp } from '../hooks/useLsp';
import { getClickColumn } from '../utils/dom';
import LspPopover from './LspPopover';

interface CodeViewerModalProps {
    isOpen: boolean;
    onClose: () => void;
    filePath: string;
    repoPath: string;
    initialLine?: number;
    theme: Theme;
    initialPosition?: { x: number; y: number };
}

interface FileNode {
    name: string;
    path: string;
    isDirectory: boolean;
    children?: FileNode[];
}

// Map file extensions to Prism language identifiers
const getLanguageFromFilename = (filename: string): string => {
    const ext = filename.split('.').pop()?.toLowerCase() || '';
    const languageMap: Record<string, string> = {
        js: 'javascript',
        jsx: 'jsx',
        ts: 'typescript',
        tsx: 'tsx',
        py: 'python',
        rb: 'ruby',
        go: 'go',
        rs: 'rust',
        java: 'java',
        kt: 'kotlin',
        c: 'c',
        h: 'c',
        cpp: 'cpp',
        cc: 'cpp',
        hpp: 'cpp',
        cs: 'csharp',
        html: 'html',
        css: 'css',
        scss: 'scss',
        json: 'json',
        yaml: 'yaml',
        yml: 'yaml',
        toml: 'toml',
        xml: 'xml',
        sh: 'bash',
        bash: 'bash',
        sql: 'sql',
        md: 'markdown',
        el: 'lisp',
        lisp: 'lisp',
        hs: 'haskell',
        ml: 'ocaml',
        ex: 'elixir',
        exs: 'elixir',
        clj: 'clojure',
        swift: 'swift',
        php: 'php',
        lua: 'lua',
        vim: 'vim',
        proto: 'protobuf',
    };
    const basename = filename.split('/').pop()?.toLowerCase() || '';
    if (basename === 'dockerfile') return 'docker';
    if (basename === 'makefile') return 'makefile';
    return languageMap[ext] || 'text';
};

const getIconFromFilename = (filename: string, isDirectory: boolean): string => {
    if (isDirectory) return '📁';
    const ext = filename.split('.').pop()?.toLowerCase() || '';
    const iconMap: Record<string, string> = {
        js: '🟨',
        jsx: '⚛️',
        ts: '🟦',
        tsx: '⚛️',
        py: '🐍',
        rb: '💎',
        go: '🐹',
        rs: '🦀',
        java: '☕',
        kt: '🎯',
        c: '©️',
        h: 'H',
        cpp: 'C+',
        cc: 'C+',
        hpp: 'H+',
        cs: '♯',
        html: '🌐',
        css: '🎨',
        scss: '🎨',
        json: '📋',
        yaml: '📜',
        yml: '📜',
        toml: '⚙️',
        xml: '📜',
        sh: '🐚',
        bash: '🐚',
        sql: '🗄️',
        md: '📝',
        el: 'λ',
        lisp: 'λ',
        hs: 'λ',
        ml: 'λ',
        ex: '💧',
        exs: '💧',
        clj: 'λ',
        swift: '🍎',
        php: '🐘',
        lua: '🌙',
        vim: '💚',
        proto: '🔌',
    };
    const basename = filename.split('/').pop()?.toLowerCase() || '';
    if (basename === 'dockerfile') return '🐳';
    if (basename === 'makefile') return '🛠️';
    if (basename === 'package.json') return '📦';
    if (basename === '.gitignore') return '🚫';

    return iconMap[ext] || '📄';
};

const buildFileTree = (files: string[]): FileNode[] => {
    const root: FileNode[] = [];
    files.forEach(path => {
        const parts = path.split('/');
        let currentLevel = root;
        let currentPath = '';
        parts.forEach((part, i) => {
            currentPath = currentPath ? `${currentPath}/${part}` : part;
            const isLast = i === parts.length - 1;
            let node = currentLevel.find(n => n.name === part);
            if (!node) {
                node = {
                    name: part,
                    path: currentPath,
                    isDirectory: !isLast,
                    children: isLast ? undefined : [],
                };
                currentLevel.push(node);
            }
            if (!isLast) {
                currentLevel = node.children!;
            }
        });
    });
    const sortNodes = (nodes: FileNode[]) => {
        nodes.sort((a, b) => {
            if (a.isDirectory && !b.isDirectory) return -1;
            if (!a.isDirectory && b.isDirectory) return 1;
            return a.name.localeCompare(b.name);
        });
        nodes.forEach(n => {
            if (n.children) sortNodes(n.children);
        });
    };
    sortNodes(root);
    return root;
};

export default function CodeViewerModal({
    isOpen,
    onClose,
    filePath,
    repoPath,
    initialLine = 1,
    theme,
    initialPosition,
}: CodeViewerModalProps) {
    const [currentFilePath, setCurrentFilePath] = useState(filePath);
    const [content, setContent] = useState<string>('');
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [scrollToLine, setScrollToLine] = useState<number | null>(null);

    const [allFiles, setAllFiles] = useState<string[]>([]);
    const [showFileTree, setShowFileTree] = useState(false);
    const [expandedDirs, setExpandedDirs] = useState<Set<string>>(new Set());

    // Modal position and size
    const [position, setPosition] = useState(initialPosition || { x: 100, y: 100 });
    const [size, setSize] = useState({ width: 900, height: 600 });
    const [isDragging, setIsDragging] = useState(false);
    const [isResizing, setIsResizing] = useState<string | null>(null);
    const [dragOffset, setDragOffset] = useState({ x: 0, y: 0 });

    // LSP state
    const [activeLspLine, setActiveLspLine] = useState<number | null>(null);

    const language = useMemo(() => {
        const displayFn = currentFilePath.split('/').pop() || currentFilePath;
        return getLanguageFromFilename(displayFn);
    }, [currentFilePath]);

    const lsp = useLsp({
        mode: 'file',
        repoPath,
        filePath: currentFilePath,
        fileContent: content,
        languageId: language,
        enabled: isOpen && !!content,
    });

    // Clear LSP state on file change
    useEffect(() => {
        setActiveLspLine(null);
        lsp.clearData();
    }, [currentFilePath]);

    const modalRef = useRef<HTMLDivElement>(null);
    const contentRef = useRef<HTMLDivElement>(null);
    const animationFrameId = useRef<number | null>(null);
    const resizeFrameId = useRef<number | null>(null);

    // Ensure modal stays within viewport bounds
    useEffect(() => {
        if (!isOpen) return;

        const margin = 20;
        const vWidth = window.innerWidth;
        const vHeight = window.innerHeight;

        let newSize = { ...size };
        let newPos = { ...position };

        // Adjust size if it's larger than viewport
        if (newSize.width > vWidth - margin * 2) {
            newSize.width = vWidth - margin * 2;
        }
        if (newSize.height > vHeight - margin * 2) {
            newSize.height = vHeight - margin * 2;
        }

        // Adjust position to stay on screen
        if (newPos.x + newSize.width > vWidth - margin) {
            newPos.x = vWidth - newSize.width - margin;
        }
        if (newPos.y + newSize.height > vHeight - margin) {
            newPos.y = vHeight - newSize.height - margin;
        }

        // Ensure position isn't negative
        if (newPos.x < margin) newPos.x = margin;
        if (newPos.y < margin) newPos.y = margin;

        if (newPos.x !== position.x || newPos.y !== position.y) {
            setPosition(newPos);
        }
        if (newSize.width !== size.width || newSize.height !== size.height) {
            setSize(newSize);
        }
    }, [isOpen]);

    // Sync currentFilePath when prop changes (if user clicks another file link in Review.tsx)
    useEffect(() => {
        setCurrentFilePath(filePath);
    }, [filePath]);

    // Fetch file list
    useEffect(() => {
        if (!isOpen || !repoPath) return;
        listFiles(repoPath).then(setAllFiles).catch(console.error);
    }, [isOpen, repoPath]);

    const fileTree = useMemo(() => buildFileTree(allFiles), [allFiles]);

    // Fetch file content
    useEffect(() => {
        if (!isOpen || !currentFilePath) return;

        const fetchContent = async () => {
            setLoading(true);
            setError(null);
            try {
                // If currentFilePath is relative, it might need to be resolved
                // but if it's from the tree, it's relative to repoPath.
                // If it's absolute, readFile handles it.
                // Our server.ts expects absolute or relative to repoPath.
                const text = await readFile(repoPath, currentFilePath);
                setContent(text);
            } catch (e) {
                setError(e instanceof Error ? e.message : 'Failed to load file');
            } finally {
                setLoading(false);
            }
        };

        fetchContent();
    }, [isOpen, currentFilePath, repoPath]);

    // Scroll to target line when content loads
    useEffect(() => {
        const targetLine = scrollToLine ?? (currentFilePath === filePath ? initialLine : null);
        if (content && contentRef.current && targetLine) {
            setTimeout(() => {
                const lineElement = contentRef.current?.querySelector(
                    `code > span:nth-child(${targetLine})`
                );
                if (lineElement) {
                    lineElement.scrollIntoView({ behavior: 'smooth', block: 'center' });
                }
                // Clear scrollToLine after scrolling so it doesn't re-trigger
                if (scrollToLine) setScrollToLine(null);
            }, 100);
        }
    }, [content, initialLine, currentFilePath, filePath, scrollToLine]);

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

    // Handle dragging with requestAnimationFrame throttling
    useEffect(() => {
        if (!isDragging) return;

        const handleMouseMove = (e: MouseEvent) => {
            if (!animationFrameId.current) {
                animationFrameId.current = requestAnimationFrame(() => {
                    setPosition({
                        x: e.clientX - dragOffset.x,
                        y: e.clientY - dragOffset.y,
                    });
                    animationFrameId.current = null;
                });
            }
        };

        const handleMouseUp = () => {
            setIsDragging(false);
            if (animationFrameId.current) {
                cancelAnimationFrame(animationFrameId.current);
                animationFrameId.current = null;
            }
        };

        window.addEventListener('mousemove', handleMouseMove);
        window.addEventListener('mouseup', handleMouseUp);
        return () => {
            window.removeEventListener('mousemove', handleMouseMove);
            window.removeEventListener('mouseup', handleMouseUp);
            if (animationFrameId.current) {
                cancelAnimationFrame(animationFrameId.current);
                animationFrameId.current = null;
            }
        };
    }, [isDragging, dragOffset]);

    // Handle resizing with requestAnimationFrame throttling
    useEffect(() => {
        if (!isResizing) return;

        const handleMouseMove = (e: MouseEvent) => {
            if (!resizeFrameId.current) {
                resizeFrameId.current = requestAnimationFrame(() => {
                    const minWidth = 400;
                    const minHeight = 300;

                    if (isResizing.includes('e')) {
                        setSize(s => ({ ...s, width: Math.max(minWidth, e.clientX - position.x) }));
                    }
                    if (isResizing.includes('s')) {
                        setSize(s => ({
                            ...s,
                            height: Math.max(minHeight, e.clientY - position.y),
                        }));
                    }
                    if (isResizing.includes('w')) {
                        const newWidth = Math.max(minWidth, size.width + (position.x - e.clientX));
                        if (newWidth > minWidth) {
                            setPosition(p => ({ ...p, x: e.clientX }));
                            setSize(s => ({ ...s, width: newWidth }));
                        }
                    }
                    if (isResizing.includes('n')) {
                        const newHeight = Math.max(
                            minHeight,
                            size.height + (position.y - e.clientY)
                        );
                        if (newHeight > minHeight) {
                            setPosition(p => ({ ...p, y: e.clientY }));
                            setSize(s => ({ ...s, height: newHeight }));
                        }
                    }
                    resizeFrameId.current = null;
                });
            }
        };

        const handleMouseUp = () => {
            setIsResizing(null);
            if (resizeFrameId.current) {
                cancelAnimationFrame(resizeFrameId.current);
                resizeFrameId.current = null;
            }
        };

        window.addEventListener('mousemove', handleMouseMove);
        window.addEventListener('mouseup', handleMouseUp);
        return () => {
            window.removeEventListener('mousemove', handleMouseMove);
            window.removeEventListener('mouseup', handleMouseUp);
            if (resizeFrameId.current) {
                cancelAnimationFrame(resizeFrameId.current);
                resizeFrameId.current = null;
            }
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
            case 'light':
                return oneLight;
            case 'gruvbox-dark':
                return gruvboxDark;
            case 'gruvbox-light':
                return gruvboxLight;
            case 'solarized-light':
                return solarizedlight;
            case 'solarized-dark':
                return solarizedDarkAtom;
            case 'dracula':
                return dracula;
            case 'nord':
                return nord;
            case 'night-owl':
                return nightOwl;
            default:
                return oneDark;
        }
    }, [theme]);

    const toggleDir = useCallback(
        (path: string) => {
            const newExpanded = new Set(expandedDirs);
            if (newExpanded.has(path)) {
                newExpanded.delete(path);
            } else {
                newExpanded.add(path);
            }
            setExpandedDirs(newExpanded);
        },
        [expandedDirs]
    );

    const renderFileTree = useCallback(
        (nodes: FileNode[], depth: number = 0): React.ReactNode => {
            return nodes.map(node => (
                <div key={node.path}>
                    <div
                        style={{
                            padding: '4px 8px',
                            paddingLeft: `${depth * 12 + 8}px`,
                            cursor: 'pointer',
                            display: 'flex',
                            alignItems: 'center',
                            gap: '6px',
                            fontSize: '12px',
                            background:
                                currentFilePath === node.path ||
                                currentFilePath.endsWith('/' + node.path)
                                    ? 'var(--bg-tertiary)'
                                    : 'transparent',
                            color:
                                currentFilePath === node.path ||
                                currentFilePath.endsWith('/' + node.path)
                                    ? 'var(--accent)'
                                    : 'var(--text-primary)',
                            userSelect: 'none',
                            transition: 'background 0.1s ease',
                        }}
                        onClick={() => {
                            if (node.isDirectory) {
                                toggleDir(node.path);
                            } else {
                                setCurrentFilePath(node.path);
                            }
                        }}
                        onMouseEnter={e =>
                            (e.currentTarget.style.background = 'var(--bg-tertiary)')
                        }
                        onMouseLeave={e =>
                            (e.currentTarget.style.background =
                                currentFilePath === node.path ||
                                currentFilePath.endsWith('/' + node.path)
                                    ? 'var(--bg-tertiary)'
                                    : 'transparent')
                        }
                    >
                        <span style={{ fontSize: '14px', width: '16px', textAlign: 'center' }}>
                            {node.isDirectory ? (expandedDirs.has(node.path) ? '▾' : '▸') : ''}
                        </span>
                        <span style={{ fontSize: '14px' }}>
                            {getIconFromFilename(node.name, node.isDirectory)}
                        </span>
                        <span
                            style={{
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                            }}
                        >
                            {node.name}
                        </span>
                    </div>
                    {node.isDirectory && expandedDirs.has(node.path) && node.children && (
                        <div>{renderFileTree(node.children, depth + 1)}</div>
                    )}
                </div>
            ));
        },
        [expandedDirs, currentFilePath, toggleDir]
    );

    if (!isOpen) return null;

    const displayFilename = currentFilePath.split('/').pop() || currentFilePath;

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
                    top: 0,
                    left: 0,
                    transform: `translate3d(${position.x}px, ${position.y}px, 0)`,
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
                    willChange: isDragging || isResizing ? 'transform' : 'auto',
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
                    <div
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '8px',
                            overflow: 'hidden',
                        }}
                    >
                        <button
                            onClick={() => setShowFileTree(!showFileTree)}
                            style={{
                                background: 'var(--bg-secondary)',
                                border: '1px solid var(--border)',
                                borderRadius: '4px',
                                padding: '4px 8px',
                                color: 'var(--text-primary)',
                                fontSize: '12px',
                                cursor: 'pointer',
                                display: 'flex',
                                alignItems: 'center',
                                gap: '4px',
                            }}
                        >
                            {showFileTree ? '◀ Hide Tree' : '▶ Files'}
                        </button>
                        <span style={{ fontSize: '16px', marginLeft: '8px' }}>
                            {getIconFromFilename(displayFilename, false)}
                        </span>
                        <span
                            style={{
                                fontFamily: 'var(--font-mono)',
                                fontSize: '13px',
                                color: 'var(--text-primary)',
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                whiteSpace: 'nowrap',
                            }}
                            title={currentFilePath}
                        >
                            {displayFilename}
                        </span>
                        {currentFilePath === filePath && initialLine && (
                            <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                                (line {initialLine})
                            </span>
                        )}
                        {lsp.connected && (
                            <span
                                style={{
                                    fontSize: '10px',
                                    padding: '2px 6px',
                                    borderRadius: '4px',
                                    background: colors.bgSuccessDim,
                                    color: colors.success,
                                    fontWeight: 600,
                                }}
                                title="Language server connected"
                            >
                                LSP
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

                {/* Sidebar and Content Container */}
                <div style={{ display: 'flex', flex: 1, overflow: 'hidden' }}>
                    {/* Sidebar */}
                    {showFileTree && (
                        <div
                            className="custom-scrollbar"
                            style={{
                                width: '250px',
                                minWidth: '150px',
                                borderRight: '1px solid var(--border)',
                                background: 'var(--bg-secondary)',
                                overflowY: 'auto',
                                display: 'flex',
                                flexDirection: 'column',
                                animation: 'slideIn 0.2s ease-out',
                            }}
                        >
                            <style>{`
                                @keyframes slideIn {
                                    from { width: 0; opacity: 0; }
                                    to { width: 250px; opacity: 1; }
                                }
                                .custom-scrollbar::-webkit-scrollbar {
                                    width: 10px;
                                    height: 10px;
                                }
                                .custom-scrollbar::-webkit-scrollbar-track {
                                    background: var(--bg-secondary);
                                }
                                .custom-scrollbar::-webkit-scrollbar-thumb {
                                    background: var(--border);
                                    border-radius: 5px;
                                    border: 2px solid var(--bg-secondary);
                                }
                                .custom-scrollbar::-webkit-scrollbar-thumb:hover {
                                    background: var(--text-tertiary);
                                }
                                /* Firefox */
                                .custom-scrollbar {
                                    scrollbar-width: thin;
                                    scrollbar-color: var(--border) var(--bg-secondary);
                                }
                            `}</style>
                            <div
                                style={{
                                    padding: '10px 12px',
                                    fontSize: '11px',
                                    fontWeight: 700,
                                    color: 'var(--text-tertiary)',
                                    borderBottom: '1px solid var(--border)',
                                    background: 'var(--bg-tertiary)',
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.5px',
                                }}
                            >
                                Project Explorer
                            </div>
                            <div
                                className="custom-scrollbar"
                                style={{ flex: 1, overflowY: 'auto', padding: '8px 0' }}
                            >
                                {renderFileTree(fileTree)}
                            </div>
                        </div>
                    )}

                    {/* Main Content */}
                    <div
                        ref={contentRef}
                        className="custom-scrollbar"
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
                            <div style={{ position: 'relative' }}>
                                <SyntaxHighlighter
                                    language={language}
                                    style={syntaxTheme}
                                    showLineNumbers={true}
                                    wrapLines={true}
                                    lineProps={(lineNumber: number) => {
                                        const isInitialTarget =
                                            currentFilePath === filePath &&
                                            lineNumber === initialLine;
                                        const isScrollTarget =
                                            scrollToLine !== null && lineNumber === scrollToLine;
                                        const isTargetLine = isInitialTarget || isScrollTarget;
                                        const isLspActive = activeLspLine === lineNumber;
                                        return {
                                            style: {
                                                display: 'block',
                                                position: 'relative' as const,
                                                background: isLspActive
                                                    ? colors.bgInfoDim
                                                    : isTargetLine
                                                      ? colors.bgWarningDim
                                                      : 'transparent',
                                                borderLeft: isLspActive
                                                    ? `3px solid var(--accent)`
                                                    : isTargetLine
                                                      ? `3px solid ${colors.warning}`
                                                      : '3px solid transparent',
                                                paddingLeft: '8px',
                                                cursor: lsp.available ? 'pointer' : 'default',
                                            },
                                            onClick: (e: React.MouseEvent) => {
                                                if (!lsp.available) return;
                                                const col = getClickColumn(
                                                    e,
                                                    e.currentTarget as HTMLElement
                                                );
                                                const lspLine = lineNumber - 1; // 0-indexed for LSP
                                                if (activeLspLine === lineNumber) {
                                                    setActiveLspLine(null);
                                                    lsp.clearData();
                                                } else {
                                                    lsp.query(lspLine, col).then(result => {
                                                        if (result) {
                                                            setActiveLspLine(lineNumber);
                                                        }
                                                    });
                                                }
                                            },
                                        };
                                    }}
                                    lineNumberStyle={{
                                        minWidth: '50px',
                                        paddingRight: '8px',
                                        textAlign: 'right',
                                        color: 'var(--text-tertiary)',
                                        background: 'var(--bg-secondary)',
                                        borderRight: '1px solid var(--border)',
                                        userSelect: 'none',
                                    }}
                                    customStyle={{
                                        margin: 0,
                                        padding: 0,
                                        background: 'transparent',
                                        fontFamily: 'var(--font-mono)',
                                        fontSize: '13px',
                                    }}
                                >
                                    {content}
                                </SyntaxHighlighter>
                                {activeLspLine !== null && lsp.lspData && (
                                    <div
                                        style={{
                                            position: 'absolute',
                                            top: `${activeLspLine * 20 + 20}px`,
                                            left: '60px',
                                            zIndex: 100,
                                        }}
                                    >
                                        <LspPopover
                                            hover={lsp.lspData.hover}
                                            refs={lsp.lspData.refs}
                                            definitions={lsp.lspData.definitions}
                                            typeDefinitions={lsp.lspData.typeDefinitions}
                                            variant="floating"
                                            onRefClick={r => {
                                                const refPath = r.uri.replace('file://', '');
                                                const relativePath = refPath.startsWith(
                                                    repoPath + '/'
                                                )
                                                    ? refPath.slice(repoPath.length + 1)
                                                    : refPath;
                                                const targetLine = r.range.start.line + 1;
                                                setCurrentFilePath(relativePath);
                                                setScrollToLine(targetLine);
                                                setActiveLspLine(null);
                                                lsp.clearData();
                                            }}
                                            onClose={() => {
                                                setActiveLspLine(null);
                                                lsp.clearData();
                                            }}
                                        />
                                    </div>
                                )}
                            </div>
                        )}
                    </div>
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
