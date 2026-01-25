import { useState, useEffect, useMemo } from 'react';
import Markdown from 'react-markdown';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { oneDark, oneLight, gruvboxDark, gruvboxLight, solarizedlight, solarizedDarkAtom, dracula, nord, nightOwl } from 'react-syntax-highlighter/dist/esm/styles/prism';
import { rpcCall } from '../api';
import { Button, Badge, Modal, TextArea, Select, mapStatusToVariant, colors, shadows, Theme, THEME_OPTIONS } from '../design';
import { LspClient, LspHover, LspLocation } from '../lsp';

// Strip HTML comments from text (e.g., <!-- comment -->)
const stripHtmlComments = (text: string): string => {
    return text.replace(/<!--[\s\S]*?-->/g, '');
};

interface ReviewProps {
    owner: string;
    repo: string;
    number: number;
    theme: Theme;
    onThemeChange: (theme: Theme) => void;
}

interface Comment {
    id: string;
    author: string;
    body: string;
    path: string;
    position: string;
    in_reply_to: number;
    created_at: string;
    outdated: boolean;
}

interface PluginResult {
    result: string;
    status: string;
}

interface PRMetadata {
    number: number;
    title: string;
    author: string;
    base_ref: string;
    head_ref: string;
    state: string;
    milestone: string;
    labels: string[];
    assignees: string[];
    reviewers: string[];           // Requested individual reviewers
    requested_teams: string[];     // Requested team reviewers
    approved_by: string[];         // Users who approved
    changes_requested_by: string[]; // Users who requested changes
    commented_by: string[];         // Users who commented
    draft: boolean;
    ci_status: string;
    ci_failures: string[];
    body: string;
    url: string;
}

interface PRResponse {
    content: string;
    diff: string;
    comments: Comment[];
    outdated_comments: Comment[];
    metadata: PRMetadata;
}

// Map file extensions to Prism language identifiers
const getLanguageFromFilename = (filename: string): string => {
    const ext = filename.split('.').pop()?.toLowerCase() || '';
    const languageMap: Record<string, string> = {
        // JavaScript/TypeScript
        'js': 'javascript',
        'jsx': 'jsx',
        'ts': 'typescript',
        'tsx': 'tsx',
        'mjs': 'javascript',
        'cjs': 'javascript',
        // Web
        'html': 'html',
        'htm': 'html',
        'css': 'css',
        'scss': 'scss',
        'sass': 'sass',
        'less': 'less',
        'json': 'json',
        'xml': 'xml',
        'svg': 'xml',
        // Backend
        'py': 'python',
        'rb': 'ruby',
        'go': 'go',
        'rs': 'rust',
        'java': 'java',
        'kt': 'kotlin',
        'scala': 'scala',
        'php': 'php',
        'c': 'c',
        'h': 'c',
        'cpp': 'cpp',
        'cc': 'cpp',
        'cxx': 'cpp',
        'hpp': 'cpp',
        'cs': 'csharp',
        'swift': 'swift',
        // Shell/Config
        'sh': 'bash',
        'bash': 'bash',
        'zsh': 'bash',
        'fish': 'bash',
        'ps1': 'powershell',
        'yaml': 'yaml',
        'yml': 'yaml',
        'toml': 'toml',
        'ini': 'ini',
        'conf': 'ini',
        // Data/Docs
        'md': 'markdown',
        'mdx': 'markdown',
        'sql': 'sql',
        'graphql': 'graphql',
        'gql': 'graphql',
        // Other
        'dockerfile': 'docker',
        'makefile': 'makefile',
        'lua': 'lua',
        'r': 'r',
        'dart': 'dart',
        'ex': 'elixir',
        'exs': 'elixir',
        'erl': 'erlang',
        'hrl': 'erlang',
        'clj': 'clojure',
        'vim': 'vim',
        'el': 'lisp',
        'lisp': 'lisp',
        'hs': 'haskell',
        'ml': 'ocaml',
        'proto': 'protobuf',
    };

    // Handle special filenames
    const basename = filename.split('/').pop()?.toLowerCase() || '';
    if (basename === 'dockerfile') return 'docker';
    if (basename === 'makefile' || basename === 'gnumakefile') return 'makefile';
    if (basename.endsWith('.d.ts')) return 'typescript';

    return languageMap[ext] || 'text';
};

export default function Review({ owner, repo, number, theme, onThemeChange }: ReviewProps) {
    const [content, setContent] = useState<string>('');
    const [diff, setDiff] = useState<string>('');
    const [comments, setComments] = useState<Comment[]>([]);
    const [outdatedComments, setOutdatedComments] = useState<Comment[]>([]);
    const [metadata, setMetadata] = useState<PRMetadata | null>(null);
    const [pluginOutputs, setPluginOutputs] = useState<Record<string, PluginResult>>({});
    const [loading, setLoading] = useState(false);

    // UI State
    const [showCommentModal, setShowCommentModal] = useState(false); // For general comments
    const [activeLineIndex, setActiveLineIndex] = useState<number | null>(null); // For inline comments
    const [activeLspIndex, setActiveLspIndex] = useState<number | null>(null); // For LSP display
    const [showPlugins, setShowPlugins] = useState(false);
    const [collapsedFiles, setCollapsedFiles] = useState<Set<string>>(new Set());
    const [activeOutdatedFile, setActiveOutdatedFile] = useState<string | null>(null);

    const [submitting, setSubmitting] = useState(false);

    // Comment form data
    const [filename, setFilename] = useState('');
    const [position, setPosition] = useState('');
    const [commentBody, setCommentBody] = useState('');
    const [replyToId, setReplyToId] = useState<number | null>(null);

    // Submit form
    const [reviewBody, setReviewBody] = useState('');
    const [reviewEvent, setReviewEvent] = useState('COMMENT');

    // LSP State
    const [lspClient, setLspClient] = useState<LspClient | null>(null);
    const [lspUri, setLspUri] = useState<string | null>(null);
    const [lspData, setLspData] = useState<{ hover: LspHover | null, refs: LspLocation[] | null } | null>(null);
    const [repoExists, setRepoExists] = useState<boolean | null>(null);

    useEffect(() => {
        loadPR();
        loadPluginOutputs();
    }, [owner, repo, number]);

    useEffect(() => {
        if (!diff || !metadata) return;

        const initLsp = async () => {
            try {
                const checkRes = await rpcCall<{ Exists: boolean; Path: string }>('RPCHandler.CheckRepoExists', [{ Repo: repo }]);
                setRepoExists(checkRes.Exists);

                if (!checkRes.Exists) {
                    console.log(`Repo ${repo} not found locally, skipping LSP initialization.`);
                    return;
                }

                if (lspClient) {
                    // Simple cleanup if needed, though we make a new one
                }
                const client = new LspClient();
                client.connect();

                // Prepare context for diff-lsp
                // We use "code-review" as type to match server configuration
                const path = await client.prepareContext(
                    repo,
                    checkRes.Path,
                    `PR #${number}`,
                    "code-review",
                    diff
                );
                const uri = `file://${path}`;
                setLspUri(uri);

                // Initialize standard LSP
                console.log("LSP Initializing...");
                try {
                    await Promise.race([
                        client.initialize("/"),
                        new Promise((_, reject) => setTimeout(() => reject(new Error("LSP Initialize timeout")), 5000))
                    ]);
                    console.log("LSP Initialize success");
                } catch (e) {
                    console.error("LSP Initialize error/timeout", e);
                }
                client.initialized();

                // Open the document (the temp file we just created)
                console.log("Sending didOpen for", uri);
                await client.didOpen(uri, "diff", 1, diff);

                setLspClient(client);
            } catch (e) {
                console.error("LSP Init failed", e);
            }
        };

        initLsp();

        return () => {
            // connection clean up handled by browser closing ws or GC
        };
    }, [diff, owner, repo, number]); // Re-init if diff changes

    useEffect(() => {
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                setShowPlugins(false);
                setShowCommentModal(false);
                setSubmitting(false);
                setActiveLineIndex(null);
                setActiveLspIndex(null);
                setActiveOutdatedFile(null);
            }
        };
        window.addEventListener('keydown', handleKeyDown);
        return () => window.removeEventListener('keydown', handleKeyDown);
    }, []);

    const loadPluginOutputs = async () => {
        try {
            const res = await rpcCall<{ output: Record<string, PluginResult> }>('RPCHandler.GetPluginOutput', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setPluginOutputs(res.output || {});
        } catch (e) {
            console.error("Failed to load plugin outputs:", e);
        }
    };

    const loadPR = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.GetPR', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setMetadata(res.metadata || null);
        } catch (e) {
            console.error(e);
            setContent('Error loading PR.');
        } finally {
            setLoading(false);
        }
    };

    const handleSync = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.SyncPR', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setMetadata(res.metadata || null);
            loadPluginOutputs();
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    };

    const resetCommentForm = () => {
        setFilename('');
        setPosition('');
        setCommentBody('');
        setReplyToId(null);
        setShowCommentModal(false);
        setActiveLineIndex(null);
        // Do not reset LSP index here as users might want to keep references open while adding a comment
    };

    const handleAddComment = async () => {
        if (!filename || !commentBody) return;
        try {
            const params: any = {
                Owner: owner,
                Repo: repo,
                Number: number,
                Filename: filename,
                Position: parseInt(position, 10) || 0,
                Body: commentBody
            };
            if (replyToId !== null) {
                params.ReplyToID = replyToId;
            }
            const res = await rpcCall<PRResponse>('RPCHandler.AddComment', [params]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            resetCommentForm();
        } catch (e) {
            console.error(e);
            alert('Error adding comment');
        }
    };

    const handleSubmitReview = async () => {
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.SubmitReview', [{
                Owner: owner,
                Repo: repo,
                Number: number,
                Event: reviewEvent,
                Body: reviewBody
            }]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setSubmitting(false);
        } catch (e) {
            console.error(e);
            alert('Error submitting review');
        }
    };

    // File collapse handlers
    const toggleFileCollapse = (filename: string) => {
        setCollapsedFiles(prev => {
            const newSet = new Set(prev);
            if (newSet.has(filename)) {
                newSet.delete(filename);
            } else {
                newSet.add(filename);
            }
            return newSet;
        });
    };

    const collapseAllFiles = () => {
        const lines = parseDiff();
        const allFiles = new Set(
            lines
                .filter(line => line.lineType === 'file-header' && line.file)
                .map(line => line.file!)
        );
        setCollapsedFiles(allFiles);
    };

    const expandAllFiles = () => {
        setCollapsedFiles(new Set());
    };

    // Line types for rendering
    type LineType = 'code' | 'addition' | 'deletion' | 'hunk' | 'file-header' | 'skip';

    interface ParsedLine {
        text: string;
        file: string | null;
        pos: number | null;
        clickable: boolean;
        lineType: LineType;
        fileStatus?: 'modified' | 'new' | 'deleted' | 'renamed';
        originalLineIndex: number;
    }

    // Parse diff to identify clickable lines (metadata is now rendered separately)
    const parseDiff = (): ParsedLine[] => {
        const parsedLines: ParsedLine[] = [];

        // Add Diff lines and track positions for comments
        if (diff) {
            const lines = diff.split('\n');
            let currentFile: string | null = null;
            let currentPos = 0;
            let foundFirstHunkInFile = false;
            let pendingFileStatus: 'modified' | 'new' | 'deleted' | 'renamed' = 'modified';

            // New state tracking for empty new files
            let fallbackFilename: string | null = null;
            let fallbackFileIndex: number | null = null;
            let hasEmittedHeader = false;

            lines.forEach((rawLine, index) => {
                const line = rawLine.replace(/\r$/, '');
                let clickable = false;
                let pos: number | null = null;
                let file: string | null = null;
                let lineType: LineType = 'code';

                // Check for diff --git header to determine file status
                if (line.startsWith('diff --git')) {
                    // Check if previous file needs a header
                    if (fallbackFilename && !hasEmittedHeader) {
                        parsedLines.push({
                            text: fallbackFilename,
                            file: fallbackFilename,
                            pos: 0,
                            clickable: true,
                            lineType: 'file-header',
                            fileStatus: pendingFileStatus,
                            originalLineIndex: fallbackFileIndex !== null ? fallbackFileIndex : index
                        });
                    }

                    // Reset for new file
                    hasEmittedHeader = false;
                    pendingFileStatus = 'modified';
                    lineType = 'skip';

                    // Parse filename from diff --git a/path b/path
                    // Try exact match first (handles spaces)
                    const sameNameMatch = line.match(/^diff --git a\/(.+) b\/\1$/);
                    if (sameNameMatch) {
                        fallbackFilename = sameNameMatch[1];
                    } else {
                        // Handle quoted filenames: diff --git "a/foo" "b/foo"
                        const quotedMatch = line.match(/^diff --git "a\/(.+)" "b\/(.+)"$/);
                        if (quotedMatch && quotedMatch[1] === quotedMatch[2]) {
                            fallbackFilename = quotedMatch[1];
                        } else {
                            // Fallback: try to grab the b/ part
                            const parts = line.split(' b/');
                            if (parts.length >= 2) {
                                fallbackFilename = parts.slice(1).join(' b/');
                                // Cleanup potential trailing quote from split if it was quoted "b/..."
                                if (fallbackFilename.endsWith('"') && line.includes('" b/')) {
                                    fallbackFilename = fallbackFilename.slice(0, -1);
                                }
                            } else {
                                fallbackFilename = null;
                            }
                        }
                    }
                    fallbackFileIndex = index;

                } else if (line.startsWith('new file mode')) {
                    pendingFileStatus = 'new';
                    lineType = 'skip';
                } else if (line.startsWith('deleted file mode')) {
                    pendingFileStatus = 'deleted';
                    lineType = 'skip';
                } else if (line.startsWith('rename from') || line.startsWith('rename to') || line.startsWith('similarity index')) {
                    pendingFileStatus = 'renamed';
                    lineType = 'skip';
                } else if (line.startsWith('index ') || line.startsWith('---')) {
                    // Skip these git metadata lines
                    lineType = 'skip';
                } else {
                    // Match +++ b/filename as the file header
                    const fileMatch = line.match(/^\+\+\+\s+b\/(.+)$/) ||
                        line.match(/^\+\+\+\s+(.+)$/);

                    if (fileMatch) {
                        currentFile = (fileMatch[1] || fileMatch[2]).trim();
                        currentPos = 0;
                        foundFirstHunkInFile = false;

                        // Allow comments on the file header itself (general file comments)
                        pos = 0;
                        file = currentFile;
                        clickable = true;
                        lineType = 'file-header';

                        hasEmittedHeader = true;

                        parsedLines.push({
                            text: currentFile,
                            file,
                            pos,
                            clickable,
                            lineType,
                            fileStatus: pendingFileStatus,
                            originalLineIndex: index
                        });
                        pendingFileStatus = 'modified'; // Reset for next file
                        return; // continue equivalent in forEach
                    } else if (currentFile) {
                        const isHunkHeader = line.startsWith('@@');
                        const isAddition = line.startsWith('+') && !line.startsWith('+++');
                        const isDeletion = line.startsWith('-') && !line.startsWith('---');
                        const isContextLine = line.length > 0 && line[0] === ' ';

                        if (isHunkHeader) {
                            // For the first hunk in a file, the backend resets diffPosCount to 0.
                            // Subsequent hunks consume a position but aren't valid comment targets.
                            if (!foundFirstHunkInFile) {
                                foundFirstHunkInFile = true;
                                // Don't increment for first hunk - mimics backend reset to 0
                            } else {
                                // Subsequent hunks consume a position in the diff
                                currentPos++;
                            }
                            pos = currentPos;
                            file = currentFile;
                            // Hunk headers are not valid GitHub comment positions
                            clickable = false;
                            lineType = 'hunk';
                        } else if (isAddition || isDeletion || isContextLine) {
                            currentPos++;
                            pos = currentPos;
                            file = currentFile;
                            clickable = true;
                            lineType = isAddition ? 'addition' : isDeletion ? 'deletion' : 'code';
                        }
                    }
                }

                if (lineType !== 'skip') {
                    parsedLines.push({ text: line, file, pos, clickable, lineType, originalLineIndex: index });
                }
            });

            // Check if last file needs header
            if (fallbackFilename && !hasEmittedHeader) {
                parsedLines.push({
                    text: fallbackFilename,
                    file: fallbackFilename,
                    pos: 0,
                    clickable: true,
                    lineType: 'file-header',
                    fileStatus: pendingFileStatus,
                    originalLineIndex: fallbackFileIndex !== null ? fallbackFileIndex : lines.length
                });
            }
        }

        return parsedLines;
    };

    const handleCommentClick = (idx: number, file: string, pos: number) => {
        if (activeLineIndex === idx) {
            setActiveLineIndex(null);
            setFilename('');
            setPosition('');
            setReplyToId(null);
        } else {
            setFilename(file);
            setPosition(pos.toString());
            setActiveLineIndex(idx);
            setShowCommentModal(false);
            setCommentBody('');
            setReplyToId(null);
        }
    };

    const getClickColumn = (e: React.MouseEvent, container: HTMLElement): number => {
        let range: Range | null = null;
        let offset = 0;

        // Standard
        if (document.caretRangeFromPoint) {
            range = document.caretRangeFromPoint(e.clientX, e.clientY);
        } else if ((document as any).caretPositionFromPoint) {
            // Firefox
            const pos = (document as any).caretPositionFromPoint(e.clientX, e.clientY);
            if (pos) {
                range = document.createRange();
                range.setStart(pos.offsetNode, pos.offset);
                range.collapse(true);
            }
        }

        if (!range) return 0;

        // Traverse text nodes to calculate offset relative to container
        const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
        while (walker.nextNode()) {
            const node = walker.currentNode;
            if (node === range.startContainer) {
                return offset + range.startOffset;
            }
            offset += node.textContent?.length || 0;
        }
        return 0;
    };

    const handleCodeClick = async (idx: number, file: string, pos: number, originalLineIndex: number, col: number) => {
        if (activeLspIndex === idx) {
            setActiveLspIndex(null);
            setLspData(null);
        } else {
            // Don't open comment box on code click
            console.log("Code clicked:", idx, file, pos, "OriginalLine:", originalLineIndex, "Col:", col);

            // Fetch LSP Data
            if (lspClient && lspUri) {
                console.log("Fetching LSP data for URI:", lspUri);
                try {
                    // Header is 4 lines, but it seems we need +5 to align correctly (maybe implicit newline or 1-based issue?)
                    const line = originalLineIndex + 5;
                    // Add 1 to col to account for the diff prefix (+/- / space)
                    const diffCol = col + 1;
                    const [hover, refs] = await Promise.all([
                        lspClient.hover(lspUri, line, diffCol),
                        lspClient.references(lspUri, line, diffCol)
                    ]);
                    console.log("LSP Response - Hover:", hover, "Refs:", refs);

                    let hasHover = false;
                    if (hover && hover.contents) {
                        if (typeof hover.contents === 'string') {
                            hasHover = hover.contents.length > 0;
                        } else if (Array.isArray(hover.contents)) {
                            hasHover = hover.contents.length > 0;
                        } else if (typeof hover.contents === 'object') {
                            hasHover = !!(hover.contents as any).value;
                        }
                    }
                    const hasRefs = refs && refs.length > 0;

                    if (hasHover || hasRefs) {
                        setLspData({ hover, refs });
                        setActiveLspIndex(idx);
                    } else {
                        // Placeholder for testing when LSP returns no data
                        setLspData({
                            hover: { contents: `### LSP Placeholder\nNo data found at line ${line}, char ${diffCol}.` },
                            refs: []
                        });
                        setActiveLspIndex(idx);
                    }
                } catch (e) {
                    console.error("Failed to fetch LSP data", e);
                }
            } else {
                console.warn("LSP Client or URI not ready", !!lspClient, lspUri);
                // Placeholder for testing when LSP is not connected
                setLspData({
                    hover: { contents: "### LSP Placeholder (Disconnected)\nThe LSP server is not yet connected or ready. This is a fallback test message." },
                    refs: [
                        { uri: `file://${file}`, range: { start: { line: originalLineIndex, character: 0 }, end: { line: originalLineIndex, character: 10 } } }
                    ]
                });
                setActiveLspIndex(idx);
            }
        }
    };

    // Handle clicking on a reply thread to reply to the last message
    const handleThreadClick = (thread: Comment[], file: string, pos: number, lineIdx: number) => {
        const lastComment = thread[thread.length - 1];
        setFilename(file);
        setPosition(pos.toString());
        setReplyToId(parseInt(lastComment.id, 10));
        setActiveLineIndex(lineIdx);
        setShowCommentModal(false);
        setCommentBody('');
    };

    // Custom theme based on oneDark but adjusted for diff context
    const customDiffTheme = useMemo(() => {
        const getBaseTheme = () => {
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
        };
        const baseTheme = getBaseTheme();
        return {
            ...baseTheme,
            'pre[class*="language-"]': {
                ...(baseTheme['pre[class*="language-"]'] as any),
                background: 'transparent',
                margin: 0,
                padding: 0,
                overflow: 'visible',
            },
            'code[class*="language-"]': {
                ...(baseTheme['code[class*="language-"]'] as any),
                background: 'transparent',
            },
        };
    }, [theme]);

    // Pre-compute highlighted lines for each file section
    const highlightedLines = useMemo(() => {
        const lines = parseDiff();
        const result: Map<number, React.ReactNode> = new Map();

        // Group lines by file and collect code for bulk highlighting
        let currentFile: string | null = null;
        let fileLines: { idx: number; code: string; prefix: string }[] = [];

        const processFileSection = (file: string, linesData: typeof fileLines) => {
            if (linesData.length === 0) return;

            const language = getLanguageFromFilename(file);

            // We'll use SyntaxHighlighter's output directly for each line
            linesData.forEach((lineData) => {
                const lineCode = lineData.code || ' '; // Use space for empty lines
                result.set(lineData.idx, (
                    <SyntaxHighlighter
                        language={language}
                        style={customDiffTheme}
                        customStyle={{
                            background: 'transparent',
                            margin: 0,
                            padding: 0,
                            display: 'inline',
                            fontSize: 'inherit',
                            fontFamily: 'inherit',
                        }}
                        codeTagProps={{
                            style: {
                                background: 'transparent',
                                fontFamily: 'inherit',
                            }
                        }}
                        PreTag="span"
                        CodeTag="span"
                    >
                        {lineCode}
                    </SyntaxHighlighter>
                ));
            });
        };

        lines.forEach((item, idx) => {
            if (item.lineType === 'file-header') {
                // Process previous file's lines
                if (currentFile && fileLines.length > 0) {
                    processFileSection(currentFile, fileLines);
                }
                currentFile = item.text;
                fileLines = [];
            } else if (currentFile && (item.lineType === 'addition' || item.lineType === 'deletion' || item.lineType === 'code')) {
                // Extract the code without the +/- prefix
                const code = item.text.slice(1);
                fileLines.push({ idx, code, prefix: item.text[0] });
            }
        });

        // Process the last file section
        if (currentFile && fileLines.length > 0) {
            processFileSection(currentFile, fileLines);
        }

        return result;
    }, [diff, customDiffTheme]);

    // Get icon and color for file status
    const getFileStatusInfo = (status?: 'modified' | 'new' | 'deleted' | 'renamed') => {
        switch (status) {
            case 'new':
                return { icon: '+', label: 'new file', color: colors.success, bg: colors.bgSuccessDim };
            case 'deleted':
                return { icon: '−', label: 'deleted', color: colors.danger, bg: colors.bgDangerDim };
            case 'renamed':
                return { icon: '→', label: 'renamed', color: colors.warning, bg: colors.bgWarningDim };
            default:
                return { icon: '●', label: 'modified', color: colors.accent, bg: colors.bgInfoDimStrong };
        }
    };

    const renderDiff = () => {
        const lines = parseDiff();
        if (lines.length === 0) return null;

        let currentFile: string | null = null;

        return lines.map((item, idx) => {
            const line = item.text;
            const isAddition = item.lineType === 'addition';
            const isDeletion = item.lineType === 'deletion';
            const isHunkHeader = item.lineType === 'hunk';
            const isFileHeader = item.lineType === 'file-header';
            const isCodeLine = isAddition || isDeletion || item.lineType === 'code';

            // Update current file when we hit a file header
            if (isFileHeader) {
                currentFile = item.file;
            }

            // Skip rendering lines that belong to collapsed files (but always render file headers)
            if (!isFileHeader && currentFile && collapsedFiles.has(currentFile)) {
                return null;
            }

            // Render file header with clean styling
            if (isFileHeader) {
                const statusInfo = getFileStatusInfo(item.fileStatus);
                const isInlineActive = activeLineIndex === idx;
                const lineComments = item.file ? comments.filter(c => c.path === item.file && (c.position === item.pos?.toString() || (c.position === "" && item.pos === 0))) : [];
                const rootComments = lineComments.filter(c => !c.in_reply_to);
                const isCollapsed = item.file ? collapsedFiles.has(item.file) : false;
                const fileOutdatedComments = item.file ? outdatedComments.filter(c => c.path === item.file) : [];
                const hasOutdated = fileOutdatedComments.length > 0;

                return (
                    <div key={idx}>
                        <div
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '10px',
                                padding: '10px 12px',
                                background: 'linear-gradient(135deg, var(--bg-tertiary) 0%, var(--bg-secondary) 100%)',
                                borderTop: idx > 0 ? '2px solid var(--border)' : 'none',
                                borderBottom: '1px solid var(--border)',
                                marginTop: idx > 0 ? '8px' : '0',
                                cursor: 'pointer',
                                borderLeft: isInlineActive ? '3px solid var(--accent)' : '3px solid transparent',
                                position: 'relative',
                            }}
                            onClick={() => item.file && item.pos !== null && handleCommentClick(idx, item.file, item.pos)}
                            className="hover-line"
                            title={`Add comment to ${item.file}`}
                        >
                            <button
                                onClick={(e) => {
                                    e.stopPropagation();
                                    if (item.file) toggleFileCollapse(item.file);
                                }}
                                style={{
                                    background: 'transparent',
                                    border: 'none',
                                    padding: '4px',
                                    cursor: 'pointer',
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'center',
                                    color: 'var(--text-secondary)',
                                    fontSize: '16px',
                                    lineHeight: 1,
                                    transition: 'transform 0.2s ease',
                                    transform: isCollapsed ? 'rotate(-90deg)' : 'rotate(0deg)',
                                }}
                                title={isCollapsed ? 'Expand file' : 'Collapse file'}
                            >
                                ▼
                            </button>
                            <span style={{
                                display: 'inline-flex',
                                alignItems: 'center',
                                justifyContent: 'center',
                                width: '22px',
                                height: '22px',
                                borderRadius: '4px',
                                background: statusInfo.bg,
                                color: statusInfo.color,
                                fontSize: '14px',
                                fontWeight: 600,
                            }}>
                                {statusInfo.icon}
                            </span>
                            <span style={{
                                fontSize: '11px',
                                fontWeight: 500,
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                                color: statusInfo.color,
                                minWidth: '60px',
                            }}>
                                {statusInfo.label}
                            </span>
                            <span style={{
                                fontFamily: 'var(--font-mono)',
                                fontSize: '13px',
                                fontWeight: 500,
                                color: 'var(--text-primary)',
                            }}>
                                {item.text}
                            </span>
                            {hasOutdated && (
                                <button
                                    onClick={(e) => {
                                        e.stopPropagation();
                                        if (item.file) setActiveOutdatedFile(item.file);
                                    }}
                                    style={{
                                        marginLeft: 'auto',
                                        background: colors.bgWarningDim,
                                        border: `1px solid ${colors.borderWarningDim}`,
                                        color: colors.textWarning,
                                        padding: '4px 8px',
                                        borderRadius: '4px',
                                        fontSize: '12px',
                                        cursor: 'pointer',
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: '6px',
                                        fontWeight: 500
                                    }}
                                >                                    <span>⚠️</span> Outdated Comments ({fileOutdatedComments.length})
                                </button>
                            )}
                        </div>
                        {rootComments.map(rc => {
                            const thread = [rc, ...lineComments.filter(c => c.in_reply_to === parseInt(rc.id, 10))];
                            const isReplyingToThread = replyToId !== null && thread.some(c => parseInt(c.id, 10) === replyToId);
                            return (
                                <div
                                    key={rc.id}
                                    style={{
                                        margin: '10px 20px',
                                        border: isReplyingToThread ? '2px solid var(--accent)' : '1px solid var(--border)',
                                        borderRadius: '6px',
                                        background: 'var(--bg-primary)',
                                        overflow: 'hidden',
                                        cursor: 'pointer',
                                        transition: 'border-color 0.15s ease',
                                    }}
                                    onClick={(e) => {
                                        e.stopPropagation();
                                        if (item.file && item.pos !== null) {
                                            handleThreadClick(thread, item.file, item.pos, idx);
                                        }
                                    }}
                                    className="hover-thread"
                                    title="Click to reply to this thread"
                                >
                                    <div style={{ background: 'var(--bg-secondary)', padding: '5px 10px', fontSize: '11px', borderBottom: '1px solid var(--border)', color: 'var(--text-secondary)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                        <span>{rc.author} commented</span>
                                        <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                            <span style={{
                                                fontSize: '10px',
                                                color: colors.accent,
                                                opacity: 0.7,
                                                background: colors.bgInfoDim,
                                                padding: '2px 6px',
                                                borderRadius: '4px'
                                            }}>
                                                ↩ click to reply
                                            </span>
                                            <span>ID: {rc.id}</span>
                                        </div>
                                    </div>
                                    {thread.map(c => (
                                        <div key={c.id} style={{ padding: '10px', borderBottom: c.id !== thread[thread.length - 1].id ? '1px solid var(--border)' : 'none' }}>
                                            {c !== rc && <div style={{ fontSize: '11px', color: 'var(--accent)', marginBottom: '5px' }}>Reply by {c.author}:</div>}
                                            <div style={{ whiteSpace: 'pre-wrap' }}>{c.body}</div>
                                        </div>
                                    ))}
                                </div>
                            );
                        })}
                        {isInlineActive && (
                            <div style={{
                                padding: '10px 20px',
                                background: 'var(--bg-primary)',
                                borderBottom: '1px solid var(--border)',
                                borderTop: '1px solid var(--border)',
                                marginBottom: '10px'
                            }}>
                                <div style={{ marginBottom: '5px', fontSize: '12px', color: 'var(--text-secondary)' }}>
                                    {replyToId !== null
                                        ? `Replying to comment #${replyToId}`
                                        : `Commenting on ${item.file}:${item.pos}`
                                    }
                                </div>
                                {lspData && (
                                    <div style={{ marginBottom: '10px', fontSize: '13px', border: '1px solid var(--border)', borderRadius: '4px', overflow: 'hidden' }}>
                                        <div style={{ background: 'var(--bg-secondary)', padding: '5px 10px', fontWeight: 600, borderBottom: '1px solid var(--border)' }}>LSP Info</div>
                                        <div style={{ padding: '10px', background: 'var(--bg-primary)' }}>
                                            {lspData.hover && (
                                                <div style={{ marginBottom: '10px' }}>
                                                    <strong>Hover:</strong>
                                                    <pre style={{ whiteSpace: 'pre-wrap', marginTop: '5px' }}>
                                                        {typeof lspData.hover.contents === 'string'
                                                            ? lspData.hover.contents
                                                            : Array.isArray(lspData.hover.contents)
                                                                ? lspData.hover.contents.map(c => typeof c === 'string' ? c : c.value).join('\n')
                                                                : (lspData.hover.contents as any).value
                                                        }
                                                    </pre>
                                                </div>
                                            )}
                                            {lspData.refs && lspData.refs.length > 0 && (
                                                <div>
                                                    <strong>References ({lspData.refs.length}):</strong>
                                                    <ul style={{ margin: '5px 0 0 20px', padding: 0 }}>
                                                        {lspData.refs.map((r, i) => (
                                                            <li key={i}>{r.uri} : {r.range.start.line + 1}</li>
                                                        ))}
                                                    </ul>
                                                </div>
                                            )}
                                            {!lspData.hover && (!lspData.refs || lspData.refs.length === 0) && (
                                                <div style={{ fontStyle: 'italic', color: 'var(--text-secondary)' }}>No information found.</div>
                                            )}
                                        </div>
                                    </div>
                                )}
                                <textarea
                                    autoFocus
                                    placeholder={replyToId !== null ? "Write a reply..." : "Write a comment..."}
                                    value={commentBody}
                                    onChange={e => setCommentBody(e.target.value)}
                                    style={{
                                        width: '100%',
                                        padding: '8px',
                                        marginBottom: '10px',
                                        background: 'var(--bg-primary)',
                                        border: '1px solid var(--border)',
                                        color: 'var(--text-primary)',
                                        borderRadius: '4px',
                                        height: '80px',
                                        fontFamily: 'inherit',
                                        resize: 'vertical',
                                    }}
                                />
                                <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                                    <Button onClick={() => { setActiveLineIndex(null); setReplyToId(null); }} variant="secondary" size="sm">Cancel</Button>
                                    <Button onClick={handleAddComment} size="sm">{replyToId !== null ? 'Reply' : 'Add Comment'}</Button>
                                </div>
                            </div>
                        )}
                    </div>
                );
            }

            let containerStyle: React.CSSProperties = {
                display: 'flex',
                alignItems: 'stretch',
                minHeight: '20px',
                position: 'relative',
            };

            let prefixStyle: React.CSSProperties = {
                width: '20px',
                minWidth: '20px',
                textAlign: 'center',
                userSelect: 'none',
                color: 'var(--text-tertiary)',
                borderRight: '1px solid var(--border)',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                fontSize: '12px',
            };

            let lineStyle: React.CSSProperties = {
                flex: 1,
                padding: '0 8px',
                whiteSpace: 'pre',
                display: 'flex',
                alignItems: 'center',
            };

            if (isAddition) {
                containerStyle = { ...containerStyle, background: colors.diffAddBg };
                prefixStyle = { ...prefixStyle, color: colors.success, background: colors.diffAddGutterBg };
            } else if (isDeletion) {
                containerStyle = { ...containerStyle, background: colors.diffDelBg };
                prefixStyle = { ...prefixStyle, color: colors.danger, background: colors.diffDelGutterBg };
            } else if (isHunkHeader) {
                containerStyle = { ...containerStyle, background: colors.diffHunkBg };
                lineStyle = { ...lineStyle, color: colors.accent, fontStyle: 'italic', fontSize: '12px' };
            }

            if (item.clickable) {
                containerStyle = { ...containerStyle, cursor: 'default' };
                prefixStyle = { ...prefixStyle, cursor: 'pointer' };
            }

            const isInlineActive = activeLineIndex === idx;
            const isLspActive = activeLspIndex === idx;
            const lineComments = item.file ? comments.filter(c => c.path === item.file && (c.position === item.pos?.toString() || (c.position === "" && item.pos === 0))) : [];
            const rootComments = lineComments.filter(c => !c.in_reply_to);

            // Determine what to render for the line content
            let lineContent: React.ReactNode;
            const highlightedContent = highlightedLines.get(idx);

            if (highlightedContent && isCodeLine && !isHunkHeader) {
                // Use syntax-highlighted content
                lineContent = highlightedContent;
            } else {
                // Use plain text for headers and non-code lines
                lineContent = isCodeLine ? line.slice(1) : line;
            }

            const getHoverContent = (hover: any) => {
                if (!hover || !hover.contents) return '';
                if (typeof hover.contents === 'string') return hover.contents;
                if (Array.isArray(hover.contents)) {
                    return hover.contents.map((c: any) => typeof c === 'string' ? c : c.value).join('\n');
                }
                return (hover.contents as any).value || '';
            };

            return (
                <div key={idx}>
                    <div
                        style={{
                            ...containerStyle,
                            borderLeft: isInlineActive || isLspActive ? '3px solid var(--accent)' : '3px solid transparent',
                            marginLeft: isInlineActive || isLspActive ? '-3px' : '0',
                        }}
                        className={item.clickable ? 'hover-line' : ''}
                        title={undefined}
                    >
                        {isCodeLine && !isHunkHeader && (
                            <span
                                style={prefixStyle}
                                onClick={(e) => {
                                    if (item.clickable && item.file && item.pos !== null) {
                                        e.stopPropagation();
                                        handleCommentClick(idx, item.file, item.pos);
                                    }
                                }}
                                title={item.clickable ? `Add comment to ${item.file}:${item.pos}` : undefined}
                            >
                                {isAddition ? '+' : isDeletion ? '-' : ''}
                            </span>
                        )}
                        <span
                            style={{
                                ...lineStyle,
                                cursor: item.clickable ? 'pointer' : 'default'
                            }}
                            onClick={(e) => {
                                if (item.clickable && item.file && item.pos !== null) {
                                    const col = getClickColumn(e, e.currentTarget);
                                    handleCodeClick(idx, item.file, item.pos, item.originalLineIndex, col);
                                }
                            }}
                        >
                            {lineContent}
                        </span>
                        {isLspActive && lspData && (
                            <div style={{
                                position: 'absolute',
                                top: '100%',
                                left: '40px',
                                zIndex: 100,
                                background: 'var(--bg-secondary)',
                                border: '1px solid var(--border)',
                                borderRadius: '6px',
                                boxShadow: shadows.md,
                                padding: '12px',
                                maxWidth: '600px',
                                overflow: 'auto',
                                maxHeight: '300px'
                            }} onClick={e => e.stopPropagation()}>
                                {lspData.hover && (
                                    <div style={{ fontSize: '13px', lineHeight: '1.5', color: 'var(--text-primary)' }}>
                                        <Markdown>
                                            {getHoverContent(lspData.hover)}
                                        </Markdown>
                                    </div>
                                )}
                                {lspData.refs && lspData.refs.length > 0 && (
                                    <div style={{ marginTop: '10px', paddingTop: '10px', borderTop: '1px solid var(--border)', fontSize: '12px' }}>
                                        <div style={{ fontWeight: 600, marginBottom: '5px', color: 'var(--text-secondary)' }}>References ({lspData.refs.length}):</div>
                                        <ul style={{ margin: '0 0 0 15px', padding: 0, color: 'var(--accent)' }}>
                                            {lspData.refs.map((r, i) => (
                                                <li key={i}>{r.uri.split('/').pop()} : {r.range.start.line + 1}</li>
                                            ))}
                                        </ul>
                                    </div>
                                )}
                            </div>
                        )}
                    </div>
                    {rootComments.map(rc => {
                        const thread = [rc, ...lineComments.filter(c => c.in_reply_to === parseInt(rc.id, 10))];
                        const isReplyingToThread = replyToId !== null && thread.some(c => parseInt(c.id, 10) === replyToId);
                        return (
                            <div
                                key={rc.id}
                                style={{
                                    margin: '10px 20px',
                                    border: isReplyingToThread ? '2px solid var(--accent)' : '1px solid var(--border)',
                                    borderRadius: '6px',
                                    background: 'var(--bg-primary)',
                                    overflow: 'hidden',
                                    cursor: 'pointer',
                                    transition: 'border-color 0.15s ease',
                                }}
                                onClick={(e) => {
                                    e.stopPropagation();
                                    if (item.file && item.pos !== null) {
                                        handleThreadClick(thread, item.file, item.pos, idx);
                                    }
                                }}
                                className="hover-thread"
                                title="Click to reply to this thread"
                            >
                                <div style={{ background: 'var(--bg-secondary)', padding: '5px 10px', fontSize: '11px', borderBottom: '1px solid var(--border)', color: 'var(--text-secondary)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                                    <span>{rc.author} commented</span>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                        <span style={{
                                            fontSize: '10px',
                                            color: colors.accent,
                                            opacity: 0.7,
                                            background: colors.bgInfoDim,
                                            padding: '2px 6px',
                                            borderRadius: '4px'
                                        }}>
                                            ↩ click to reply
                                        </span>
                                        <span>ID: {rc.id}</span>
                                    </div>
                                </div>
                                {thread.map(c => (
                                    <div key={c.id} style={{ padding: '10px', borderBottom: c.id !== thread[thread.length - 1].id ? '1px solid var(--border)' : 'none' }}>
                                        {c !== rc && <div style={{ fontSize: '11px', color: 'var(--accent)', marginBottom: '5px' }}>Reply by {c.author}:</div>}
                                        <div style={{ whiteSpace: 'pre-wrap' }}>{c.body}</div>
                                    </div>
                                ))}
                            </div>
                        );
                    })}
                    {isInlineActive && (
                        <div style={{
                            padding: '10px 20px',
                            background: 'var(--bg-primary)',
                            borderBottom: '1px solid var(--border)',
                            borderTop: '1px solid var(--border)',
                            marginBottom: '10px'
                        }}>
                            <div style={{ marginBottom: '5px', fontSize: '12px', color: 'var(--text-secondary)' }}>
                                {replyToId !== null
                                    ? `Replying to comment #${replyToId}`
                                    : `Commenting on ${item.file}:${item.pos}`
                                }
                            </div>
                            <textarea
                                autoFocus
                                placeholder={replyToId !== null ? "Write a reply..." : "Write a comment..."}
                                value={commentBody}
                                onChange={e => setCommentBody(e.target.value)}
                                style={{
                                    width: '100%',
                                    padding: '8px',
                                    marginBottom: '10px',
                                    background: 'var(--bg-primary)',
                                    border: '1px solid var(--border)',
                                    color: 'var(--text-primary)',
                                    borderRadius: '4px',
                                    height: '80px',
                                    fontFamily: 'inherit',
                                    resize: 'vertical',
                                }}
                            />
                            <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                                <Button onClick={() => { setActiveLineIndex(null); setReplyToId(null); }} variant="secondary" size="sm">Cancel</Button>
                                <Button onClick={handleAddComment} size="sm">{replyToId !== null ? 'Reply' : 'Add Comment'}</Button>
                            </div>
                        </div>
                    )}
                </div>
            );
        });
    };

    if (loading && !content) return <div style={{ padding: '20px' }}>Loading PR...</div>;

    const getCIStatusStyle = () => {
        if (!metadata?.ci_status) return {};
        const status = metadata.ci_status.toLowerCase();
        if (status === 'success' || status === 'passed') {
            return { background: colors.bgSuccessDim, color: colors.textSuccess, borderColor: colors.borderSuccessDim };
        } else if (status === 'pending' || status === 'running') {
            return { background: colors.bgWarningDim, color: colors.textWarning, borderColor: colors.borderWarningDim };
        } else if (status === 'failure' || status === 'failed') {
            return { background: colors.bgDangerDim, color: colors.textDanger, borderColor: colors.borderDangerDim };
        }
        return { background: colors.bgTertiary, color: colors.textSecondary, borderColor: colors.border };
    };

    const getStateStyle = () => {
        if (!metadata?.state) return {};
        const state = metadata.state.toLowerCase();
        if (state === 'open') {
            return { background: colors.bgSuccessDim, color: colors.textSuccess };
        } else if (state === 'closed') {
            return { background: colors.bgDangerDim, color: colors.textDanger };
        } else if (state === 'merged') {
            return { background: colors.bgMergedDim, color: colors.textMerged };
        }
        return {};
    };

    return (
        <div className="review-container">
            {/* PR Header Section */}
            {metadata && (
                <div className="pr-header" style={{
                    background: 'linear-gradient(135deg, var(--bg-secondary) 0%, var(--bg-primary) 100%)',
                    borderRadius: '12px',
                    border: '1px solid var(--border)',
                    marginBottom: '16px',
                    overflow: 'hidden'
                }}>
                    {/* Title Bar */}
                    <div style={{
                        padding: '20px 24px',
                        borderBottom: '1px solid var(--border)',
                        background: 'var(--bg-secondary)'
                    }}>
                        <div style={{ display: 'flex', alignItems: 'flex-start', gap: '16px' }}>
                            <div style={{ flex: 1 }}>
                                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '8px', flexWrap: 'wrap' }}>
                                    <span style={{
                                        ...getStateStyle(),
                                        padding: '4px 12px',
                                        borderRadius: '16px',
                                        fontSize: '12px',
                                        fontWeight: 600,
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.5px'
                                    }}>
                                        {metadata.draft ? '📝 Draft' : metadata.state}
                                    </span>
                                    <span style={{ color: 'var(--text-secondary)', fontSize: '14px' }}>
                                        #{metadata.number}
                                    </span>
                                </div>
                                <h1 style={{
                                    margin: 0,
                                    fontSize: '22px',
                                    fontWeight: 600,
                                    color: 'var(--text-primary)',
                                    lineHeight: 1.3
                                }}>
                                    {metadata.title}
                                </h1>
                            </div>
                            <a
                                href={metadata.url}
                                target="_blank"
                                rel="noopener noreferrer"
                                style={{
                                    background: 'transparent',
                                    color: 'var(--text-secondary)',
                                    border: '1px solid var(--border)',
                                    padding: '8px 16px',
                                    borderRadius: '6px',
                                    cursor: 'pointer',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '6px',
                                    textDecoration: 'none',
                                    fontSize: '13px'
                                }}
                            >
                                <span>↗</span> GitHub
                            </a>
                        </div>
                    </div>

                    {/* Info Grid */}
                    <div style={{
                        display: 'grid',
                        gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
                        gap: '1px',
                        background: 'var(--border)'
                    }}>
                        {/* Branch Info */}
                        <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                            <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                Branch
                            </div>
                            <div style={{ fontFamily: 'var(--font-mono)', fontSize: '13px', color: 'var(--accent)' }}>
                                <span style={{ color: 'var(--text-secondary)' }}>{metadata.base_ref}</span>
                                <span style={{ margin: '0 8px', color: 'var(--text-tertiary)' }}>←</span>
                                <span>{metadata.head_ref}</span>
                            </div>
                        </div>

                        {/* Author */}
                        <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                            <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                Author
                            </div>
                            <div style={{ fontSize: '14px', fontWeight: 500, color: 'var(--text-primary)' }}>
                                @{metadata.author}
                            </div>
                        </div>

                        {/* CI Status */}
                        {metadata.ci_status && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                    CI Status
                                </div>
                                <div style={{
                                    display: 'inline-flex',
                                    alignItems: 'center',
                                    gap: '6px',
                                    padding: '4px 10px',
                                    borderRadius: '6px',
                                    fontSize: '13px',
                                    fontWeight: 500,
                                    border: '1px solid',
                                    ...getCIStatusStyle()
                                }}>
                                    {metadata.ci_status.toLowerCase() === 'success' && '✓'}
                                    {metadata.ci_status.toLowerCase() === 'pending' && '○'}
                                    {metadata.ci_status.toLowerCase() === 'failure' && '✗'}
                                    {metadata.ci_status}
                                </div>
                            </div>
                        )}

                        {/* Reviewers/Teams */}
                        {((metadata.reviewers && metadata.reviewers.length > 0) || (metadata.requested_teams && metadata.requested_teams.length > 0)) && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                    Requested Reviewers
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.reviewers?.map((r, i) => (
                                        <span key={`rev-${i}`} style={{
                                            background: 'var(--bg-tertiary)',
                                            padding: '2px 8px',
                                            borderRadius: '4px',
                                            fontSize: '12px',
                                            color: 'var(--text-primary)'
                                        }}>
                                            @{r}
                                        </span>
                                    ))}
                                    {metadata.requested_teams?.map((t, i) => (
                                        <span key={`team-${i}`} style={{
                                            background: colors.bgInfoDim,
                                            padding: '2px 8px',
                                            borderRadius: '4px',
                                            fontSize: '12px',
                                            color: colors.accent,
                                            border: `1px solid ${colors.borderInfoDim}`
                                        }}>
                                            team:{t}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}

                        {/* Approved By */}
                        {metadata.approved_by && metadata.approved_by.length > 0 && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div style={{ fontSize: '11px', color: 'var(--success)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                    ✓ Approved By
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.approved_by.map((r, i) => (
                                        <span key={i} style={{
                                            background: colors.bgSuccessDim,
                                            padding: '2px 8px',
                                            borderRadius: '4px',
                                            fontSize: '12px',
                                            color: colors.success,
                                            border: `1px solid ${colors.borderSuccessDim}`
                                        }}>
                                            @{r}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}

                        {/* Changes Requested By */}
                        {metadata.changes_requested_by && metadata.changes_requested_by.length > 0 && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div style={{ fontSize: '11px', color: 'var(--danger)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                    ✗ Changes Requested By
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.changes_requested_by.map((r, i) => (
                                        <span key={i} style={{
                                            background: colors.bgDangerDim,
                                            padding: '2px 8px',
                                            borderRadius: '4px',
                                            fontSize: '12px',
                                            color: colors.danger,
                                            border: `1px solid ${colors.borderDangerDim}`
                                        }}>
                                            @{r}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}

                        {/* Commented By */}
                        {metadata.commented_by && metadata.commented_by.length > 0 && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '6px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                    💬 Commented By
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.commented_by.map((r, i) => (
                                        <span key={i} style={{
                                            background: 'var(--bg-tertiary)',
                                            padding: '2px 8px',
                                            borderRadius: '4px',
                                            fontSize: '12px',
                                            color: 'var(--text-primary)'
                                        }}>
                                            @{r}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}
                    </div>

                    {/* Labels, Assignees, Milestone Row */}
                    {(metadata.labels?.length > 0 || metadata.assignees?.length > 0 || metadata.milestone) && (
                        <div style={{
                            padding: '14px 20px',
                            display: 'flex',
                            flexWrap: 'wrap',
                            gap: '20px',
                            borderTop: '1px solid var(--border)',
                            background: 'var(--bg-primary)'
                        }}>
                            {metadata.labels && metadata.labels.length > 0 && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span style={{ fontSize: '11px', color: 'var(--text-secondary)', textTransform: 'uppercase' }}>Labels:</span>
                                    <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
                                        {metadata.labels.map((label, i) => (
                                            <span key={i} style={{
                                                background: colors.bgMergedDim,
                                                color: colors.textMerged,
                                                padding: '2px 10px',
                                                borderRadius: '12px',
                                                fontSize: '11px',
                                                fontWeight: 500,
                                                border: `1px solid ${colors.borderMergedDim}`
                                            }}>
                                                {label}
                                            </span>
                                        ))}
                                    </div>
                                </div>
                            )}
                            {metadata.assignees && metadata.assignees.length > 0 && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span style={{ fontSize: '11px', color: 'var(--text-secondary)', textTransform: 'uppercase' }}>Assignees:</span>
                                    <span style={{ fontSize: '13px', color: 'var(--text-primary)' }}>
                                        {metadata.assignees.map(a => `@${a}`).join(', ')}
                                    </span>
                                </div>
                            )}
                            {metadata.milestone && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span style={{ fontSize: '11px', color: 'var(--text-secondary)', textTransform: 'uppercase' }}>Milestone:</span>
                                    <span style={{ fontSize: '13px', color: 'var(--accent)' }}>{metadata.milestone}</span>
                                </div>
                            )}
                        </div>
                    )}

                    {/* Description */}
                    {metadata.body && (
                        <div style={{
                            padding: '16px 20px',
                            borderTop: '1px solid var(--border)',
                            background: 'var(--bg-primary)'
                        }}>
                            <div style={{ fontSize: '11px', color: 'var(--text-secondary)', marginBottom: '10px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                Description
                            </div>
                            <div
                                className="pr-description"
                                style={{
                                    fontSize: '14px',
                                    lineHeight: 1.6,
                                    color: 'var(--text-primary)',
                                    maxHeight: '300px',
                                    overflow: 'auto'
                                }}
                            >
                                <Markdown>{stripHtmlComments(metadata.body)}</Markdown>
                            </div>
                        </div>
                    )}

                    {/* CI Failures */}
                    {metadata.ci_failures && metadata.ci_failures.length > 0 && (
                        <div style={{
                            padding: '14px 20px',
                            borderTop: `1px solid ${colors.border}`,
                            background: colors.bgDangerDim
                        }}>
                            <div style={{ fontSize: '11px', color: colors.textDanger, marginBottom: '8px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                                ✗ CI Failures
                            </div>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                                {metadata.ci_failures.map((failure, i) => (
                                    <span key={i} style={{
                                        fontFamily: 'var(--font-mono)',
                                        fontSize: '12px',
                                        color: colors.textDanger
                                    }}>
                                        • {failure}
                                    </span>
                                ))}
                            </div>
                        </div>
                    )}
                </div>
            )}

            {/* Toolbar */}
            <div className="toolbar" style={{
                display: 'flex',
                gap: '10px',
                marginBottom: '16px',
                padding: '12px 16px',
                background: 'var(--bg-secondary)',
                borderRadius: '8px',
                border: '1px solid var(--border)',
                position: 'sticky',
                top: '10px',
                zIndex: 10
            }}>
                <Button onClick={handleSync} loading={loading}>
                    {loading ? 'Syncing...' : '↻ Sync'}
                </Button>
                <Button onClick={() => {
                    if (collapsedFiles.size > 0) {
                        expandAllFiles();
                    } else {
                        collapseAllFiles();
                    }
                }} variant="secondary" size="sm">
                    {collapsedFiles.size > 0 ? '▼ Expand All' : '◀ Collapse All'}
                </Button>
                <Button onClick={() => {
                    setFilename('');
                    setPosition('');
                    setShowCommentModal(true);
                }}>+ Comment</Button>
                <Button onClick={() => setSubmitting(true)} style={{ background: 'var(--success)' }}>Submit Review</Button>
                <Button
                    onClick={() => setShowPlugins(!showPlugins)}
                    variant={showPlugins ? 'primary' : 'secondary'}
                >
                    Plugins {Object.keys(pluginOutputs).length > 0 ? `(${Object.keys(pluginOutputs).length})` : ''}
                </Button>

                <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', color: 'var(--text-secondary)', fontSize: '13px' }}>
                    💡 Click on any line of code to add a comment
                </span>
            </div>

            {/* Diff Section */}
            <div className="diff-section" style={{
                background: 'var(--bg-secondary)',
                borderRadius: '8px',
                border: '1px solid var(--border)',
                overflow: 'hidden'
            }}>
                <div style={{
                    padding: '12px 16px',
                    borderBottom: '1px solid var(--border)',
                    background: 'var(--bg-primary)',
                    fontSize: '13px',
                    fontWeight: 500,
                    color: 'var(--text-secondary)',
                    display: 'flex',
                    alignItems: 'center',
                    gap: '8px'
                }}>
                    <span style={{ color: 'var(--accent)' }}>◈</span>
                    Changes
                    {repoExists === false && (
                        <div style={{ marginLeft: '12px', display: 'inline-flex', alignItems: 'center', gap: '6px', color: 'var(--warning)', fontSize: '12px', fontWeight: 400 }}>
                            <span style={{ fontSize: '14px' }}>⚠️</span>
                            Repo not found locally. LSP disabled.
                        </div>
                    )}
                </div>
                <div style={{
                    padding: '16px',
                    fontFamily: 'var(--font-mono)',
                    fontSize: '13px',
                    overflowX: 'auto'
                }}>
                    {renderDiff()}
                </div>
            </div>

            <Modal
                isOpen={showCommentModal}
                onClose={resetCommentForm}
                title="Add Comment"
                size="sm"
            >
                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                    <input
                        placeholder="Filename"
                        value={filename}
                        onChange={e => setFilename(e.target.value)}
                        style={{
                            padding: '8px',
                            background: 'var(--bg-primary)',
                            border: '1px solid var(--border)',
                            color: 'var(--text-primary)',
                            borderRadius: '4px',
                        }}
                    />
                    <input
                        type="number"
                        placeholder="Line Position in Diff"
                        value={position}
                        onChange={e => setPosition(e.target.value)}
                        style={{
                            padding: '8px',
                            background: 'var(--bg-primary)',
                            border: '1px solid var(--border)',
                            color: 'var(--text-primary)',
                            borderRadius: '4px',
                        }}
                    />
                    <TextArea
                        placeholder="Comment"
                        value={commentBody}
                        onChange={e => setCommentBody(e.target.value)}
                        rows={5}
                    />
                    <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                        <Button onClick={resetCommentForm} variant="secondary">Cancel</Button>
                        <Button onClick={handleAddComment}>Add</Button>
                    </div>
                </div>
            </Modal>

            <Modal
                isOpen={submitting}
                onClose={() => setSubmitting(false)}
                title="Submit Review"
                size="sm"
            >
                <div style={{ display: 'flex', flexDirection: 'column', gap: '12px' }}>
                    <select
                        value={reviewEvent}
                        onChange={e => setReviewEvent(e.target.value)}
                        style={{
                            padding: '8px',
                            background: 'var(--bg-primary)',
                            border: '1px solid var(--border)',
                            color: 'var(--text-primary)',
                            borderRadius: '4px',
                        }}
                    >
                        <option value="COMMENT">Comment</option>
                        <option value="APPROVE">Approve</option>
                        <option value="REQUEST_CHANGES">Request Changes</option>
                    </select>
                    <TextArea
                        placeholder="Review Body (Optional)"
                        value={reviewBody}
                        onChange={e => setReviewBody(e.target.value)}
                        rows={5}
                    />
                    <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                        <Button onClick={() => setSubmitting(false)} variant="secondary">Cancel</Button>
                        <Button onClick={handleSubmitReview} style={{ background: 'var(--success)' }}>Submit</Button>
                    </div>
                </div>
            </Modal>
            {showPlugins && (
                <div style={{
                    position: 'fixed' as const,
                    top: 0,
                    left: 0,
                    right: 0,
                    bottom: 0,
                    background: colors.overlayBg,
                    display: 'flex',
                    justifyContent: 'flex-end',
                    zIndex: 100,
                }} onClick={() => setShowPlugins(false)}>
                    <div style={{
                        background: 'var(--bg-secondary)',
                        width: '600px',
                        maxWidth: '90vw',
                        height: '100vh',
                        borderLeft: '1px solid var(--border)',
                        boxShadow: shadows.lg,
                        display: 'flex',
                        flexDirection: 'column',
                        overflow: 'hidden',
                    }} onClick={e => e.stopPropagation()}>
                        <div style={{
                            display: 'flex',
                            justifyContent: 'space-between',
                            alignItems: 'center',
                            padding: '20px 24px',
                            background: 'var(--bg-secondary)',
                            zIndex: 1,
                            borderBottom: '1px solid var(--border)'
                        }}>
                            <h2 style={{ fontSize: '18px', margin: 0, color: 'var(--accent)' }}>Plugin Analysis</h2>
                            <Button onClick={() => setShowPlugins(false)} variant="secondary" size="sm">Close (Esc)</Button>
                        </div>
                        <div style={{ flex: 1, overflowY: 'auto', padding: '24px' }}>
                            {Object.keys(pluginOutputs).length === 0 ? (
                                <div style={{ color: 'var(--text-secondary)', fontStyle: 'italic', padding: '20px', textAlign: 'center' }}>No plugin output found.</div>
                            ) : (
                                <div style={{ display: 'flex', flexDirection: 'column', gap: '20px' }}>
                                    {Object.entries(pluginOutputs).map(([name, data]) => (
                                        <div key={name} style={{ background: 'var(--bg-primary)', border: '1px solid var(--border)', borderRadius: '8px', overflow: 'hidden' }}>
                                            <div style={{
                                                padding: '10px 15px',
                                                background: 'var(--bg-secondary)',
                                                borderBottom: '1px solid var(--border)',
                                                display: 'flex',
                                                justifyContent: 'space-between',
                                                alignItems: 'center'
                                            }}>
                                                <span style={{ fontWeight: 600, fontSize: '14px' }}>{name}</span>
                                                <span style={{
                                                    fontSize: '11px',
                                                    padding: '2px 10px',
                                                    borderRadius: '12px',
                                                    fontWeight: 600,
                                                    background: data.status === 'success' ? colors.bgSuccessDim : data.status === 'pending' ? colors.bgWarningDim : colors.bgDangerDim,
                                                    color: data.status === 'success' ? colors.textSuccess : data.status === 'pending' ? colors.textWarning : colors.textDanger,
                                                    border: `1px solid ${data.status === 'success' ? colors.borderSuccessDim : data.status === 'pending' ? colors.borderWarningDim : colors.borderDangerDim}`
                                                }}>
                                                    {data.status.toUpperCase()}
                                                </span>
                                            </div>
                                            <div className="plugin-output markdown-content" style={{
                                                padding: '15px',
                                                fontSize: '14px',
                                                lineHeight: '1.6',
                                                color: 'var(--text-primary)',
                                                background: 'var(--bg-primary)',
                                            }}>
                                                {data.result ? (
                                                    <Markdown>{data.result}</Markdown>
                                                ) : (
                                                    <span style={{ color: 'var(--text-secondary)', fontStyle: 'italic' }}>No output produced.</span>
                                                )}
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            )}
                        </div>
                    </div>
                </div>
            )}
            {activeOutdatedFile && (
                <div style={{
                    position: 'fixed' as const,
                    top: 0,
                    left: 0,
                    right: 0,
                    bottom: 0,
                    background: colors.overlayBg,
                    display: 'flex',
                    justifyContent: 'flex-end',
                    zIndex: 100,
                }} onClick={() => setActiveOutdatedFile(null)}>
                    <div style={{
                        background: 'var(--bg-secondary)',
                        width: '600px',
                        maxWidth: '90vw',
                        height: '100vh',
                        borderLeft: '1px solid var(--border)',
                        boxShadow: shadows.lg,
                        display: 'flex',
                        flexDirection: 'column',
                        overflow: 'hidden',
                    }} onClick={e => e.stopPropagation()}>
                        <div style={{
                            display: 'flex',
                            justifyContent: 'space-between',
                            alignItems: 'center',
                            padding: '20px 24px',
                            background: 'var(--bg-secondary)',
                            zIndex: 1,
                            borderBottom: '1px solid var(--border)'
                        }}>
                            <div style={{ display: 'flex', flexDirection: 'column' }}>
                                <h2 style={{ fontSize: '18px', margin: 0, color: 'var(--warning)' }}>Outdated Comments</h2>
                                <span style={{ fontSize: '12px', color: 'var(--text-secondary)', marginTop: '4px' }}>{activeOutdatedFile}</span>
                            </div>
                            <Button onClick={() => setActiveOutdatedFile(null)} variant="secondary" size="sm">Close (Esc)</Button>
                        </div>
                        <div style={{ flex: 1, overflowY: 'auto', padding: '24px' }}>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '20px' }}>
                                {outdatedComments.filter(c => c.path === activeOutdatedFile).map(c => (
                                    <div key={c.id} style={{
                                        background: 'var(--bg-primary)',
                                        border: '1px solid var(--border)',
                                        borderRadius: '8px',
                                        overflow: 'hidden'
                                    }}>
                                        <div style={{
                                            padding: '10px 15px',
                                            background: 'var(--bg-secondary)',
                                            borderBottom: '1px solid var(--border)',
                                            display: 'flex',
                                            justifyContent: 'space-between',
                                            alignItems: 'center'
                                        }}>
                                            <span style={{ fontWeight: 600, fontSize: '14px', color: 'var(--text-primary)' }}>{c.author}</span>
                                            <span style={{ fontSize: '11px', color: 'var(--text-secondary)' }}>
                                                {new Date(c.created_at).toLocaleString()}
                                            </span>
                                        </div>
                                        <div className="markdown-content" style={{
                                            padding: '15px',
                                            fontSize: '14px',
                                            lineHeight: '1.6',
                                            color: 'var(--text-primary)',
                                        }}>
                                            <Markdown>{c.body}</Markdown>
                                        </div>
                                    </div>
                                ))}
                            </div>
                        </div>
                    </div>
                </div>
            )}
        </div>
    );
}
