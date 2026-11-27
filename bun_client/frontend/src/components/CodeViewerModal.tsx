import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import { colors, shadows } from '../design';
import { getSyntaxTheme } from './review/diff_theme';
import { readFile, listFiles } from '../api';
import type { Theme } from '../design';
import { useFileLsp } from '../hooks/useFileLsp';
import { useIsMobile } from '../hooks/useMediaQuery';
import type { LspLocation } from '../lsp';
import {
    canGoBack,
    canGoForward,
    currentEntry,
    goBack,
    goForward,
    type NavEntry,
    navigate,
    pathInCheckout,
    startHistory,
    uriToPath,
} from '../lsp_utils';
import LspPopover from './LspPopover';
import SymbolsPanel from './SymbolsPanel';
import VirtualizedCodeBlock, { CODE_LINE_HEIGHT } from './VirtualizedCodeBlock';

interface CodeViewerModalProps {
    isOpen: boolean;
    onClose: () => void;
    filePath: string;
    repoPath: string;
    initialLine?: number;
    theme: Theme;
    initialPosition?: { x: number; y: number };
    onDock?: () => void;
    onUndock?: () => void;
    docked?: boolean;
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

const navButtonStyle = (enabled: boolean): React.CSSProperties => ({
    background: 'transparent',
    border: '1px solid var(--border)',
    borderRadius: '4px',
    padding: '2px 7px',
    color: enabled ? 'var(--text-primary)' : 'var(--text-tertiary)',
    fontSize: '13px',
    cursor: enabled ? 'pointer' : 'default',
    opacity: enabled ? 1 : 0.5,
    lineHeight: 1.3,
});

const toggleButtonStyle = (active: boolean): React.CSSProperties => ({
    background: active ? 'var(--bg-tertiary)' : 'var(--bg-secondary)',
    border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
    borderRadius: '4px',
    padding: '4px 8px',
    color: 'var(--text-primary)',
    fontSize: '12px',
    cursor: 'pointer',
    display: 'flex',
    alignItems: 'center',
    gap: '4px',
    whiteSpace: 'nowrap',
});

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
    onDock,
    onUndock,
    docked = false,
}: CodeViewerModalProps) {
    // Where the viewer is, and where it has been: Back and Forward walk
    // this like an editor's jump list. Paths in the checkout are kept
    // relative to it, whichever way they came (the diff hands over absolute
    // ones), so one file is one path.
    const [history, setHistory] = useState(() =>
        startHistory({ path: pathInCheckout(repoPath, filePath), line: initialLine || null })
    );
    // Bumped on every jump, so going back to a line already on show still
    // scrolls to it.
    const [jumps, setJumps] = useState(0);
    const here = currentEntry(history);
    const currentFilePath = here.path;
    // The file's text, with the path it was read from: while the next file
    // loads, the last one's text mustn't pass for it.
    const [loaded, setLoaded] = useState<{ path: string; content: string } | null>(null);
    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    // A note in the header, e.g. that a Ctrl+click found no definition.
    const [notice, setNotice] = useState<string | null>(null);

    const [allFiles, setAllFiles] = useState<string[]>([]);
    const [sidebar, setSidebar] = useState<'files' | 'symbols' | null>(null);
    const [expandedDirs, setExpandedDirs] = useState<Set<string>>(new Set());

    // On phones the floating/draggable layout can't fit, so we render the
    // viewer full-screen and disable dragging/resizing.
    const isMobile = useIsMobile();

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

    const content = loaded && loaded.path === currentFilePath ? loaded.content : null;
    const absolutePath = currentFilePath.startsWith('/')
        ? currentFilePath
        : `${repoPath.replace(/\/$/, '')}/${currentFilePath}`;

    // The file's own language server (gopls, rust-analyzer, pyright,
    // typescript-language-server), rooted at its project.
    const lsp = useFileLsp({
        checkout: repoPath,
        filePath: absolutePath,
        text: content,
        enabled: isOpen,
    });

    // Clear LSP state on file change
    useEffect(() => {
        setActiveLspLine(null);
        lsp.clearData();
    }, [currentFilePath]);

    useEffect(() => {
        if (!notice) return;
        const timer = setTimeout(() => setNotice(null), 2500);
        return () => clearTimeout(timer);
    }, [notice]);

    /** Moves to a file and line, remembering where the jump was made from. */
    const jumpTo = useCallback(
        (to: NavEntry, fromLine?: number | null) => {
            setHistory(h =>
                navigate(h, { ...to, path: pathInCheckout(repoPath, to.path) }, fromLine)
            );
            setJumps(n => n + 1);
            setActiveLspLine(null);
            lsp.clearData();
        },
        [lsp.clearData, repoPath]
    );

    const jumpToLocation = useCallback(
        (location: LspLocation, fromLine?: number | null) =>
            jumpTo(
                { path: uriToPath(location.uri), line: location.range.start.line + 1 },
                fromLine
            ),
        [jumpTo]
    );

    const walkHistory = useCallback(
        (step: typeof goBack) => {
            setHistory(step);
            setJumps(n => n + 1);
            setActiveLspLine(null);
            lsp.clearData();
        },
        [lsp.clearData]
    );

    const modalRef = useRef<HTMLDivElement>(null);
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

    // Another file handed in by the review (a link clicked there) is a jump too.
    const firstRender = useRef(true);
    useEffect(() => {
        if (firstRender.current) {
            firstRender.current = false;
            return;
        }
        jumpTo({ path: filePath, line: initialLine || null });
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
        if (loaded?.path === currentFilePath) {
            // Back to the file already loaded, before another one arrived.
            setLoading(false);
            setError(null);
            return;
        }
        let cancelled = false;

        const fetchContent = async () => {
            setLoading(true);
            setError(null);
            try {
                // Relative to repoPath from the tree and the checkout; absolute
                // for a definition outside it (a dependency, the standard
                // library), which server.ts reads as is.
                const text = await readFile(repoPath, currentFilePath);
                if (!cancelled) setLoaded({ path: currentFilePath, content: text });
            } catch (e) {
                if (!cancelled) setError(e instanceof Error ? e.message : 'Failed to load file');
            } finally {
                if (!cancelled) setLoading(false);
            }
        };

        fetchContent();
        return () => {
            cancelled = true;
        };
    }, [isOpen, currentFilePath, repoPath]);

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
            // Highlight dock zone while dragging
            if (onDock) {
                const dockZone = document.querySelector('[data-dock-zone]');
                if (dockZone) {
                    const rect = dockZone.getBoundingClientRect();
                    const isOver =
                        e.clientX >= rect.left &&
                        e.clientX <= rect.right &&
                        e.clientY >= rect.top &&
                        e.clientY <= rect.bottom;
                    dockZone.setAttribute('data-dock-hover', isOver ? 'true' : 'false');
                }
            }
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

        const handleMouseUp = (e: MouseEvent) => {
            setIsDragging(false);
            if (animationFrameId.current) {
                cancelAnimationFrame(animationFrameId.current);
                animationFrameId.current = null;
            }
            // Check if dropped on dock zone
            if (onDock) {
                const dockZone = document.querySelector('[data-dock-zone]');
                if (dockZone) {
                    dockZone.setAttribute('data-dock-hover', 'false');
                    const rect = dockZone.getBoundingClientRect();
                    const isOver =
                        e.clientX >= rect.left &&
                        e.clientX <= rect.right &&
                        e.clientY >= rect.top &&
                        e.clientY <= rect.bottom;
                    if (isOver) {
                        onDock();
                        return;
                    }
                }
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
            // Clean up dock hover state
            const dockZone = document.querySelector('[data-dock-zone]');
            if (dockZone) dockZone.setAttribute('data-dock-hover', 'false');
        };
    }, [isDragging, dragOffset, onDock]);

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
    const syntaxTheme = useMemo(() => getSyntaxTheme(theme), [theme]);

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

    // Ctrl/Cmd+click: straight to the definition, as in an editor. Several
    // (an overloaded or re-declared name) open the popover to pick from.
    const goToDefinition = useCallback(
        async (line: number, column: number) => {
            const definitions = await lsp.definitionAt(line - 1, column);
            if (!definitions) {
                setNotice('No definition found');
                return;
            }
            if (definitions.length === 1) {
                jumpToLocation(definitions[0], line);
                return;
            }
            setActiveLspLine(line);
            lsp.query(line - 1, column);
        },
        [lsp.definitionAt, lsp.query, jumpToLocation]
    );

    // Toggle an LSP query for the clicked line/column.
    const handleLspLineClick = useCallback(
        (line: number, column: number, e?: React.MouseEvent) => {
            if (!lsp.connected) return;
            if (e && (e.metaKey || e.ctrlKey)) {
                goToDefinition(line, column);
                return;
            }
            if (activeLspLine === line) {
                setActiveLspLine(null);
                lsp.clearData();
                return;
            }
            // Mark the line at once; the popover opens as answers arrive.
            setActiveLspLine(line);
            lsp.query(line - 1, column).then(result => {
                if (result === 'empty') setActiveLspLine(cur => (cur === line ? null : cur));
            });
        },
        [lsp, activeLspLine, goToDefinition]
    );

    // Alt+← / Alt+→ walk the history while the viewer has focus (a click in
    // it gives it focus), as Back and Forward do in an editor.
    const handleKeyDown = useCallback(
        (e: React.KeyboardEvent) => {
            if (!e.altKey || (e.target as HTMLElement).closest('input, textarea')) return;
            if (e.key === 'ArrowLeft' && canGoBack(history)) {
                e.preventDefault();
                walkHistory(goBack);
            } else if (e.key === 'ArrowRight' && canGoForward(history)) {
                e.preventDefault();
                walkHistory(goForward);
            }
        },
        [history, walkHistory]
    );

    const targetLine = here.line;

    // Single highlighted, virtualization-aware file view shared by both the
    // docked and floating layouts.
    const codeViewer =
        !error && content !== null ? (
            <VirtualizedCodeBlock
                content={content}
                language={language}
                syntaxStyle={syntaxTheme}
                activeLine={activeLspLine}
                targetLine={targetLine}
                scrollToLine={targetLine}
                scrollKey={jumps}
                clickable={lsp.connected}
                onLineClick={handleLspLineClick}
            >
                {activeLspLine !== null && lsp.lspData && (
                    <div
                        style={{
                            position: 'absolute',
                            top: `${activeLspLine * CODE_LINE_HEIGHT + CODE_LINE_HEIGHT}px`,
                            left: '60px',
                            zIndex: 100,
                        }}
                    >
                        <LspPopover
                            data={lsp.lspData}
                            variant="floating"
                            onRefClick={r => jumpToLocation(r, activeLspLine)}
                            onClose={() => {
                                setActiveLspLine(null);
                                lsp.clearData();
                            }}
                        />
                    </div>
                )}
            </VirtualizedCodeBlock>
        ) : null;

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
                                jumpTo({ path: node.path, line: null });
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
        [expandedDirs, currentFilePath, toggleDir, jumpTo]
    );

    if (!isOpen) return null;

    const displayFilename = currentFilePath.split('/').pop() || currentFilePath;

    const sidebarContent = sidebar && (
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
                {sidebar === 'files' ? 'Project Explorer' : 'Symbols'}
            </div>
            {sidebar === 'files' ? (
                <div
                    className="custom-scrollbar"
                    style={{ flex: 1, overflowY: 'auto', padding: '8px 0' }}
                >
                    {renderFileTree(fileTree)}
                </div>
            ) : (
                <SymbolsPanel
                    available={lsp.available}
                    symbols={lsp.symbols}
                    roots={[repoPath, lsp.root]}
                    searchWorkspace={lsp.searchWorkspace}
                    onPickLine={line => jumpTo({ path: currentFilePath, line })}
                    onPickLocation={location => jumpToLocation(location)}
                />
            )}
        </div>
    );

    const headerContent = (
        <>
            {/* Header */}
            <div
                style={{
                    padding: '12px 16px',
                    background: 'var(--bg-tertiary)',
                    borderBottom: '1px solid var(--border)',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    cursor: docked || isMobile ? 'default' : 'move',
                    userSelect: 'none',
                }}
                onMouseDown={docked || isMobile ? undefined : startDrag}
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
                        onClick={() => walkHistory(goBack)}
                        disabled={!canGoBack(history)}
                        title="Back (Alt+←)"
                        aria-label="Back"
                        style={navButtonStyle(canGoBack(history))}
                    >
                        ←
                    </button>
                    <button
                        onClick={() => walkHistory(goForward)}
                        disabled={!canGoForward(history)}
                        title="Forward (Alt+→)"
                        aria-label="Forward"
                        style={navButtonStyle(canGoForward(history))}
                    >
                        →
                    </button>
                    <button
                        onClick={() => setSidebar(sidebar === 'files' ? null : 'files')}
                        aria-pressed={sidebar === 'files'}
                        style={toggleButtonStyle(sidebar === 'files')}
                    >
                        {sidebar === 'files' ? '◀ Hide Tree' : '▶ Files'}
                    </button>
                    <button
                        onClick={() => setSidebar(sidebar === 'symbols' ? null : 'symbols')}
                        aria-pressed={sidebar === 'symbols'}
                        title="This file's declarations, and a search across the workspace"
                        style={toggleButtonStyle(sidebar === 'symbols')}
                    >
                        Symbols
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
                    {here.line && (
                        <span style={{ fontSize: '12px', color: 'var(--text-secondary)' }}>
                            (line {here.line})
                        </span>
                    )}
                    {lsp.connected && (
                        <span
                            style={{
                                fontSize: '10px',
                                padding: '2px 6px',
                                borderRadius: '4px',
                                background: lsp.progress
                                    ? colors.bgWarningDim
                                    : colors.bgSuccessDim,
                                color: lsp.progress ? colors.warning : colors.success,
                                fontWeight: 600,
                                whiteSpace: 'nowrap',
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                maxWidth: '260px',
                            }}
                            title={`Language server connected: ${lsp.server}, rooted at ${lsp.root}${
                                lsp.progress ? ` (${lsp.progress})` : ''
                            }. Click a symbol for its details, Ctrl/⌘+click to go to its definition.`}
                        >
                            {lsp.server || 'LSP'}
                            {lsp.progress ? ` · ${lsp.progress}` : ''}
                        </span>
                    )}
                    {notice && (
                        <span
                            role="status"
                            style={{ fontSize: '12px', color: 'var(--text-secondary)' }}
                        >
                            {notice}
                        </span>
                    )}
                </div>
                <div style={{ display: 'flex', alignItems: 'center', gap: '4px' }}>
                    {docked && onUndock && (
                        <button
                            onClick={onUndock}
                            title="Pop out as floating window"
                            style={{
                                background: 'transparent',
                                border: 'none',
                                color: 'var(--text-secondary)',
                                fontSize: '14px',
                                cursor: 'pointer',
                                padding: '2px 6px',
                                lineHeight: 1,
                                borderRadius: '4px',
                            }}
                        >
                            ↗
                        </button>
                    )}
                    {!docked && onDock && (
                        <button
                            onClick={onDock}
                            title="Pin as tab in review pane"
                            style={{
                                background: 'transparent',
                                border: 'none',
                                color: 'var(--text-secondary)',
                                fontSize: '14px',
                                cursor: 'pointer',
                                padding: '2px 6px',
                                lineHeight: 1,
                                borderRadius: '4px',
                            }}
                        >
                            ⊞
                        </button>
                    )}
                    <button
                        onClick={onClose}
                        aria-label="Close viewer"
                        title="Close"
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
            </div>
        </>
    );

    // Docked mode: render inline filling its container
    if (docked) {
        return (
            <div
                ref={modalRef}
                tabIndex={-1}
                onKeyDown={handleKeyDown}
                style={{
                    display: 'flex',
                    flexDirection: 'column',
                    height: '100%',
                    background: 'var(--bg-secondary)',
                    overflow: 'hidden',
                    outline: 'none',
                }}
            >
                {headerContent}

                {/* Sidebar and Content Container */}
                <div style={{ display: 'flex', flex: 1, overflow: 'hidden' }}>
                    {sidebarContent}

                    {/* Main Content */}
                    <div
                        style={{
                            flex: 1,
                            overflow: 'hidden',
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
                        {codeViewer}
                    </div>
                </div>
            </div>
        );
    }

    // Floating mode (default)
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
                tabIndex={-1}
                onKeyDown={handleKeyDown}
                style={{
                    outline: 'none',
                    position: 'absolute',
                    top: 0,
                    left: 0,
                    transform: isMobile
                        ? 'none'
                        : `translate3d(${position.x}px, ${position.y}px, 0)`,
                    width: isMobile ? '100%' : size.width,
                    height: isMobile ? '100%' : size.height,
                    background: 'var(--bg-secondary)',
                    border: isMobile ? 'none' : '1px solid var(--border)',
                    borderRadius: isMobile ? 0 : '8px',
                    boxShadow: isMobile ? 'none' : shadows.lg,
                    display: 'flex',
                    flexDirection: 'column',
                    pointerEvents: 'auto',
                    overflow: 'hidden',
                    willChange: isDragging || isResizing ? 'transform' : 'auto',
                }}
            >
                {headerContent}

                {/* Sidebar and Content Container */}
                <div style={{ display: 'flex', flex: 1, overflow: 'hidden' }}>
                    {sidebarContent}

                    {/* Main Content */}
                    <div
                        style={{
                            flex: 1,
                            overflow: 'hidden',
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
                        {codeViewer}
                    </div>
                </div>

                {/* Resize handles — hidden on mobile (full-screen, no resize) */}
                {!isMobile && (
                    <>
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
                    </>
                )}
            </div>
        </div>
    );
}
