import { useState, useEffect, useMemo, useCallback } from 'react';
import Markdown from 'react-markdown';
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
import { rpcCall, getHunkContext } from '../api';
import { Button, Modal, TextArea, colors, shadows, Theme } from '../design';
import { useLsp } from '../hooks/useLsp';
import { getClickColumn } from '../utils/dom';
import {
    dockViewer as dockViewerState,
    undockTab as undockTabState,
    closeDockedTab as closeDockedTabState,
    dockDiffFile as dockDiffFileState,
    moveTabToPanel,
    toggleSplit,
    allDockedTabs,
} from '../dock_utils';
import type { ActiveTab, DockState, PanelId, PanelState } from '../dock_utils';
import LspPopover from './LspPopover';
import CodeViewerModal from './CodeViewerModal';

// Strip HTML comments from text (e.g., <!-- comment -->)
function CopyUrlButton({ url }: { url: string }) {
    const [copied, setCopied] = useState(false);

    const handleCopy = () => {
        navigator.clipboard.writeText(url).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        });
    };

    return (
        <button
            onClick={handleCopy}
            style={{
                background: 'transparent',
                color: copied ? 'var(--accent)' : 'var(--text-secondary)',
                border: '1px solid var(--border)',
                padding: '8px 16px',
                borderRadius: '6px',
                cursor: 'pointer',
                display: 'flex',
                alignItems: 'center',
                gap: '6px',
                fontSize: '13px',
            }}
        >
            <span>{copied ? '✓' : '⎘'}</span> {copied ? 'Copied!' : 'Copy URL'}
        </button>
    );
}

// Stable slug for in-page anchor IDs from file paths.
const slugify = (s: string): string => s.replace(/[^a-zA-Z0-9_-]+/g, '-').replace(/^-+|-+$/g, '');

const stripHtmlComments = (text: string): string => {
    return text.replace(/<!--[\s\S]*?-->/g, '');
};

interface ReviewProps {
    owner: string;
    repo: string;
    number: number;
    theme: Theme;
    onThemeChange: (theme: Theme) => void;
    onNavigate?: (owner: string, repo: string, number: number) => void;
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
    diff_hunk: string;
}

interface ReviewData {
    id: number;
    user: string;
    body: string;
    state: string;
    submitted_at: string;
    html_url: string;
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
    reviewers: string[]; // Requested individual reviewers
    requested_teams: string[]; // Requested team reviewers
    approved_by: string[]; // Users who approved
    changes_requested_by: string[]; // Users who requested changes
    commented_by: string[]; // Users who commented
    draft: boolean;
    ci_status: string;
    ci_failures: string[];
    body: string;
    url: string;
    repo_path: string;
    worktree_path: string;
}

interface PRResponse {
    content: string;
    diff: string;
    comments: Comment[];
    outdated_comments: Comment[];
    reviews: ReviewData[];
    metadata: PRMetadata;
    feedback: string;
}

// Map file extensions to Prism language identifiers
const getLanguageFromFilename = (filename: string): string => {
    const ext = filename.split('.').pop()?.toLowerCase() || '';
    const languageMap: Record<string, string> = {
        // JavaScript/TypeScript
        js: 'javascript',
        jsx: 'jsx',
        ts: 'typescript',
        tsx: 'tsx',
        mjs: 'javascript',
        cjs: 'javascript',
        // Web
        html: 'html',
        htm: 'html',
        css: 'css',
        scss: 'scss',
        sass: 'sass',
        less: 'less',
        json: 'json',
        xml: 'xml',
        svg: 'xml',
        // Backend
        py: 'python',
        rb: 'ruby',
        go: 'go',
        rs: 'rust',
        java: 'java',
        kt: 'kotlin',
        scala: 'scala',
        php: 'php',
        c: 'c',
        h: 'c',
        cpp: 'cpp',
        cc: 'cpp',
        cxx: 'cpp',
        hpp: 'cpp',
        cs: 'csharp',
        swift: 'swift',
        // Shell/Config
        sh: 'bash',
        bash: 'bash',
        zsh: 'bash',
        fish: 'bash',
        ps1: 'powershell',
        yaml: 'yaml',
        yml: 'yaml',
        toml: 'toml',
        ini: 'ini',
        conf: 'ini',
        // Data/Docs
        md: 'markdown',
        mdx: 'markdown',
        sql: 'sql',
        graphql: 'graphql',
        gql: 'graphql',
        // Other
        dockerfile: 'docker',
        makefile: 'makefile',
        lua: 'lua',
        r: 'r',
        dart: 'dart',
        ex: 'elixir',
        exs: 'elixir',
        erl: 'erlang',
        hrl: 'erlang',
        clj: 'clojure',
        vim: 'vim',
        el: 'lisp',
        lisp: 'lisp',
        hs: 'haskell',
        ml: 'ocaml',
        proto: 'protobuf',
    };

    // Handle special filenames
    const basename = filename.split('/').pop()?.toLowerCase() || '';
    if (basename === 'dockerfile') return 'docker';
    if (basename === 'makefile' || basename === 'gnumakefile') return 'makefile';
    if (basename.endsWith('.d.ts')) return 'typescript';

    return languageMap[ext] || 'text';
};

export default function Review({
    owner,
    repo,
    number,
    theme,
    onThemeChange: _onThemeChange,
    onNavigate,
}: ReviewProps) {
    const [content, setContent] = useState<string>('');
    const [diff, setDiff] = useState<string>('');
    const [comments, setComments] = useState<Comment[]>([]);
    const [outdatedComments, setOutdatedComments] = useState<Comment[]>([]);
    const [reviews, setReviews] = useState<ReviewData[]>([]);
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
    // Description starts collapsed so the diff dominates the viewport.
    const [descCollapsed, setDescCollapsed] = useState(true);
    // Inline review feedback drafting is hidden behind a toggle; the body is also
    // editable in the Submit Review modal.
    const [feedbackCollapsed, setFeedbackCollapsed] = useState(true);

    const [submitting, setSubmitting] = useState(false);
    const [isSubmittingReview, setIsSubmittingReview] = useState(false);
    const [isAddingComment, setIsAddingComment] = useState(false);

    // Comment form data
    const [filename, setFilename] = useState('');
    const [position, setPosition] = useState('');
    const [commentBody, setCommentBody] = useState('');
    const [replyToId, setReplyToId] = useState<number | null>(null);
    const [editingCommentId, setEditingCommentId] = useState<number | null>(null);
    const [editingCommentBody, setEditingCommentBody] = useState('');

    // Review feedback (PR-level comment body, persisted server-side)
    const [feedbackBody, setFeedbackBody] = useState('');
    const [isSavingFeedback, setIsSavingFeedback] = useState(false);

    // Submit form
    const [reviewBody, setReviewBody] = useState('');
    const [reviewEvent, setReviewEvent] = useState('COMMENT');

    // LSP Hook
    const lsp = useLsp({
        mode: 'diff',
        repoPath: metadata?.repo_path || null,
        worktreePath: metadata?.worktree_path || '',
        repoName: repo,
        prNumber: number,
        diffContent: diff,
        enabled: !!diff && !!metadata,
    });

    // Dock state (floating viewers + split panels)
    const [dockState, setDockState] = useState<DockState>({
        codeViewers: [],
        split: false,
        left: { dockedTabs: [], activeTab: 'review' },
        right: { dockedTabs: [], activeTab: 'review' },
    });
    const [dragOverPanel, setDragOverPanel] = useState<PanelId | null>(null);

    // Convenience accessors
    const codeViewers = dockState.codeViewers;
    const dockedTabs = allDockedTabs(dockState);

    useEffect(() => {
        loadPR();
        loadPluginOutputs();
    }, [owner, repo, number]);

    useEffect(() => {
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                setShowPlugins(false);
                setShowCommentModal(false);
                setSubmitting(false);
                setActiveLineIndex(null);
                setActiveLspIndex(null);
                lsp.clearData();
                setActiveOutdatedFile(null);
            }
        };
        window.addEventListener('keydown', handleKeyDown);
        return () => window.removeEventListener('keydown', handleKeyDown);
    }, []);

    const loadPluginOutputs = async () => {
        try {
            const res = await rpcCall<{ output: Record<string, PluginResult> }>(
                'RPCHandler.GetPluginOutput',
                [
                    {
                        Owner: owner,
                        Repo: repo,
                        Number: number,
                    },
                ]
            );
            setPluginOutputs(res.output || {});
        } catch (e) {
            console.error('Failed to load plugin outputs:', e);
        }
    };

    const loadPR = async () => {
        setLoading(true);
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.GetPR', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                },
            ]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setReviews(res.reviews || []);
            setMetadata(res.metadata || null);
            setFeedbackBody(res.feedback || '');
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
            const res = await rpcCall<PRResponse>('RPCHandler.SyncPR', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                },
            ]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setReviews(res.reviews || []);
            setMetadata(res.metadata || null);
            setFeedbackBody(res.feedback || '');
            loadPluginOutputs();
        } catch (e) {
            console.error(e);
        } finally {
            setLoading(false);
        }
    };

    // Expand a hunk by fetching extra context lines from the server and
    // splicing them into the current diff text.
    const handleExpandHunk = async (
        hunkLineIndex: number,
        file: string,
        direction: 'before' | 'after',
        count: number = 20
    ) => {
        const lines = diff.split('\n');
        const hunkLine = lines[hunkLineIndex];
        if (!hunkLine) return;
        const hunkMatch = hunkLine.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/);
        if (!hunkMatch) return;
        const origStart = parseInt(hunkMatch[1], 10);
        const origLength = hunkMatch[2] !== undefined ? parseInt(hunkMatch[2], 10) : 1;
        const newStart = parseInt(hunkMatch[3], 10);
        const newLength = hunkMatch[4] !== undefined ? parseInt(hunkMatch[4], 10) : 1;
        const hunkHeader = hunkMatch[5].replace(/^\s+/, '');

        // Anchor: for "before", the first new-side line of the hunk; for "after",
        // the last new-side line of the hunk.
        const anchorLine = direction === 'before' ? newStart : newStart + newLength - 1;
        if (anchorLine < 1) return;

        try {
            const res = await getHunkContext({
                Owner: owner,
                Repo: repo,
                Number: number,
                Filename: file,
                Side: 'new',
                AnchorLine: anchorLine,
                Direction: direction,
                Count: count,
                OrigStart: origStart,
                OrigLength: origLength,
                NewStart: newStart,
                NewLength: newLength,
                HunkHeader: hunkHeader,
            });

            if (!res.lines || res.lines.length === 0) return;

            // Prefix each context line with a single space (diff convention for
            // unchanged lines).
            const contextLines = res.lines.map(l => ` ${l}`);

            const newLines = lines.slice();
            // Replace the hunk header with the updated range header from the server.
            if (res.range_header) {
                newLines[hunkLineIndex] = res.range_header;
            }

            if (direction === 'before') {
                newLines.splice(hunkLineIndex + 1, 0, ...contextLines);
            } else {
                // Find the end of this hunk: next hunk header, next file, or EOF.
                let endIdx = hunkLineIndex + 1;
                while (endIdx < newLines.length) {
                    const l = newLines[endIdx];
                    if (l.startsWith('@@ ') || l.startsWith('diff --git ')) break;
                    endIdx++;
                }
                newLines.splice(endIdx, 0, ...contextLines);
            }

            setDiff(newLines.join('\n'));
        } catch (e) {
            console.error('Failed to expand hunk context:', e);
        }
    };

    const handleNavigate = async (previous: boolean) => {
        setLoading(true);
        try {
            const res = await rpcCall<
                PRResponse & {
                    adjacent_owner: string;
                    adjacent_repo: string;
                    adjacent_number: number;
                }
            >('RPCHandler.GetAdjacentPR', [
                { Owner: owner, Repo: repo, Number: number, Previous: previous },
            ]);
            if (onNavigate) {
                onNavigate(
                    res.adjacent_owner || owner,
                    res.adjacent_repo || repo,
                    res.adjacent_number
                );
            }
        } catch (e: any) {
            console.error(e);
            const msg = e?.message || String(e);
            const parsed = (() => {
                try {
                    return JSON.parse(msg);
                } catch {
                    return null;
                }
            })();
            alert(parsed?.message ?? msg);
        } finally {
            setLoading(false);
        }
    };

    const resetCommentForm = () => {
        setFilename('');
        setPosition('');
        setCommentBody('');
        setReplyToId(null);
        setEditingCommentId(null);
        setEditingCommentBody('');
        setShowCommentModal(false);
        setActiveLineIndex(null);
        // Do not reset LSP index here as users might want to keep references open while adding a comment
    };

    const handleAddComment = async () => {
        if (!filename || !commentBody) return;
        setIsAddingComment(true);
        try {
            const params: any = {
                Owner: owner,
                Repo: repo,
                Number: number,
                Filename: filename,
                Position: parseInt(position, 10) || 0,
                Body: commentBody,
            };
            if (replyToId !== null) {
                params.ReplyToID = replyToId;
            }
            const res = await rpcCall<PRResponse>('RPCHandler.AddComment', [params]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setReviews(res.reviews || []);
            resetCommentForm();
        } catch (e) {
            console.error(e);
            alert('Error adding comment');
        } finally {
            setIsAddingComment(false);
        }
    };

    const handleDeleteComment = async (id: number) => {
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.DeleteComment', [
                { Owner: owner, Repo: repo, Number: number, ID: id },
            ]);
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setReviews(res.reviews || []);
        } catch (e) {
            console.error(e);
            alert('Error deleting comment');
        }
    };

    const handleEditComment = async () => {
        if (editingCommentId === null || !editingCommentBody) return;
        setIsAddingComment(true);
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.EditComment', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                    ID: editingCommentId,
                    Body: editingCommentBody,
                },
            ]);
            setContent(res.content || '');
            setDiff(res.diff || '');
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setReviews(res.reviews || []);
            resetCommentForm();
        } catch (e) {
            console.error(e);
            alert('Error editing comment');
        } finally {
            setIsAddingComment(false);
        }
    };

    const handleSubmitReview = async () => {
        setIsSubmittingReview(true);
        try {
            await rpcCall<PRResponse>('RPCHandler.SubmitReview', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                    Event: reviewEvent,
                    Body: reviewBody,
                },
            ]);
            setSubmitting(false);
            setReviewBody('');
            // Refresh everything after submission
            await handleSync();
        } catch (e) {
            console.error(e);
            alert('Error submitting review');
        } finally {
            setIsSubmittingReview(false);
        }
    };

    const handleSaveFeedback = async () => {
        setIsSavingFeedback(true);
        try {
            await rpcCall('RPCHandler.SetFeedback', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                    Body: feedbackBody,
                },
            ]);
        } catch (e) {
            console.error(e);
            alert('Error saving feedback');
        } finally {
            setIsSavingFeedback(false);
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
        origName?: string;
        originalLineIndex: number;
        // Source-side line numbers (old = base, new = head). null when N/A.
        oldLineNo?: number | null;
        newLineNo?: number | null;
        // For hunk headers: parsed function/context that appears after "@@".
        hunkContext?: string;
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
            let fallbackOrigName: string | null = null;
            let fallbackFileIndex: number | null = null;
            let hasEmittedHeader = false;

            // Per-hunk line-number cursors (old = base side, new = head side).
            let oldLineCursor = 0;
            let newLineCursor = 0;
            let currentHunkContext = '';

            lines.forEach((rawLine, index) => {
                const line = rawLine.replace(/\r$/, '');
                let clickable = false;
                let pos: number | null = null;
                let file: string | null = null;
                let lineType: LineType = 'code';
                let lineOldNo: number | null = null;
                let lineNewNo: number | null = null;

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
                            origName: fallbackOrigName || undefined,
                            originalLineIndex:
                                fallbackFileIndex !== null ? fallbackFileIndex : index,
                        });
                    }

                    // Reset for new file
                    hasEmittedHeader = false;
                    pendingFileStatus = 'modified';
                    fallbackOrigName = null;
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
                } else if (line.startsWith('rename from ')) {
                    pendingFileStatus = 'renamed';
                    fallbackOrigName = line.slice('rename from '.length).trim();
                    lineType = 'skip';
                } else if (line.startsWith('rename to ')) {
                    pendingFileStatus = 'renamed';
                    fallbackFilename = line.slice('rename to '.length).trim();
                    lineType = 'skip';
                } else if (line.startsWith('similarity index')) {
                    pendingFileStatus = 'renamed';
                    lineType = 'skip';
                } else if (line.startsWith('index ') || line.startsWith('---')) {
                    // Detect --- /dev/null: indicates a new file
                    if (line === '--- /dev/null') {
                        pendingFileStatus = 'new';
                    }
                    lineType = 'skip';
                } else {
                    // Match +++ b/filename as the file header
                    const fileMatch =
                        line.match(/^\+\+\+\s+b\/(.+)$/) || line.match(/^\+\+\+\s+(.+)$/);

                    if (fileMatch) {
                        const matchedFile = (fileMatch[1] || fileMatch[2]).trim();

                        if (matchedFile === '/dev/null') {
                            // +++ /dev/null means this is a deleted file.
                            // Use fallbackFilename (from diff --git) as the displayed name.
                            if (fallbackFilename) {
                                currentFile = fallbackFilename;
                                currentPos = 0;
                                foundFirstHunkInFile = false;
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
                                    fileStatus: 'deleted',
                                    originalLineIndex: index,
                                });
                                pendingFileStatus = 'modified';
                            }
                            return;
                        }

                        currentFile = matchedFile;
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
                            origName: fallbackOrigName || undefined,
                            originalLineIndex: index,
                        });
                        pendingFileStatus = 'modified'; // Reset for next file
                        fallbackOrigName = null;
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

                            // Seed per-side line cursors from the hunk header so we can
                            // render real line numbers alongside the diff.
                            const m = line.match(
                                /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/
                            );
                            if (m) {
                                oldLineCursor = parseInt(m[1], 10);
                                newLineCursor = parseInt(m[3], 10);
                                currentHunkContext = (m[5] || '').replace(/^\s+/, '');
                            }
                        } else if (isAddition || isDeletion || isContextLine) {
                            currentPos++;
                            pos = currentPos;
                            file = currentFile;
                            clickable = true;
                            lineType = isAddition ? 'addition' : isDeletion ? 'deletion' : 'code';

                            if (isAddition) {
                                lineNewNo = newLineCursor++;
                            } else if (isDeletion) {
                                lineOldNo = oldLineCursor++;
                            } else {
                                lineOldNo = oldLineCursor++;
                                lineNewNo = newLineCursor++;
                            }
                        }
                    }
                }

                if (lineType !== 'skip') {
                    parsedLines.push({
                        text: line,
                        file,
                        pos,
                        clickable,
                        lineType,
                        originalLineIndex: index,
                        oldLineNo: lineOldNo,
                        newLineNo: lineNewNo,
                        hunkContext: lineType === 'hunk' ? currentHunkContext : undefined,
                    });
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
                    origName: fallbackOrigName || undefined,
                    originalLineIndex:
                        fallbackFileIndex !== null ? fallbackFileIndex : lines.length,
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

    const handleCodeClick = async (
        idx: number,
        _file: string,
        _pos: number,
        originalLineIndex: number,
        col: number
    ) => {
        if (!lsp.available || !metadata?.repo_path) return;
        if (activeLspIndex === idx) {
            setActiveLspIndex(null);
            lsp.clearData();
        } else {
            const result = await lsp.query(originalLineIndex, col);
            if (result) {
                setActiveLspIndex(idx);
            }
        }
    };

    // Dock a floating code viewer as a tab
    const handleDockViewer = (viewerId: number, panel: PanelId = 'left') => {
        setDockState(prev => dockViewerState(prev, viewerId, panel));
    };

    // Undock a tab back to a floating modal
    const handleUndockTab = (tabId: number) => {
        setDockState(prev => undockTabState(prev, tabId));
    };

    // Close a docked tab
    const handleCloseDockedTab = (tabId: number) => {
        setDockState(prev => closeDockedTabState(prev, tabId));
    };

    // Dock a diff file as a tab
    const handleDockDiffFile = (filePath: string, panel: PanelId = 'left') => {
        setDockState(prev => dockDiffFileState(prev, filePath, panel));
    };

    // Toggle split pane
    const handleToggleSplit = () => {
        setDockState(prev => toggleSplit(prev));
    };

    // Set active tab for a specific panel
    const handleSetActiveTab = (panel: PanelId, tab: ActiveTab) => {
        setDockState(prev => ({
            ...prev,
            [panel]: { ...prev[panel], activeTab: tab },
        }));
    };

    // Handle drag-and-drop of tabs between panels
    const handleTabDragStart = useCallback(
        (e: React.DragEvent, tabId: number, sourcePanel: PanelId) => {
            e.dataTransfer.setData('application/x-tab', JSON.stringify({ tabId, sourcePanel }));
            e.dataTransfer.effectAllowed = 'move';
        },
        []
    );

    const handlePanelDragOver = useCallback((e: React.DragEvent) => {
        if (e.dataTransfer.types.includes('application/x-tab')) {
            e.preventDefault();
            e.dataTransfer.dropEffect = 'move';
        }
    }, []);

    const handlePanelDrop = useCallback((e: React.DragEvent, targetPanel: PanelId) => {
        e.preventDefault();
        setDragOverPanel(null);
        const raw = e.dataTransfer.getData('application/x-tab');
        if (!raw) return;
        const { tabId, sourcePanel } = JSON.parse(raw) as {
            tabId: number;
            sourcePanel: PanelId;
        };
        if (sourcePanel !== targetPanel) {
            setDockState(prev => moveTabToPanel(prev, tabId, targetPanel));
        }
    }, []);

    // Handle clicking on a reply thread to reply to the last message, or edit if local
    const handleThreadClick = (thread: Comment[], file: string, pos: number, lineIdx: number) => {
        const rc = thread[0];
        setFilename(file);
        setPosition(pos.toString());
        setActiveLineIndex(lineIdx);
        setShowCommentModal(false);
        if (rc.author === 'local') {
            setEditingCommentId(parseInt(rc.id, 10));
            setEditingCommentBody(rc.body);
            setReplyToId(null);
            setCommentBody('');
        } else {
            const lastComment = thread[thread.length - 1];
            setReplyToId(parseInt(lastComment.id, 10));
            setEditingCommentId(null);
            setEditingCommentBody('');
            setCommentBody('');
        }
    };

    // Custom theme based on oneDark but adjusted for diff context
    const customDiffTheme = useMemo(() => {
        const getBaseTheme = () => {
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
            linesData.forEach(lineData => {
                const lineCode = lineData.code || ' '; // Use space for empty lines
                result.set(
                    lineData.idx,
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
                            },
                        }}
                        PreTag="span"
                        CodeTag="span"
                    >
                        {lineCode}
                    </SyntaxHighlighter>
                );
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
            } else if (
                currentFile &&
                (item.lineType === 'addition' ||
                    item.lineType === 'deletion' ||
                    item.lineType === 'code')
            ) {
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
                return {
                    icon: '+',
                    label: 'new file',
                    color: colors.success,
                    bg: colors.bgSuccessDim,
                };
            case 'deleted':
                return {
                    icon: '−',
                    label: 'deleted',
                    color: colors.danger,
                    bg: colors.bgDangerDim,
                };
            case 'renamed':
                return {
                    icon: '→',
                    label: 'renamed',
                    color: colors.warning,
                    bg: colors.bgWarningDim,
                };
            default:
                return {
                    icon: '●',
                    label: 'modified',
                    color: colors.accent,
                    bg: colors.bgInfoDimStrong,
                };
        }
    };

    const isTestFile = (filename: string): boolean =>
        /\.(test|spec)\.[jt]sx?$/.test(filename) ||
        /_test\.[jt]sx?$/.test(filename) ||
        /_test\.go$/.test(filename) ||
        /\/__tests__\//.test(filename);

    const sortParsedLinesTestsLast = (lines: ParsedLine[]): ParsedLine[] => {
        const groups: ParsedLine[][] = [];
        let currentGroup: ParsedLine[] = [];
        for (const line of lines) {
            if (line.lineType === 'file-header') {
                if (currentGroup.length > 0) groups.push(currentGroup);
                currentGroup = [line];
            } else {
                currentGroup.push(line);
            }
        }
        if (currentGroup.length > 0) groups.push(currentGroup);
        groups.sort((a, b) => {
            const aIsTest = a[0].file ? isTestFile(a[0].file) : false;
            const bIsTest = b[0].file ? isTestFile(b[0].file) : false;
            if (aIsTest === bIsTest) return 0;
            return aIsTest ? 1 : -1;
        });
        return groups.flat();
    };

    const renderDiff = (filterFile?: string) => {
        const lines = sortParsedLinesTestsLast(parseDiff());
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

            // If filtering by file, skip lines not belonging to that file
            if (filterFile && currentFile !== filterFile) {
                return null;
            }

            // Skip rendering lines that belong to collapsed files (but always render file headers)
            if (!isFileHeader && currentFile && collapsedFiles.has(currentFile)) {
                return null;
            }

            // Render file header with clean styling
            if (isFileHeader) {
                const statusInfo = getFileStatusInfo(item.fileStatus);
                const isInlineActive = activeLineIndex === idx;
                const lineComments = item.file
                    ? comments.filter(
                          c =>
                              c.path === item.file &&
                              (c.position === item.pos?.toString() ||
                                  (c.position === '' && item.pos === 0))
                      )
                    : [];
                const rootComments = lineComments.filter(c => !c.in_reply_to);
                const isCollapsed = item.file ? collapsedFiles.has(item.file) : false;
                const fileOutdatedComments = item.file
                    ? outdatedComments.filter(c => c.path === item.file)
                    : [];
                const hasOutdated = fileOutdatedComments.length > 0;

                return (
                    <div key={idx} id={item.file ? `file-${slugify(item.file)}` : undefined}>
                        <div
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '10px',
                                padding: '10px 12px',
                                background:
                                    'linear-gradient(135deg, var(--bg-tertiary) 0%, var(--bg-secondary) 100%)',
                                borderTop: idx > 0 ? '2px solid var(--border)' : 'none',
                                borderBottom: '1px solid var(--border)',
                                marginTop: idx > 0 ? '8px' : '0',
                                cursor: 'pointer',
                                borderLeft: isInlineActive
                                    ? '3px solid var(--accent)'
                                    : '3px solid transparent',
                                position: 'sticky',
                                top: 'var(--review-file-sticky-top)',
                                zIndex: 6,
                            }}
                            onClick={() =>
                                item.file &&
                                item.pos !== null &&
                                handleCommentClick(idx, item.file, item.pos)
                            }
                            className="hover-line"
                            title={`Add comment to ${item.file}`}
                        >
                            <button
                                onClick={e => {
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
                            <span
                                style={{
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
                                }}
                            >
                                {statusInfo.icon}
                            </span>
                            <span
                                style={{
                                    fontSize: '11px',
                                    fontWeight: 500,
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.5px',
                                    color: statusInfo.color,
                                    minWidth: '60px',
                                }}
                            >
                                {statusInfo.label}
                            </span>
                            <span
                                style={{
                                    fontFamily: 'var(--font-mono)',
                                    fontSize: '13px',
                                    fontWeight: 500,
                                    color: 'var(--text-primary)',
                                }}
                            >
                                {item.fileStatus === 'renamed' && item.origName
                                    ? `${item.origName} → ${item.text}`
                                    : item.text}
                            </span>
                            {hasOutdated && (
                                <button
                                    onClick={e => {
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
                                        fontWeight: 500,
                                    }}
                                >
                                    {' '}
                                    <span>⚠️</span> Outdated Comments ({fileOutdatedComments.length}
                                    )
                                </button>
                            )}
                            {item.file && (
                                <button
                                    onClick={e => {
                                        e.stopPropagation();
                                        handleDockDiffFile(item.file!);
                                    }}
                                    style={{
                                        marginLeft: hasOutdated ? '6px' : 'auto',
                                        background: 'transparent',
                                        border: '1px solid var(--border)',
                                        color: 'var(--text-tertiary)',
                                        padding: '4px 8px',
                                        borderRadius: '4px',
                                        fontSize: '12px',
                                        cursor: 'pointer',
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: '4px',
                                        transition: 'color 0.15s ease',
                                    }}
                                    title="Open in tab"
                                >
                                    <span style={{ fontSize: '13px' }}>⊞</span>
                                </button>
                            )}
                        </div>
                        {rootComments.map(rc => {
                            const thread = [
                                rc,
                                ...lineComments.filter(c => c.in_reply_to === parseInt(rc.id, 10)),
                            ];
                            const isLocalComment = rc.author === 'local';
                            const isReplyingToThread =
                                (replyToId !== null &&
                                    thread.some(c => parseInt(c.id, 10) === replyToId)) ||
                                (editingCommentId !== null &&
                                    thread.some(c => parseInt(c.id, 10) === editingCommentId));
                            return (
                                <div
                                    key={rc.id}
                                    style={{
                                        margin: '10px 20px',
                                        border: isReplyingToThread
                                            ? '2px solid var(--accent)'
                                            : '1px solid var(--border)',
                                        borderRadius: '6px',
                                        background: 'var(--bg-primary)',
                                        overflow: 'hidden',
                                        cursor: 'pointer',
                                        transition: 'border-color 0.15s ease',
                                    }}
                                    onClick={e => {
                                        e.stopPropagation();
                                        if (item.file && item.pos !== null) {
                                            handleThreadClick(thread, item.file, item.pos, idx);
                                        }
                                    }}
                                    className="hover-thread"
                                    title={
                                        isLocalComment
                                            ? 'Click to edit this comment'
                                            : 'Click to reply to this thread'
                                    }
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
                                                <span
                                                    style={{
                                                        marginLeft: '6px',
                                                        opacity: 0.7,
                                                    }}
                                                >
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
                                                {isLocalComment
                                                    ? '✎ click to edit'
                                                    : '↩ click to reply'}
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
                                                    {c.created_at &&
                                                        !c.created_at.startsWith('0001') && (
                                                            <span
                                                                style={{
                                                                    opacity: 0.7,
                                                                    color: 'var(--text-secondary)',
                                                                }}
                                                            >
                                                                {new Date(
                                                                    c.created_at
                                                                ).toLocaleString()}
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
                                                <div style={{ whiteSpace: 'pre-wrap', flex: 1 }}>
                                                    {c.body}
                                                </div>
                                                {c.author === 'local' && (
                                                    <button
                                                        onClick={e => {
                                                            e.stopPropagation();
                                                            handleDeleteComment(parseInt(c.id, 10));
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
                        })}
                        {isInlineActive && (
                            <div
                                style={{
                                    padding: '10px 20px',
                                    background: 'var(--bg-primary)',
                                    borderBottom: '1px solid var(--border)',
                                    borderTop: '1px solid var(--border)',
                                    marginBottom: '10px',
                                }}
                            >
                                <div
                                    style={{
                                        marginBottom: '5px',
                                        fontSize: '12px',
                                        color: 'var(--text-secondary)',
                                    }}
                                >
                                    {editingCommentId !== null
                                        ? `Editing local comment #${editingCommentId}`
                                        : replyToId !== null
                                          ? `Replying to comment #${replyToId}`
                                          : `Commenting on ${item.file}:${item.pos}`}
                                </div>
                                {lsp.lspData && (
                                    <LspPopover
                                        hover={lsp.lspData.hover}
                                        refs={lsp.lspData.refs}
                                        definitions={lsp.lspData.definitions}
                                        typeDefinitions={lsp.lspData.typeDefinitions}
                                        variant="inline"
                                        onRefClick={(r, e) => {
                                            const filePath = r.uri.replace('file://', '');
                                            setDockState(prev => ({
                                                ...prev,
                                                codeViewers: [
                                                    ...prev.codeViewers,
                                                    {
                                                        id: Date.now(),
                                                        filePath,
                                                        line: r.range.start.line + 1,
                                                        position: {
                                                            x: e.clientX + 20,
                                                            y: e.clientY - 50,
                                                        },
                                                    },
                                                ],
                                            }));
                                        }}
                                        onClose={() => lsp.clearData()}
                                    />
                                )}
                                <textarea
                                    autoFocus
                                    placeholder={
                                        editingCommentId !== null
                                            ? 'Edit your comment...'
                                            : replyToId !== null
                                              ? 'Write a reply...'
                                              : 'Write a comment...'
                                    }
                                    value={
                                        editingCommentId !== null ? editingCommentBody : commentBody
                                    }
                                    onChange={e =>
                                        editingCommentId !== null
                                            ? setEditingCommentBody(e.target.value)
                                            : setCommentBody(e.target.value)
                                    }
                                    disabled={isAddingComment}
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
                                <div
                                    style={{
                                        display: 'flex',
                                        gap: '12px',
                                        justifyContent: 'flex-end',
                                    }}
                                >
                                    <Button
                                        onClick={() => {
                                            setActiveLineIndex(null);
                                            setReplyToId(null);
                                            setEditingCommentId(null);
                                            setEditingCommentBody('');
                                        }}
                                        variant="secondary"
                                        size="sm"
                                        disabled={isAddingComment}
                                    >
                                        Cancel
                                    </Button>
                                    <Button
                                        onClick={
                                            editingCommentId !== null
                                                ? handleEditComment
                                                : handleAddComment
                                        }
                                        size="sm"
                                        loading={isAddingComment}
                                    >
                                        {editingCommentId !== null
                                            ? 'Save Edit'
                                            : replyToId !== null
                                              ? 'Reply'
                                              : 'Add Comment'}
                                    </Button>
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
                prefixStyle = {
                    ...prefixStyle,
                    color: colors.success,
                    background: colors.diffAddGutterBg,
                };
            } else if (isDeletion) {
                containerStyle = { ...containerStyle, background: colors.diffDelBg };
                prefixStyle = {
                    ...prefixStyle,
                    color: colors.danger,
                    background: colors.diffDelGutterBg,
                };
            } else if (isHunkHeader) {
                containerStyle = { ...containerStyle, background: colors.diffHunkBg };
                lineStyle = {
                    ...lineStyle,
                    color: colors.accent,
                    fontStyle: 'italic',
                    fontSize: '12px',
                };
            }

            if (item.clickable) {
                containerStyle = { ...containerStyle, cursor: 'default' };
                prefixStyle = { ...prefixStyle, cursor: 'pointer' };
            }

            const isInlineActive = activeLineIndex === idx;
            const isLspActive = activeLspIndex === idx;
            const lineComments = item.file
                ? comments.filter(
                      c =>
                          c.path === item.file &&
                          (c.position === item.pos?.toString() ||
                              (c.position === '' && item.pos === 0))
                  )
                : [];
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

            return (
                <div key={idx}>
                    <div
                        className={
                            (item.clickable ? 'hover-line' : '') +
                            (isHunkHeader ? ' diff-hunk-row' : '')
                        }
                        style={{
                            ...containerStyle,
                            borderLeft:
                                isInlineActive || isLspActive
                                    ? '3px solid var(--accent)'
                                    : '3px solid transparent',
                            marginLeft: isInlineActive || isLspActive ? '-3px' : '0',
                        }}
                        title={undefined}
                    >
                        {isCodeLine && !isHunkHeader && (
                            <>
                                <span
                                    className="diff-line-no"
                                    aria-hidden="true"
                                    style={{
                                        background: isDeletion
                                            ? colors.diffDelGutterBg
                                            : isAddition
                                              ? undefined
                                              : undefined,
                                    }}
                                >
                                    {item.oldLineNo ?? ''}
                                </span>
                                <span
                                    className="diff-line-no"
                                    aria-hidden="true"
                                    style={{
                                        background: isAddition ? colors.diffAddGutterBg : undefined,
                                    }}
                                >
                                    {item.newLineNo ?? ''}
                                </span>
                            </>
                        )}
                        {isCodeLine && !isHunkHeader && (
                            <span
                                style={prefixStyle}
                                onClick={e => {
                                    if (item.clickable && item.file && item.pos !== null) {
                                        e.stopPropagation();
                                        handleCommentClick(idx, item.file, item.pos);
                                    }
                                }}
                                title={
                                    item.clickable
                                        ? `Add comment to ${item.file}:${item.pos}`
                                        : undefined
                                }
                            >
                                {isAddition ? '+' : isDeletion ? '-' : ''}
                            </span>
                        )}
                        {isHunkHeader && item.file && (
                            <span
                                style={{
                                    display: 'inline-flex',
                                    gap: '4px',
                                    padding: '0 6px',
                                    alignItems: 'center',
                                }}
                            >
                                <button
                                    type="button"
                                    onClick={e => {
                                        e.stopPropagation();
                                        handleExpandHunk(
                                            item.originalLineIndex,
                                            item.file!,
                                            'before'
                                        );
                                    }}
                                    title="Expand 20 lines before this hunk"
                                    style={{
                                        background: 'transparent',
                                        border: '1px solid var(--border)',
                                        color: 'var(--text-secondary)',
                                        borderRadius: '3px',
                                        padding: '0 6px',
                                        fontSize: '11px',
                                        lineHeight: '16px',
                                        cursor: 'pointer',
                                    }}
                                >
                                    ↑
                                </button>
                                <button
                                    type="button"
                                    onClick={e => {
                                        e.stopPropagation();
                                        handleExpandHunk(
                                            item.originalLineIndex,
                                            item.file!,
                                            'after'
                                        );
                                    }}
                                    title="Expand 20 lines after this hunk"
                                    style={{
                                        background: 'transparent',
                                        border: '1px solid var(--border)',
                                        color: 'var(--text-secondary)',
                                        borderRadius: '3px',
                                        padding: '0 6px',
                                        fontSize: '11px',
                                        lineHeight: '16px',
                                        cursor: 'pointer',
                                    }}
                                >
                                    ↓
                                </button>
                            </span>
                        )}
                        <span
                            style={{
                                ...lineStyle,
                                cursor: item.clickable ? 'pointer' : 'default',
                            }}
                            onClick={e => {
                                if (item.clickable && item.file && item.pos !== null) {
                                    const col = getClickColumn(e, e.currentTarget);
                                    handleCodeClick(
                                        idx,
                                        item.file,
                                        item.pos,
                                        item.originalLineIndex,
                                        col
                                    );
                                }
                            }}
                        >
                            {lineContent}
                        </span>
                        {isLspActive && lsp.lspData && (
                            <LspPopover
                                hover={lsp.lspData.hover}
                                refs={lsp.lspData.refs}
                                definitions={lsp.lspData.definitions}
                                typeDefinitions={lsp.lspData.typeDefinitions}
                                variant="floating"
                                onRefClick={(r, e) => {
                                    const filePath = r.uri.replace('file://', '');
                                    setDockState(prev => ({
                                        ...prev,
                                        codeViewers: [
                                            ...prev.codeViewers,
                                            {
                                                id: Date.now(),
                                                filePath,
                                                line: r.range.start.line + 1,
                                                position: {
                                                    x: e.clientX + 20,
                                                    y: e.clientY - 50,
                                                },
                                            },
                                        ],
                                    }));
                                }}
                                onClose={() => {
                                    setActiveLspIndex(null);
                                    lsp.clearData();
                                }}
                            />
                        )}
                    </div>
                    {rootComments.map(rc => {
                        const thread = [
                            rc,
                            ...lineComments.filter(c => c.in_reply_to === parseInt(rc.id, 10)),
                        ];
                        const isLocalComment = rc.author === 'local';
                        const isReplyingToThread =
                            (replyToId !== null &&
                                thread.some(c => parseInt(c.id, 10) === replyToId)) ||
                            (editingCommentId !== null &&
                                thread.some(c => parseInt(c.id, 10) === editingCommentId));
                        return (
                            <div
                                key={rc.id}
                                style={{
                                    margin: '10px 20px',
                                    border: isReplyingToThread
                                        ? '2px solid var(--accent)'
                                        : '1px solid var(--border)',
                                    borderRadius: '6px',
                                    background: 'var(--bg-primary)',
                                    overflow: 'hidden',
                                    cursor: 'pointer',
                                    transition: 'border-color 0.15s ease',
                                }}
                                onClick={e => {
                                    e.stopPropagation();
                                    if (item.file && item.pos !== null) {
                                        handleThreadClick(thread, item.file, item.pos, idx);
                                    }
                                }}
                                className="hover-thread"
                                title={
                                    isLocalComment
                                        ? 'Click to edit this comment'
                                        : 'Click to reply to this thread'
                                }
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
                                            {isLocalComment
                                                ? '✎ click to edit'
                                                : '↩ click to reply'}
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
                                                }}
                                            >
                                                Reply by {c.author}:
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
                                            <div style={{ whiteSpace: 'pre-wrap', flex: 1 }}>
                                                {c.body}
                                            </div>
                                            {c.author === 'local' && (
                                                <button
                                                    onClick={e => {
                                                        e.stopPropagation();
                                                        handleDeleteComment(parseInt(c.id, 10));
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
                    })}
                    {isInlineActive && (
                        <div
                            style={{
                                padding: '10px 20px',
                                background: 'var(--bg-primary)',
                                borderBottom: '1px solid var(--border)',
                                borderTop: '1px solid var(--border)',
                                marginBottom: '10px',
                            }}
                        >
                            <div
                                style={{
                                    marginBottom: '5px',
                                    fontSize: '12px',
                                    color: 'var(--text-secondary)',
                                }}
                            >
                                {editingCommentId !== null
                                    ? `Editing local comment #${editingCommentId}`
                                    : replyToId !== null
                                      ? `Replying to comment #${replyToId}`
                                      : `Commenting on ${item.file}:${item.pos}`}
                            </div>
                            <textarea
                                autoFocus
                                placeholder={
                                    editingCommentId !== null
                                        ? 'Edit your comment...'
                                        : replyToId !== null
                                          ? 'Write a reply...'
                                          : 'Write a comment...'
                                }
                                value={editingCommentId !== null ? editingCommentBody : commentBody}
                                onChange={e =>
                                    editingCommentId !== null
                                        ? setEditingCommentBody(e.target.value)
                                        : setCommentBody(e.target.value)
                                }
                                disabled={isAddingComment}
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
                            <div
                                style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}
                            >
                                <Button
                                    onClick={() => {
                                        setActiveLineIndex(null);
                                        setReplyToId(null);
                                        setEditingCommentId(null);
                                        setEditingCommentBody('');
                                    }}
                                    variant="secondary"
                                    size="sm"
                                    disabled={isAddingComment}
                                >
                                    Cancel
                                </Button>
                                <Button
                                    onClick={
                                        editingCommentId !== null
                                            ? handleEditComment
                                            : handleAddComment
                                    }
                                    size="sm"
                                    loading={isAddingComment}
                                >
                                    {editingCommentId !== null
                                        ? 'Save Edit'
                                        : replyToId !== null
                                          ? 'Reply'
                                          : 'Add Comment'}
                                </Button>
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
            return {
                background: colors.bgSuccessDim,
                color: colors.textSuccess,
                borderColor: colors.borderSuccessDim,
            };
        } else if (status === 'pending' || status === 'running') {
            return {
                background: colors.bgWarningDim,
                color: colors.textWarning,
                borderColor: colors.borderWarningDim,
            };
        } else if (status === 'failure' || status === 'failed') {
            return {
                background: colors.bgDangerDim,
                color: colors.textDanger,
                borderColor: colors.borderDangerDim,
            };
        }
        return {
            background: colors.bgTertiary,
            color: colors.textSecondary,
            borderColor: colors.border,
        };
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
                <div
                    className="pr-header"
                    style={{
                        background:
                            'linear-gradient(135deg, var(--bg-secondary) 0%, var(--bg-primary) 100%)',
                        borderRadius: '12px',
                        border: '1px solid var(--border)',
                        marginBottom: '16px',
                        overflow: 'hidden',
                    }}
                >
                    {/* Title Bar */}
                    <div
                        style={{
                            padding: '20px 24px',
                            borderBottom: '1px solid var(--border)',
                            background: 'var(--bg-secondary)',
                        }}
                    >
                        <div style={{ display: 'flex', alignItems: 'flex-start', gap: '16px' }}>
                            <div style={{ flex: 1 }}>
                                <div
                                    style={{
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: '12px',
                                        marginBottom: '8px',
                                        flexWrap: 'wrap',
                                    }}
                                >
                                    <span
                                        style={{
                                            ...getStateStyle(),
                                            padding: '4px 12px',
                                            borderRadius: '16px',
                                            fontSize: '12px',
                                            fontWeight: 600,
                                            textTransform: 'uppercase',
                                            letterSpacing: '0.5px',
                                        }}
                                    >
                                        {metadata.draft ? '📝 Draft' : metadata.state}
                                    </span>
                                    <span
                                        style={{ color: 'var(--text-secondary)', fontSize: '14px' }}
                                    >
                                        #{metadata.number}
                                    </span>
                                </div>
                                <h1
                                    style={{
                                        margin: 0,
                                        fontSize: '22px',
                                        fontWeight: 600,
                                        color: 'var(--text-primary)',
                                        lineHeight: 1.3,
                                    }}
                                >
                                    {metadata.title}
                                </h1>
                            </div>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
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
                                        fontSize: '13px',
                                    }}
                                >
                                    <span>↗</span> GitHub
                                </a>
                                <CopyUrlButton url={metadata.url} />
                            </div>
                        </div>
                    </div>

                    {/* Info Grid */}
                    <div
                        style={{
                            display: 'grid',
                            gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))',
                            gap: '1px',
                            background: 'var(--border)',
                        }}
                    >
                        {/* Branch Info */}
                        <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                            <div
                                style={{
                                    fontSize: '11px',
                                    color: 'var(--text-secondary)',
                                    marginBottom: '6px',
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.5px',
                                }}
                            >
                                Branch
                            </div>
                            <div
                                style={{
                                    fontFamily: 'var(--font-mono)',
                                    fontSize: '13px',
                                    color: 'var(--accent)',
                                }}
                            >
                                <span style={{ color: 'var(--text-secondary)' }}>
                                    {metadata.base_ref}
                                </span>
                                <span style={{ margin: '0 8px', color: 'var(--text-tertiary)' }}>
                                    ←
                                </span>
                                <span>{metadata.head_ref}</span>
                            </div>
                        </div>

                        {/* Author */}
                        <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                            <div
                                style={{
                                    fontSize: '11px',
                                    color: 'var(--text-secondary)',
                                    marginBottom: '6px',
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.5px',
                                }}
                            >
                                Author
                            </div>
                            <div
                                style={{
                                    fontSize: '14px',
                                    fontWeight: 500,
                                    color: 'var(--text-primary)',
                                }}
                            >
                                @{metadata.author}
                            </div>
                        </div>

                        {/* CI Status */}
                        {metadata.ci_status && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div
                                    style={{
                                        fontSize: '11px',
                                        color: 'var(--text-secondary)',
                                        marginBottom: '6px',
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.5px',
                                    }}
                                >
                                    CI Status
                                </div>
                                <div
                                    style={{
                                        display: 'inline-flex',
                                        alignItems: 'center',
                                        gap: '6px',
                                        padding: '4px 10px',
                                        borderRadius: '6px',
                                        fontSize: '13px',
                                        fontWeight: 500,
                                        border: '1px solid',
                                        ...getCIStatusStyle(),
                                    }}
                                >
                                    {metadata.ci_status.toLowerCase() === 'success' && '✓'}
                                    {metadata.ci_status.toLowerCase() === 'pending' && '○'}
                                    {metadata.ci_status.toLowerCase() === 'failure' && '✗'}
                                    {metadata.ci_status}
                                </div>
                            </div>
                        )}

                        {/* Reviewers/Teams */}
                        {((metadata.reviewers && metadata.reviewers.length > 0) ||
                            (metadata.requested_teams && metadata.requested_teams.length > 0)) && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div
                                    style={{
                                        fontSize: '11px',
                                        color: 'var(--text-secondary)',
                                        marginBottom: '6px',
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.5px',
                                    }}
                                >
                                    Requested Reviewers
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.reviewers?.map((r, i) => (
                                        <span
                                            key={`rev-${i}`}
                                            style={{
                                                background: 'var(--bg-tertiary)',
                                                padding: '2px 8px',
                                                borderRadius: '4px',
                                                fontSize: '12px',
                                                color: 'var(--text-primary)',
                                            }}
                                        >
                                            @{r}
                                        </span>
                                    ))}
                                    {metadata.requested_teams?.map((t, i) => (
                                        <span
                                            key={`team-${i}`}
                                            style={{
                                                background: colors.bgInfoDim,
                                                padding: '2px 8px',
                                                borderRadius: '4px',
                                                fontSize: '12px',
                                                color: colors.accent,
                                                border: `1px solid ${colors.borderInfoDim}`,
                                            }}
                                        >
                                            team:{t}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}

                        {/* Approved By */}
                        {metadata.approved_by && metadata.approved_by.length > 0 && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div
                                    style={{
                                        fontSize: '11px',
                                        color: 'var(--success)',
                                        marginBottom: '6px',
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.5px',
                                    }}
                                >
                                    ✓ Approved By
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.approved_by.map((r, i) => (
                                        <span
                                            key={i}
                                            style={{
                                                background: colors.bgSuccessDim,
                                                padding: '2px 8px',
                                                borderRadius: '4px',
                                                fontSize: '12px',
                                                color: colors.success,
                                                border: `1px solid ${colors.borderSuccessDim}`,
                                            }}
                                        >
                                            @{r}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}

                        {/* Changes Requested By */}
                        {metadata.changes_requested_by &&
                            metadata.changes_requested_by.length > 0 && (
                                <div
                                    style={{
                                        padding: '16px 20px',
                                        background: 'var(--bg-primary)',
                                    }}
                                >
                                    <div
                                        style={{
                                            fontSize: '11px',
                                            color: 'var(--danger)',
                                            marginBottom: '6px',
                                            textTransform: 'uppercase',
                                            letterSpacing: '0.5px',
                                        }}
                                    >
                                        ✗ Changes Requested By
                                    </div>
                                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                        {metadata.changes_requested_by.map((r, i) => (
                                            <span
                                                key={i}
                                                style={{
                                                    background: colors.bgDangerDim,
                                                    padding: '2px 8px',
                                                    borderRadius: '4px',
                                                    fontSize: '12px',
                                                    color: colors.danger,
                                                    border: `1px solid ${colors.borderDangerDim}`,
                                                }}
                                            >
                                                @{r}
                                            </span>
                                        ))}
                                    </div>
                                </div>
                            )}

                        {/* Commented By */}
                        {metadata.commented_by && metadata.commented_by.length > 0 && (
                            <div style={{ padding: '16px 20px', background: 'var(--bg-primary)' }}>
                                <div
                                    style={{
                                        fontSize: '11px',
                                        color: 'var(--text-secondary)',
                                        marginBottom: '6px',
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.5px',
                                    }}
                                >
                                    💬 Commented By
                                </div>
                                <div style={{ display: 'flex', flexWrap: 'wrap', gap: '6px' }}>
                                    {metadata.commented_by.map((r, i) => (
                                        <span
                                            key={i}
                                            style={{
                                                background: 'var(--bg-tertiary)',
                                                padding: '2px 8px',
                                                borderRadius: '4px',
                                                fontSize: '12px',
                                                color: 'var(--text-primary)',
                                            }}
                                        >
                                            @{r}
                                        </span>
                                    ))}
                                </div>
                            </div>
                        )}
                    </div>

                    {/* Labels, Assignees, Milestone Row */}
                    {(metadata.labels?.length > 0 ||
                        metadata.assignees?.length > 0 ||
                        metadata.milestone) && (
                        <div
                            style={{
                                padding: '14px 20px',
                                display: 'flex',
                                flexWrap: 'wrap',
                                gap: '20px',
                                borderTop: '1px solid var(--border)',
                                background: 'var(--bg-primary)',
                            }}
                        >
                            {metadata.labels && metadata.labels.length > 0 && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span
                                        style={{
                                            fontSize: '11px',
                                            color: 'var(--text-secondary)',
                                            textTransform: 'uppercase',
                                        }}
                                    >
                                        Labels:
                                    </span>
                                    <div style={{ display: 'flex', gap: '6px', flexWrap: 'wrap' }}>
                                        {metadata.labels.map((label, i) => (
                                            <span
                                                key={i}
                                                style={{
                                                    background: colors.bgMergedDim,
                                                    color: colors.textMerged,
                                                    padding: '2px 10px',
                                                    borderRadius: '12px',
                                                    fontSize: '11px',
                                                    fontWeight: 500,
                                                    border: `1px solid ${colors.borderMergedDim}`,
                                                }}
                                            >
                                                {label}
                                            </span>
                                        ))}
                                    </div>
                                </div>
                            )}
                            {metadata.assignees && metadata.assignees.length > 0 && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span
                                        style={{
                                            fontSize: '11px',
                                            color: 'var(--text-secondary)',
                                            textTransform: 'uppercase',
                                        }}
                                    >
                                        Assignees:
                                    </span>
                                    <span
                                        style={{ fontSize: '13px', color: 'var(--text-primary)' }}
                                    >
                                        {metadata.assignees.map(a => `@${a}`).join(', ')}
                                    </span>
                                </div>
                            )}
                            {metadata.milestone && (
                                <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                                    <span
                                        style={{
                                            fontSize: '11px',
                                            color: 'var(--text-secondary)',
                                            textTransform: 'uppercase',
                                        }}
                                    >
                                        Milestone:
                                    </span>
                                    <span style={{ fontSize: '13px', color: 'var(--accent)' }}>
                                        {metadata.milestone}
                                    </span>
                                </div>
                            )}
                        </div>
                    )}

                    {/* Description (collapsible — diff is the focus) */}
                    {metadata.body && (
                        <div
                            style={{
                                borderTop: '1px solid var(--border)',
                                background: 'var(--bg-primary)',
                            }}
                        >
                            <button
                                type="button"
                                onClick={() => setDescCollapsed(c => !c)}
                                style={{
                                    width: '100%',
                                    background: 'transparent',
                                    border: 'none',
                                    padding: '10px 20px',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '8px',
                                    cursor: 'pointer',
                                    color: 'var(--text-secondary)',
                                    fontSize: '11px',
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.5px',
                                    textAlign: 'left',
                                }}
                                aria-expanded={!descCollapsed}
                            >
                                <span
                                    style={{
                                        display: 'inline-block',
                                        transform: descCollapsed
                                            ? 'rotate(-90deg)'
                                            : 'rotate(0deg)',
                                        transition: 'transform 0.15s ease',
                                        fontSize: '10px',
                                    }}
                                >
                                    ▼
                                </span>
                                Description
                                {descCollapsed && (
                                    <span
                                        style={{
                                            marginLeft: '8px',
                                            fontSize: '11px',
                                            color: 'var(--text-tertiary)',
                                            textTransform: 'none',
                                            letterSpacing: 0,
                                        }}
                                    >
                                        — click to expand
                                    </span>
                                )}
                            </button>
                            {!descCollapsed && (
                                <div
                                    className="pr-description"
                                    style={{
                                        fontSize: '14px',
                                        lineHeight: 1.6,
                                        color: 'var(--text-primary)',
                                        maxHeight: '300px',
                                        overflow: 'auto',
                                        padding: '0 20px 16px',
                                    }}
                                >
                                    <Markdown>{stripHtmlComments(metadata.body)}</Markdown>
                                </div>
                            )}
                        </div>
                    )}

                    {/* CI Failures */}
                    {metadata.ci_failures && metadata.ci_failures.length > 0 && (
                        <div
                            style={{
                                padding: '14px 20px',
                                borderTop: `1px solid ${colors.border}`,
                                background: colors.bgDangerDim,
                            }}
                        >
                            <div
                                style={{
                                    fontSize: '11px',
                                    color: colors.textDanger,
                                    marginBottom: '8px',
                                    textTransform: 'uppercase',
                                    letterSpacing: '0.5px',
                                }}
                            >
                                ✗ CI Failures
                            </div>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '4px' }}>
                                {metadata.ci_failures.map((failure, i) => (
                                    <span
                                        key={i}
                                        style={{
                                            fontFamily: 'var(--font-mono)',
                                            fontSize: '12px',
                                            color: colors.textDanger,
                                        }}
                                    >
                                        • {failure}
                                    </span>
                                ))}
                            </div>
                        </div>
                    )}
                </div>
            )}

            {/* Review Discussion */}
            {reviews.filter(r => r.body && r.body.trim()).length > 0 && (
                <div
                    style={{
                        background: 'var(--bg-secondary)',
                        borderRadius: '8px',
                        border: '1px solid var(--border)',
                        marginBottom: '16px',
                        overflow: 'hidden',
                    }}
                >
                    <div
                        style={{
                            padding: '12px 16px',
                            borderBottom: '1px solid var(--border)',
                            background: 'var(--bg-primary)',
                            fontSize: '13px',
                            fontWeight: 500,
                            color: 'var(--text-secondary)',
                        }}
                    >
                        Review Discussion ({reviews.filter(r => r.body && r.body.trim()).length})
                    </div>
                    <div style={{ display: 'flex', flexDirection: 'column' }}>
                        {reviews
                            .filter(r => r.body && r.body.trim())
                            .sort(
                                (a, b) =>
                                    new Date(a.submitted_at).getTime() -
                                    new Date(b.submitted_at).getTime()
                            )
                            .map(r => (
                                <div
                                    key={r.id}
                                    style={{
                                        padding: '14px 16px',
                                        borderBottom: '1px solid var(--border)',
                                    }}
                                >
                                    <div
                                        style={{
                                            display: 'flex',
                                            alignItems: 'center',
                                            gap: '8px',
                                            marginBottom: '8px',
                                        }}
                                    >
                                        <span
                                            style={{
                                                fontWeight: 600,
                                                fontSize: '13px',
                                                color: 'var(--text-primary)',
                                            }}
                                        >
                                            @{r.user}
                                        </span>
                                        <span
                                            style={{
                                                fontSize: '11px',
                                                padding: '1px 6px',
                                                borderRadius: '4px',
                                                fontWeight: 500,
                                                ...(r.state === 'APPROVED'
                                                    ? {
                                                          background: colors.bgSuccessDim,
                                                          color: colors.success,
                                                          border: `1px solid ${colors.borderSuccessDim}`,
                                                      }
                                                    : r.state === 'CHANGES_REQUESTED'
                                                      ? {
                                                            background: colors.bgDangerDim,
                                                            color: colors.danger,
                                                            border: `1px solid ${colors.borderDangerDim}`,
                                                        }
                                                      : {
                                                            background: 'var(--bg-tertiary)',
                                                            color: 'var(--text-secondary)',
                                                            border: '1px solid var(--border)',
                                                        }),
                                            }}
                                        >
                                            {r.state === 'APPROVED'
                                                ? 'Approved'
                                                : r.state === 'CHANGES_REQUESTED'
                                                  ? 'Changes Requested'
                                                  : 'Commented'}
                                        </span>
                                        <span
                                            style={{
                                                fontSize: '11px',
                                                color: 'var(--text-secondary)',
                                                marginLeft: 'auto',
                                            }}
                                        >
                                            {new Date(r.submitted_at).toLocaleString()}
                                        </span>
                                        {r.html_url && (
                                            <a
                                                href={r.html_url}
                                                target="_blank"
                                                rel="noopener noreferrer"
                                                style={{
                                                    fontSize: '11px',
                                                    color: 'var(--accent)',
                                                    textDecoration: 'none',
                                                }}
                                            >
                                                View
                                            </a>
                                        )}
                                    </div>
                                    <div
                                        className="pr-description"
                                        style={{
                                            fontSize: '14px',
                                            lineHeight: 1.6,
                                            color: 'var(--text-primary)',
                                        }}
                                    >
                                        <Markdown>{stripHtmlComments(r.body)}</Markdown>
                                    </div>
                                </div>
                            ))}
                    </div>
                </div>
            )}

            {/* Toolbar */}
            <div
                className="toolbar"
                style={{
                    display: 'flex',
                    gap: '10px',
                    marginBottom: '16px',
                    padding: '12px 16px',
                    background: 'var(--bg-secondary)',
                    borderRadius: '8px',
                    border: '1px solid var(--border)',
                    position: 'sticky',
                    top: '10px',
                    zIndex: 10,
                }}
            >
                <Button
                    onClick={() => handleNavigate(true)}
                    variant="secondary"
                    disabled={loading}
                    title="Previous PR"
                >
                    ← Prev PR
                </Button>
                <Button
                    onClick={() => handleNavigate(false)}
                    variant="secondary"
                    disabled={loading}
                    title="Next PR"
                >
                    Next PR →
                </Button>
                <Button onClick={handleSync} loading={loading}>
                    {loading ? 'Syncing...' : '↻ Sync'}
                </Button>
                <Button
                    onClick={() => {
                        if (collapsedFiles.size > 0) {
                            expandAllFiles();
                        } else {
                            collapseAllFiles();
                        }
                    }}
                    variant="secondary"
                    size="sm"
                    disabled={loading}
                >
                    {collapsedFiles.size > 0 ? '▼ Expand All' : '◀ Collapse All'}
                </Button>
                <Button
                    onClick={() => {
                        setFilename('');
                        setPosition('');
                        setShowCommentModal(true);
                    }}
                    disabled={loading}
                >
                    + Comment
                </Button>
                <Button
                    onClick={() => {
                        setReviewBody(feedbackBody);
                        setSubmitting(true);
                    }}
                    style={{ background: 'var(--success)' }}
                    disabled={loading}
                >
                    Submit Review
                </Button>
                <Button
                    onClick={() => {
                        setShowPlugins(!showPlugins);
                        if (!showPlugins) loadPluginOutputs();
                    }}
                    variant={showPlugins ? 'primary' : 'secondary'}
                >
                    Plugins{' '}
                    {Object.keys(pluginOutputs).length > 0
                        ? `(${Object.keys(pluginOutputs).length})`
                        : ''}
                </Button>

                <span
                    title="Click any line of code to comment. Click a file pill below to jump to that file."
                    style={{
                        marginLeft: 'auto',
                        display: 'inline-flex',
                        alignItems: 'center',
                        justifyContent: 'center',
                        width: '24px',
                        height: '24px',
                        borderRadius: '50%',
                        border: '1px solid var(--border)',
                        color: 'var(--text-secondary)',
                        fontSize: '12px',
                        cursor: 'help',
                        userSelect: 'none',
                    }}
                >
                    ?
                </span>
            </div>

            {/* File index — jump to any file in the diff */}
            {(() => {
                const parsed = parseDiff();
                const fileHeaders = parsed.filter(p => p.lineType === 'file-header' && p.file);
                if (fileHeaders.length === 0) return null;

                // Tally additions/deletions per file from the parsed lines.
                const counts = new Map<string, { add: number; del: number }>();
                let curFile: string | null = null;
                for (const p of parsed) {
                    if (p.lineType === 'file-header') {
                        curFile = p.file;
                        if (curFile && !counts.has(curFile)) {
                            counts.set(curFile, { add: 0, del: 0 });
                        }
                    } else if (curFile && counts.has(curFile)) {
                        const c = counts.get(curFile)!;
                        if (p.lineType === 'addition') c.add++;
                        else if (p.lineType === 'deletion') c.del++;
                    }
                }

                const scrollToFile = (file: string) => {
                    // Expand the file if it was collapsed so the diff is visible.
                    if (collapsedFiles.has(file)) {
                        setCollapsedFiles(prev => {
                            const next = new Set(prev);
                            next.delete(file);
                            return next;
                        });
                    }
                    requestAnimationFrame(() => {
                        const el = document.getElementById(`file-${slugify(file)}`);
                        if (!el) return;
                        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
                        el.classList.remove('file-flash');
                        // Re-trigger the CSS animation.
                        void el.offsetWidth;
                        el.classList.add('file-flash');
                    });
                };

                return (
                    <div className="file-index" aria-label="Files changed">
                        <span
                            style={{
                                fontSize: '11px',
                                color: 'var(--text-tertiary)',
                                textTransform: 'uppercase',
                                letterSpacing: '0.5px',
                                marginRight: '4px',
                                alignSelf: 'center',
                            }}
                        >
                            {fileHeaders.length} file{fileHeaders.length === 1 ? '' : 's'}
                        </span>
                        {fileHeaders.map(p => {
                            if (!p.file) return null;
                            const info = getFileStatusInfo(p.fileStatus);
                            const c = counts.get(p.file) || { add: 0, del: 0 };
                            const shortName = p.file.length > 40 ? '…' + p.file.slice(-39) : p.file;
                            return (
                                <button
                                    key={p.file}
                                    type="button"
                                    onClick={() => scrollToFile(p.file!)}
                                    className="file-index-item"
                                    title={p.file}
                                >
                                    <span style={{ color: info.color, fontWeight: 600 }}>
                                        {info.icon}
                                    </span>
                                    <span>{shortName}</span>
                                    {c.add > 0 && <span className="delta-add">+{c.add}</span>}
                                    {c.del > 0 && <span className="delta-del">−{c.del}</span>}
                                </button>
                            );
                        })}
                    </div>
                );
            })()}

            {/* Dock zone for floating modal drag-to-dock */}
            {dockedTabs.length > 0 ? (
                <div
                    data-dock-zone
                    style={{
                        height: '0px',
                        overflow: 'hidden',
                    }}
                />
            ) : (
                <div
                    data-dock-zone
                    style={{
                        height: '0px',
                        overflow: 'hidden',
                        transition: 'all 0.2s ease',
                        borderRadius: '8px',
                        marginBottom: '0',
                    }}
                >
                    <style>{`
                        [data-dock-zone][data-dock-hover="true"] {
                            height: 40px !important;
                            overflow: visible !important;
                            background: var(--bg-secondary) !important;
                            border: 2px dashed var(--accent) !important;
                            margin-bottom: 8px !important;
                            display: flex !important;
                            align-items: center !important;
                            justify-content: center !important;
                        }
                    `}</style>
                    <span style={{ color: 'var(--accent)', fontSize: '13px', fontWeight: 500 }}>
                        Drop here to pin as tab
                    </span>
                </div>
            )}

            {/* Split Pane Layout */}
            {(() => {
                const hasTabs = dockedTabs.length > 0;

                // Render a panel's tab bar
                const renderTabBar = (
                    panelId: PanelId,
                    panel: PanelState,
                    showSplitBtn: boolean
                ) => (
                    <div
                        data-dock-zone={panelId === 'left' ? '' : undefined}
                        style={{
                            display: 'flex',
                            alignItems: 'stretch',
                            gap: '0',
                            marginBottom: '0',
                            background: 'var(--bg-secondary)',
                            borderRadius: '8px 8px 0 0',
                            border: '1px solid var(--border)',
                            borderBottom: 'none',
                            overflow: 'hidden',
                            transition: 'box-shadow 0.2s ease',
                            ...(dragOverPanel === panelId
                                ? { boxShadow: 'inset 0 0 0 2px var(--accent)' }
                                : {}),
                        }}
                        onDragOver={handlePanelDragOver}
                        onDragEnter={() => setDragOverPanel(panelId)}
                        onDragLeave={e => {
                            if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                                setDragOverPanel(null);
                            }
                        }}
                        onDrop={e => handlePanelDrop(e, panelId)}
                    >
                        <button
                            onClick={() => handleSetActiveTab(panelId, 'review')}
                            style={{
                                padding: '10px 20px',
                                background:
                                    panel.activeTab === 'review'
                                        ? 'var(--bg-primary)'
                                        : 'transparent',
                                border: 'none',
                                borderBottom:
                                    panel.activeTab === 'review'
                                        ? '2px solid var(--accent)'
                                        : '2px solid transparent',
                                color:
                                    panel.activeTab === 'review'
                                        ? 'var(--text-primary)'
                                        : 'var(--text-secondary)',
                                fontSize: '13px',
                                fontWeight: panel.activeTab === 'review' ? 600 : 400,
                                cursor: 'pointer',
                                display: 'flex',
                                alignItems: 'center',
                                gap: '6px',
                                transition: 'all 0.15s ease',
                            }}
                        >
                            <span style={{ color: 'var(--accent)' }}>◈</span>
                            Review
                        </button>
                        {panel.dockedTabs.map(tab => {
                            const tabFilename = tab.filePath.split('/').pop() || tab.filePath;
                            const isActive = panel.activeTab === tab.id;
                            return (
                                <div
                                    key={tab.id}
                                    draggable
                                    onDragStart={e => handleTabDragStart(e, tab.id, panelId)}
                                    style={{
                                        display: 'flex',
                                        alignItems: 'center',
                                        background: isActive ? 'var(--bg-primary)' : 'transparent',
                                        borderBottom: isActive
                                            ? '2px solid var(--accent)'
                                            : '2px solid transparent',
                                        transition: 'all 0.15s ease',
                                        cursor: 'grab',
                                    }}
                                >
                                    <button
                                        onClick={() => handleSetActiveTab(panelId, tab.id)}
                                        style={{
                                            padding: '10px 12px',
                                            background: 'transparent',
                                            border: 'none',
                                            color: isActive
                                                ? 'var(--text-primary)'
                                                : 'var(--text-secondary)',
                                            fontSize: '13px',
                                            fontWeight: isActive ? 600 : 400,
                                            cursor: 'inherit',
                                            fontFamily: 'var(--font-mono)',
                                            display: 'flex',
                                            alignItems: 'center',
                                            gap: '6px',
                                        }}
                                        title={tab.filePath}
                                    >
                                        {tabFilename}
                                        {tab.line > 1 && (
                                            <span
                                                style={{
                                                    fontSize: '11px',
                                                    color: 'var(--text-tertiary)',
                                                }}
                                            >
                                                :{tab.line}
                                            </span>
                                        )}
                                    </button>
                                    <button
                                        onClick={e => {
                                            e.stopPropagation();
                                            handleCloseDockedTab(tab.id);
                                        }}
                                        style={{
                                            background: 'transparent',
                                            border: 'none',
                                            color: 'var(--text-tertiary)',
                                            fontSize: '14px',
                                            cursor: 'pointer',
                                            padding: '4px 8px 4px 0',
                                            lineHeight: 1,
                                        }}
                                        title="Close tab"
                                    >
                                        ×
                                    </button>
                                </div>
                            );
                        })}
                        {showSplitBtn && (
                            <button
                                onClick={handleToggleSplit}
                                style={{
                                    marginLeft: 'auto',
                                    padding: '6px 12px',
                                    background: 'transparent',
                                    border: 'none',
                                    color: dockState.split
                                        ? 'var(--accent)'
                                        : 'var(--text-tertiary)',
                                    fontSize: '14px',
                                    cursor: 'pointer',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '4px',
                                    transition: 'color 0.15s ease',
                                }}
                                title={
                                    dockState.split ? 'Close split view' : 'Split view vertically'
                                }
                            >
                                <span style={{ fontFamily: 'monospace', fontSize: '16px' }}>
                                    {dockState.split ? '◧' : '◫'}
                                </span>
                            </button>
                        )}
                    </div>
                );

                // Render the review content (diff + feedback)
                const renderReviewContent = (inPanel: boolean) => (
                    <>
                        {/* Diff Section */}
                        <div
                            className="diff-section"
                            style={{
                                background: 'var(--bg-secondary)',
                                borderRadius: inPanel ? '0 0 8px 8px' : '8px',
                                border: '1px solid var(--border)',
                                borderTop: inPanel ? 'none' : undefined,
                                // No `overflow: hidden/auto/scroll` — those make this a
                                // scroll container, which would re-anchor descendant
                                // `position: sticky` rows (hunk + file headers) to this
                                // box instead of the viewport.
                            }}
                        >
                            <div
                                style={{
                                    padding: '12px 16px',
                                    borderBottom: '1px solid var(--border)',
                                    background: 'var(--bg-primary)',
                                    fontSize: '13px',
                                    fontWeight: 500,
                                    color: 'var(--text-secondary)',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '8px',
                                }}
                            >
                                <span style={{ color: 'var(--accent)' }}>◈</span>
                                Changes
                                {metadata && !metadata.repo_path && (
                                    <div
                                        style={{
                                            marginLeft: '12px',
                                            display: 'inline-flex',
                                            alignItems: 'center',
                                            gap: '6px',
                                            color: 'var(--warning)',
                                            fontSize: '12px',
                                            fontWeight: 400,
                                        }}
                                    >
                                        <span style={{ fontSize: '14px' }}>⚠️</span>
                                        Repo not found locally. LSP disabled.
                                    </div>
                                )}
                                {lsp.available === false && (
                                    <div
                                        style={{
                                            marginLeft: '12px',
                                            display: 'inline-flex',
                                            alignItems: 'center',
                                            gap: '6px',
                                            color: 'var(--warning)',
                                            fontSize: '12px',
                                            fontWeight: 400,
                                        }}
                                        title="diff-lsp binary not found on server path"
                                    >
                                        <span style={{ fontSize: '14px' }}>⚠️</span>
                                        LSP not active
                                    </div>
                                )}
                            </div>
                            <div
                                style={{
                                    padding: '16px',
                                    fontFamily: 'var(--font-mono)',
                                    fontSize: '13px',
                                    // No overflow:auto here — it would create a scroll
                                    // container and break sticky positioning of hunk
                                    // headers. Long lines are handled by per-line
                                    // overflow handling instead.
                                }}
                            >
                                {renderDiff()}
                            </div>
                        </div>

                        {/* Review Feedback Section (collapsed by default) */}
                        <div
                            style={{
                                background: 'var(--bg-secondary)',
                                borderRadius: '8px',
                                border: '1px solid var(--border)',
                                overflow: 'hidden',
                                marginTop: '16px',
                            }}
                        >
                            <button
                                type="button"
                                onClick={() => setFeedbackCollapsed(c => !c)}
                                aria-expanded={!feedbackCollapsed}
                                style={{
                                    width: '100%',
                                    background: 'var(--bg-primary)',
                                    borderTop: 'none',
                                    borderLeft: 'none',
                                    borderRight: 'none',
                                    borderBottom: feedbackCollapsed
                                        ? 'none'
                                        : '1px solid var(--border)',
                                    padding: '12px 16px',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '8px',
                                    cursor: 'pointer',
                                    color: 'var(--text-secondary)',
                                    fontSize: '13px',
                                    fontWeight: 500,
                                    textAlign: 'left',
                                }}
                            >
                                <span
                                    style={{
                                        display: 'inline-block',
                                        transform: feedbackCollapsed
                                            ? 'rotate(-90deg)'
                                            : 'rotate(0deg)',
                                        transition: 'transform 0.15s ease',
                                        fontSize: '10px',
                                    }}
                                >
                                    ▼
                                </span>
                                <span style={{ color: 'var(--accent)' }}>✎</span>
                                Review Feedback Draft
                                {feedbackBody.trim().length > 0 && (
                                    <span
                                        style={{
                                            fontSize: '11px',
                                            color: 'var(--accent)',
                                            background: 'var(--bg-info-dim)',
                                            border: '1px solid var(--border-info-dim)',
                                            borderRadius: '10px',
                                            padding: '1px 8px',
                                        }}
                                    >
                                        {feedbackBody.trim().length} chars
                                    </span>
                                )}
                                <span
                                    style={{
                                        marginLeft: 'auto',
                                        fontSize: '11px',
                                        color: 'var(--text-tertiary)',
                                        fontWeight: 400,
                                    }}
                                >
                                    pre-fills the Submit Review body
                                </span>
                            </button>
                            {!feedbackCollapsed && (
                                <div style={{ padding: '16px' }}>
                                    <textarea
                                        placeholder="Write your overall review feedback here... This will be pre-filled in the Submit Review body."
                                        value={feedbackBody}
                                        onChange={e => setFeedbackBody(e.target.value)}
                                        disabled={isSavingFeedback}
                                        style={{
                                            width: '100%',
                                            minHeight: '120px',
                                            padding: '10px',
                                            background: 'var(--bg-primary)',
                                            border: '1px solid var(--border)',
                                            color: 'var(--text-primary)',
                                            borderRadius: '6px',
                                            fontFamily: 'inherit',
                                            fontSize: '13px',
                                            resize: 'vertical',
                                            boxSizing: 'border-box',
                                        }}
                                    />
                                    <div
                                        style={{
                                            display: 'flex',
                                            gap: '10px',
                                            justifyContent: 'flex-end',
                                            marginTop: '10px',
                                        }}
                                    >
                                        <Button
                                            onClick={handleSaveFeedback}
                                            size="sm"
                                            loading={isSavingFeedback}
                                            disabled={isSavingFeedback}
                                        >
                                            Save Draft
                                        </Button>
                                    </div>
                                </div>
                            )}
                        </div>
                    </>
                );

                // Render a panel's content area
                const renderPanelContent = (_panelId: PanelId, panel: PanelState) => (
                    <>
                        {panel.activeTab === 'review' && renderReviewContent(true)}
                        {panel.dockedTabs.map(tab => (
                            <div
                                key={tab.id}
                                style={{
                                    display: panel.activeTab === tab.id ? 'block' : 'none',
                                    background: 'var(--bg-secondary)',
                                    borderRadius: '0 0 8px 8px',
                                    border: '1px solid var(--border)',
                                    borderTop: 'none',
                                    overflow: 'hidden',
                                    height: '70vh',
                                }}
                            >
                                {tab.tabType === 'diff' ? (
                                    <div
                                        style={{
                                            height: '100%',
                                            overflowY: 'auto',
                                            padding: '16px',
                                            fontFamily: 'var(--font-mono)',
                                            fontSize: '13px',
                                        }}
                                    >
                                        {renderDiff(tab.filePath)}
                                    </div>
                                ) : (
                                    <CodeViewerModal
                                        isOpen={true}
                                        onClose={() => handleCloseDockedTab(tab.id)}
                                        filePath={tab.filePath}
                                        repoPath={metadata?.repo_path || ''}
                                        initialLine={tab.line}
                                        theme={theme}
                                        docked={true}
                                        onUndock={() => handleUndockTab(tab.id)}
                                    />
                                )}
                            </div>
                        ))}
                    </>
                );

                // No tabs docked — just show review content directly
                if (!hasTabs) {
                    return renderReviewContent(false);
                }

                // Tabs docked, not split — single panel
                if (!dockState.split) {
                    return (
                        <div>
                            {renderTabBar('left', dockState.left, true)}
                            {renderPanelContent('left', dockState.left)}
                        </div>
                    );
                }

                // Split view — two panels side by side
                return (
                    <div style={{ display: 'flex', gap: '8px', alignItems: 'flex-start' }}>
                        <div style={{ flex: 1, minWidth: 0, position: 'sticky', top: '70px' }}>
                            {renderTabBar('left', dockState.left, true)}
                            {renderPanelContent('left', dockState.left)}
                        </div>
                        <div style={{ flex: 1, minWidth: 0, position: 'sticky', top: '70px' }}>
                            {renderTabBar('right', dockState.right, false)}
                            {renderPanelContent('right', dockState.right)}
                        </div>
                    </div>
                );
            })()}

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
                        disabled={isAddingComment}
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
                        disabled={isAddingComment}
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
                        disabled={isAddingComment}
                    />
                    <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                        <Button
                            onClick={resetCommentForm}
                            variant="secondary"
                            disabled={isAddingComment}
                        >
                            Cancel
                        </Button>
                        <Button onClick={handleAddComment} loading={isAddingComment}>
                            Add
                        </Button>
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
                    <div style={{ display: 'flex', gap: '8px' }}>
                        {[
                            { value: 'COMMENT', label: 'Comment' },
                            { value: 'APPROVE', label: 'Approve' },
                            { value: 'REQUEST_CHANGES', label: 'Request Changes' },
                        ].map(option => {
                            const selected = reviewEvent === option.value;
                            let bgColor = 'var(--bg-primary)';
                            let borderColor = 'var(--border)';
                            let textColor = 'var(--text-primary)';

                            if (selected) {
                                if (option.value === 'APPROVE') {
                                    bgColor = colors.success;
                                    borderColor = colors.success;
                                    textColor = 'white';
                                } else if (option.value === 'REQUEST_CHANGES') {
                                    bgColor = colors.danger;
                                    borderColor = colors.danger;
                                    textColor = 'white';
                                } else {
                                    bgColor = 'var(--accent)';
                                    borderColor = 'var(--accent)';
                                    textColor = 'white';
                                }
                            }

                            return (
                                <button
                                    key={option.value}
                                    type="button"
                                    onClick={() => setReviewEvent(option.value)}
                                    disabled={isSubmittingReview}
                                    style={{
                                        flex: 1,
                                        padding: '8px',
                                        background: bgColor,
                                        border: `1px solid ${borderColor}`,
                                        color: textColor,
                                        borderRadius: '4px',
                                        cursor: isSubmittingReview ? 'default' : 'pointer',
                                        fontFamily: 'inherit',
                                        fontSize: '14px',
                                        fontWeight: 500,
                                    }}
                                >
                                    {option.label}
                                </button>
                            );
                        })}
                    </div>
                    <TextArea
                        placeholder="Review Body (Optional)"
                        value={reviewBody}
                        onChange={e => setReviewBody(e.target.value)}
                        rows={5}
                        disabled={isSubmittingReview}
                    />
                    <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                        <Button
                            onClick={() => setSubmitting(false)}
                            variant="secondary"
                            disabled={isSubmittingReview}
                        >
                            Cancel
                        </Button>
                        <Button
                            onClick={handleSubmitReview}
                            style={{ background: 'var(--success)' }}
                            loading={isSubmittingReview}
                        >
                            Submit
                        </Button>
                    </div>
                </div>
            </Modal>
            {showPlugins && (
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
                    onClick={() => setShowPlugins(false)}
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
                            <h2 style={{ fontSize: '18px', margin: 0, color: 'var(--accent)' }}>
                                Plugin Analysis
                            </h2>
                            <div style={{ display: 'flex', gap: '8px' }}>
                                <Button onClick={loadPluginOutputs} variant="secondary" size="sm">
                                    Refresh
                                </Button>
                                <Button
                                    onClick={() => setShowPlugins(false)}
                                    variant="secondary"
                                    size="sm"
                                >
                                    Close (Esc)
                                </Button>
                            </div>
                        </div>
                        <div style={{ flex: 1, overflowY: 'auto', padding: '24px' }}>
                            {Object.keys(pluginOutputs).length === 0 ? (
                                <div
                                    style={{
                                        color: 'var(--text-secondary)',
                                        fontStyle: 'italic',
                                        padding: '20px',
                                        textAlign: 'center',
                                    }}
                                >
                                    No plugin output found.
                                </div>
                            ) : (
                                <div
                                    style={{
                                        display: 'flex',
                                        flexDirection: 'column',
                                        gap: '20px',
                                    }}
                                >
                                    {Object.entries(pluginOutputs).map(([name, data]) => (
                                        <div
                                            key={name}
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
                                                <span style={{ fontWeight: 600, fontSize: '14px' }}>
                                                    {name}
                                                </span>
                                                <span
                                                    style={{
                                                        fontSize: '11px',
                                                        padding: '2px 10px',
                                                        borderRadius: '12px',
                                                        fontWeight: 600,
                                                        background:
                                                            data.status === 'success'
                                                                ? colors.bgSuccessDim
                                                                : data.status === 'pending'
                                                                  ? colors.bgWarningDim
                                                                  : colors.bgDangerDim,
                                                        color:
                                                            data.status === 'success'
                                                                ? colors.textSuccess
                                                                : data.status === 'pending'
                                                                  ? colors.textWarning
                                                                  : colors.textDanger,
                                                        border: `1px solid ${data.status === 'success' ? colors.borderSuccessDim : data.status === 'pending' ? colors.borderWarningDim : colors.borderDangerDim}`,
                                                    }}
                                                >
                                                    {data.status.toUpperCase()}
                                                </span>
                                            </div>
                                            <div
                                                className="plugin-output markdown-content"
                                                style={{
                                                    padding: '15px',
                                                    fontSize: '14px',
                                                    lineHeight: '1.6',
                                                    color: 'var(--text-primary)',
                                                    background: 'var(--bg-primary)',
                                                }}
                                            >
                                                {data.result ? (
                                                    <Markdown>{data.result}</Markdown>
                                                ) : (
                                                    <span
                                                        style={{
                                                            color: 'var(--text-secondary)',
                                                            fontStyle: 'italic',
                                                        }}
                                                    >
                                                        No output produced.
                                                    </span>
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
                    onClick={() => setActiveOutdatedFile(null)}
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
                                <h2
                                    style={{ fontSize: '18px', margin: 0, color: 'var(--warning)' }}
                                >
                                    Outdated Comments
                                </h2>
                                <span
                                    style={{
                                        fontSize: '12px',
                                        color: 'var(--text-secondary)',
                                        marginTop: '4px',
                                    }}
                                >
                                    {activeOutdatedFile}
                                </span>
                            </div>
                            <Button
                                onClick={() => setActiveOutdatedFile(null)}
                                variant="secondary"
                                size="sm"
                            >
                                Close (Esc)
                            </Button>
                        </div>
                        <div style={{ flex: 1, overflowY: 'auto', padding: '24px' }}>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '20px' }}>
                                {outdatedComments
                                    .filter(c => c.path === activeOutdatedFile)
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
                                                            style={customDiffTheme}
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
            )}

            {/* Code Viewer Modals */}
            {codeViewers.map(viewer => (
                <CodeViewerModal
                    key={viewer.id}
                    isOpen={true}
                    onClose={() =>
                        setDockState(prev => ({
                            ...prev,
                            codeViewers: prev.codeViewers.filter(v => v.id !== viewer.id),
                        }))
                    }
                    filePath={viewer.filePath}
                    repoPath={metadata?.repo_path || ''}
                    initialLine={viewer.line}
                    theme={theme}
                    initialPosition={viewer.position}
                    onDock={() => handleDockViewer(viewer.id)}
                />
            ))}
        </div>
    );
}
