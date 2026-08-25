import { useState, useEffect, useMemo, useCallback, useRef, useLayoutEffect } from 'react';
import { rpcCall, getHunkContext } from '../api';
import { Button, Toast, Theme, StatusVariant } from '../design';
import { useLsp } from '../hooks/useLsp';
import { useIsMobile } from '../hooks/useMediaQuery';
import { annotationCommentBody, collectPRAnnotations, indexAnnotations } from '../annotation_utils';
import { parseDiff } from '../diff_utils';
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
import CodeViewerModal from './CodeViewerModal';
import AddCommentModal from './review/AddCommentModal';
import DiffView from './review/DiffView';
import FileIndex from './review/FileIndex';
import MobileReviewBar from './review/MobileReviewBar';
import OutdatedCommentsPanel from './review/OutdatedCommentsPanel';
import PluginsPanel from './review/PluginsPanel';
import PRHeader from './review/PRHeader';
import PRDiscussion from './review/PRDiscussion';
import ReviewSubmitModal from './review/ReviewSubmitModal';
import { buildDiffTheme } from './review/diff_theme';
import {
    slugify,
    type Comment,
    type PluginResult,
    type PRAnnotation,
    type PRMetadata,
    type PRResponse,
} from './review/types';

interface ReviewProps {
    owner: string;
    repo: string;
    number: number;
    theme: Theme;
    onThemeChange: (theme: Theme) => void;
}

export default function Review({
    owner,
    repo,
    number,
    theme,
    onThemeChange: _onThemeChange,
}: ReviewProps) {
    const [content, setContent] = useState<string>('');
    const [diff, setDiff] = useState<string>('');
    // Indices (into diff.split('\n')) of context lines fetched on demand via
    // hunk expansion. These lines are NOT part of the PR's canonical diff, so
    // they must not consume a GitHub comment "position" — otherwise every line
    // below an expansion shifts and comments created while expanded are stored
    // at a position that no longer exists once the canonical diff is restored.
    const [expandedLineIndices, setExpandedLineIndices] = useState<Set<number>>(new Set());
    const [comments, setComments] = useState<Comment[]>([]);
    const [outdatedComments, setOutdatedComments] = useState<Comment[]>([]);
    const [reviews, setReviews] = useState<PRResponse['reviews']>([]);
    const [metadata, setMetadata] = useState<PRMetadata | null>(null);
    const [pluginOutputs, setPluginOutputs] = useState<Record<string, PluginResult>>({});
    const [executingPlugins, setExecutingPlugins] = useState<Set<string>>(new Set());
    const [loading, setLoading] = useState(false);
    // Transient toast notification; keyed by id so a new message restarts the timer.
    const [toast, setToast] = useState<{
        id: number;
        message: string;
        variant: StatusVariant;
    } | null>(null);
    const showToast = (message: string, variant: StatusVariant = 'info') =>
        setToast({ id: Date.now(), message, variant });

    // UI State
    const [showCommentModal, setShowCommentModal] = useState(false); // For general comments
    const [activeLineIndex, setActiveLineIndex] = useState<number | null>(null); // For inline comments
    const [activeLspIndex, setActiveLspIndex] = useState<number | null>(null); // For LSP display
    const [showPlugins, setShowPlugins] = useState(false);
    const [collapsedFiles, setCollapsedFiles] = useState<Set<string>>(new Set());
    // Comment threads are hidden by default; a thread's root comment id must be
    // in this set for its full interactive thread to render inline.
    const [visibleThreadIds, setVisibleThreadIds] = useState<Set<string>>(new Set());
    const toggleThreadsVisible = (rootIds: string[]) => {
        setVisibleThreadIds(prev => {
            const next = new Set(prev);
            const allVisible = rootIds.every(id => next.has(id));
            rootIds.forEach(id => {
                if (allVisible) next.delete(id);
                else next.add(id);
            });
            return next;
        });
    };
    // Plugin annotations are hidden by default too; a bucket's key (see
    // annotation_utils) must be in this set for its card to render inline.
    const [visibleAnnotationKeys, setVisibleAnnotationKeys] = useState<Set<string>>(new Set());
    const toggleAnnotations = (key: string) => {
        setVisibleAnnotationKeys(prev => {
            const next = new Set(prev);
            if (next.has(key)) next.delete(key);
            else next.add(key);
            return next;
        });
    };
    const [activeOutdatedFile, setActiveOutdatedFile] = useState<string | null>(null);
    // Description starts expanded — it's the context for everything below it.
    const [descCollapsed, setDescCollapsed] = useState(false);
    // On phones, wrap long diff lines by default so they read without
    // horizontal scrolling; keep `pre` (scroll) as the default on desktop where
    // column alignment matters more.
    const isMobile = useIsMobile();
    const [wrapLines, setWrapLines] = useState<boolean>(() => isMobile);

    // Measure the sticky toolbar and a file header row and publish their
    // heights as CSS variables, so the file header pins directly below the
    // toolbar and hunk headers pin directly below the file header —
    // regardless of font size, button padding, or wrap. Every file header
    // renders at the same height (see DiffView), so one row is enough.
    const toolbarRef = useRef<HTMLDivElement | null>(null);
    useLayoutEffect(() => {
        const el = toolbarRef.current;
        if (!el) return;
        // Queried rather than ref'd: DiffView renders one of these per file and
        // any of them gives the same height.
        const fileRow = document.querySelector<HTMLElement>('.diff-file-row');
        const sync = () => {
            // Toolbar `top` offset (10px) + rendered height. No extra gap — any
            // pixel between the toolbar's bottom and the row pinned beneath it
            // would expose scrolling content through the seam.
            const top = 10 + el.offsetHeight;
            const root = document.documentElement;
            root.style.setProperty('--review-sticky-top', `${top}px`);
            // Before any file header exists, hunk headers pin under the toolbar.
            root.style.setProperty(
                '--review-hunk-sticky-top',
                `${top + (fileRow?.offsetHeight ?? 0)}px`
            );
        };
        sync();
        const ro = new ResizeObserver(sync);
        ro.observe(el);
        if (fileRow) ro.observe(fileRow);
        window.addEventListener('resize', sync);
        return () => {
            ro.disconnect();
            window.removeEventListener('resize', sync);
        };
        // Re-run once the diff renders (or changes) so the file row is measured.
    }, [diff]);
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
        if (metadata?.title) {
            document.title = `${metadata.title} (#${number})`;
        }
    }, [metadata?.title, number]);

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

    const executePlugin = async (pluginName: string) => {
        setExecutingPlugins((prev: Set<string>) => new Set(prev).add(pluginName));
        try {
            await rpcCall('RPCHandler.RerunPlugins', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                    Plugins: [pluginName],
                },
            ]);
            await loadPluginOutputs();
        } catch (e) {
            console.error('Failed to execute plugin:', e);
        } finally {
            setExecutingPlugins((prev: Set<string>) => {
                const next = new Set(prev);
                next.delete(pluginName);
                return next;
            });
        }
    };

    // Apply a PR response payload to component state.
    const applyPRResponse = (res: PRResponse) => {
        setContent(res.content || '');
        setDiff(res.diff || '');
        setExpandedLineIndices(new Set());
        setComments(res.comments || []);
        setOutdatedComments(res.outdated_comments || []);
        setReviews(res.reviews || []);
        setMetadata(res.metadata || null);
        setFeedbackBody(res.feedback || '');
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
            applyPRResponse(res);
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
            applyPRResponse(res);
            loadPluginOutputs();
            if (res.updated) {
                showToast('Synced — new commits, comments, or reviews pulled in', 'success');
            } else {
                showToast('Synced — already up to date', 'info');
            }
        } catch (e) {
            console.error(e);
            showToast('Sync failed — see console for details', 'danger');
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

            let insertAt: number;
            if (direction === 'before') {
                insertAt = hunkLineIndex + 1;
            } else {
                // Find the end of this hunk: next hunk header, next file, or EOF.
                let endIdx = hunkLineIndex + 1;
                while (endIdx < newLines.length) {
                    const l = newLines[endIdx];
                    if (l.startsWith('@@ ') || l.startsWith('diff --git ')) break;
                    endIdx++;
                }
                insertAt = endIdx;
            }
            newLines.splice(insertAt, 0, ...contextLines);

            // Record the newly inserted lines as expanded context, shifting any
            // previously expanded indices at/after the insertion point. Keeping
            // this in sync with the diff string lets parseDiff exclude these
            // lines from comment-position counting.
            const expandedCount = contextLines.length;
            setExpandedLineIndices(prev => {
                const next = new Set<number>();
                for (const i of prev) {
                    next.add(i >= insertAt ? i + expandedCount : i);
                }
                for (let k = 0; k < expandedCount; k++) {
                    next.add(insertAt + k);
                }
                return next;
            });

            setDiff(newLines.join('\n'));
        } catch (e) {
            console.error('Failed to expand hunk context:', e);
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
            setExpandedLineIndices(new Set());
            const prevIds = new Set(comments.map(c => c.id));
            const newComments = (res.comments || []).filter(c => !prevIds.has(c.id));
            if (newComments.length > 0) {
                setVisibleThreadIds(prev => {
                    const next = new Set(prev);
                    newComments.forEach(c => {
                        next.add(c.in_reply_to ? c.in_reply_to.toString() : c.id);
                    });
                    return next;
                });
            }
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

    // Adopt a plugin annotation as a local comment on the row it annotates, so
    // it goes out with the review under the reviewer's name, attributed to the
    // plugin that raised it.
    const handleAnnotationToComment = async (
        annotation: PRAnnotation,
        file: string,
        pos: number
    ) => {
        setIsAddingComment(true);
        try {
            const res = await rpcCall<PRResponse>('RPCHandler.AddComment', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                    Filename: file,
                    Position: pos,
                    Body: annotationCommentBody(annotation),
                },
            ]);
            // Only the comments changed, so the diff is left as it is — this is a
            // one-click action, and reloading the diff would throw away any hunks
            // the reviewer has expanded around the annotation they just adopted.
            const prevIds = new Set(comments.map(c => c.id));
            const added = (res.comments || []).filter(c => !prevIds.has(c.id));
            if (added.length > 0) {
                setVisibleThreadIds(prev => {
                    const next = new Set(prev);
                    added.forEach(c => next.add(c.id));
                    return next;
                });
            }
            setComments(res.comments || []);
            setOutdatedComments(res.outdated_comments || []);
            setReviews(res.reviews || []);
            showToast(`Added ${annotation.plugin} annotation as a local comment`, 'success');
        } catch (e) {
            console.error(e);
            showToast('Error adding annotation as a comment', 'danger');
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
            setExpandedLineIndices(new Set());
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
            // The reply is the post-submission PR, fetched fresh from GitHub
            // by the server. Applying it directly is the refresh — calling
            // handleSync() here would throw this payload away and pay for the
            // identical forced refetch a second time.
            const res = await rpcCall<PRResponse>('RPCHandler.SubmitReview', [
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
            applyPRResponse(res);
            loadPluginOutputs();
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

    // Parsed diff lines — the shared input for the diff view, the file index,
    // and collapse-all.
    const parsedLines = useMemo(
        () => parseDiff(diff, expandedLineIndices),
        [diff, expandedLineIndices]
    );

    // Plugin annotations, anchored to the rows of the diff they refer to. Taken
    // from the loaded plugin outputs rather than the PR payload's identical
    // `annotations` field so rerunning a plugin refreshes the diff too.
    const annotationIndex = useMemo(
        () => indexAnnotations(collectPRAnnotations(pluginOutputs), parsedLines),
        [pluginOutputs, parsedLines]
    );
    const allAnnotationsVisible =
        annotationIndex.keys.length > 0 &&
        annotationIndex.keys.every(key => visibleAnnotationKeys.has(key));
    const toggleAllAnnotations = () =>
        setVisibleAnnotationKeys(allAnnotationsVisible ? new Set() : new Set(annotationIndex.keys));

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
        const allFiles = new Set(
            parsedLines
                .filter(line => line.lineType === 'file-header' && line.file)
                .map(line => line.file!)
        );
        setCollapsedFiles(allFiles);
    };

    const expandAllFiles = () => {
        setCollapsedFiles(new Set());
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

    // Open a floating code viewer at a file/line (from LSP reference clicks).
    const handleOpenCodeViewer = (
        filePath: string,
        line: number,
        position: { x: number; y: number }
    ) => {
        setDockState(prev => ({
            ...prev,
            codeViewers: [
                ...prev.codeViewers,
                {
                    id: Date.now(),
                    filePath,
                    line,
                    position,
                },
            ],
        }));
    };

    // Custom Prism theme adjusted for diff context
    const customDiffTheme = useMemo(() => buildDiffTheme(theme), [theme]);

    const handleCancelInline = () => {
        setActiveLineIndex(null);
        setReplyToId(null);
        setEditingCommentId(null);
        setEditingCommentBody('');
    };

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

    if (loading && !content) return <div style={{ padding: '20px' }}>Loading PR...</div>;

    const renderDiff = (filterFile?: string) => (
        <DiffView
            parsedLines={parsedLines}
            filterFile={filterFile}
            comments={comments}
            outdatedComments={outdatedComments}
            collapsedFiles={collapsedFiles}
            visibleThreadIds={visibleThreadIds}
            annotations={annotationIndex}
            visibleAnnotationKeys={visibleAnnotationKeys}
            activeLineIndex={activeLineIndex}
            activeLspIndex={activeLspIndex}
            replyToId={replyToId}
            editingCommentId={editingCommentId}
            commentBody={commentBody}
            editingCommentBody={editingCommentBody}
            isAddingComment={isAddingComment}
            isMobile={isMobile}
            wrapLines={wrapLines}
            diffTheme={customDiffTheme}
            lspData={lsp.lspData}
            onToggleThreadsVisible={toggleThreadsVisible}
            onToggleAnnotations={toggleAnnotations}
            onAnnotationToComment={handleAnnotationToComment}
            onCommentClick={handleCommentClick}
            onCodeClick={handleCodeClick}
            onThreadClick={handleThreadClick}
            onDeleteComment={handleDeleteComment}
            onToggleFileCollapse={toggleFileCollapse}
            onShowOutdated={setActiveOutdatedFile}
            onDockDiffFile={handleDockDiffFile}
            onExpandHunk={handleExpandHunk}
            onOpenCodeViewer={handleOpenCodeViewer}
            onClearLspData={() => lsp.clearData()}
            onCloseLsp={() => {
                setActiveLspIndex(null);
                lsp.clearData();
            }}
            onCancelInline={handleCancelInline}
            onChangeCommentBody={setCommentBody}
            onChangeEditingCommentBody={setEditingCommentBody}
            onSubmitInline={editingCommentId !== null ? handleEditComment : handleAddComment}
        />
    );

    return (
        <div className="review-container" style={isMobile ? { paddingBottom: '72px' } : undefined}>
            {/* PR Header Section */}
            {metadata && (
                <PRHeader
                    metadata={metadata}
                    descCollapsed={descCollapsed}
                    onToggleDescCollapsed={() => setDescCollapsed(c => !c)}
                />
            )}

            {/* PR discussion: reviews and their comment threads, chronologically */}
            <PRDiscussion
                reviews={reviews}
                comments={comments}
                outdatedComments={outdatedComments}
                onJumpToFile={scrollToFile}
            />

            {/* Toolbar */}
            <div
                ref={toolbarRef}
                className="toolbar"
                style={{
                    display: 'flex',
                    flexWrap: 'wrap',
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
                    onClick={() => setWrapLines(w => !w)}
                    variant={wrapLines ? 'primary' : 'secondary'}
                    size="sm"
                    disabled={loading}
                    title={wrapLines ? 'Wrapping long lines' : 'Long lines scroll horizontally'}
                >
                    ↩ Wrap
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
                {annotationIndex.count > 0 && (
                    <Button
                        onClick={toggleAllAnnotations}
                        variant={allAnnotationsVisible ? 'primary' : 'secondary'}
                        size="sm"
                        title={
                            allAnnotationsVisible
                                ? 'Collapse every plugin annotation in the diff'
                                : 'Expand every plugin annotation in the diff'
                        }
                    >
                        ⚑ Annotations ({annotationIndex.count})
                    </Button>
                )}

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
            <FileIndex parsed={parsedLines} onSelectFile={scrollToFile} />

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
                        {showSplitBtn && !isMobile && (
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
                                    // Set from the Review Diff Font Size preference; the
                                    // diff's line numbers, +/- gutter and hunk headers
                                    // size themselves in `em` off this base.
                                    fontSize: 'var(--diff-font-size, 13px)',
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
                                            fontSize: 'var(--diff-font-size, 13px)',
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

                // Tabs docked, not split — single panel. Split view is also
                // forced off on phones, where two side-by-side panels are too
                // narrow to read.
                if (!dockState.split || isMobile) {
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

            <AddCommentModal
                isOpen={showCommentModal}
                filename={filename}
                position={position}
                commentBody={commentBody}
                isAddingComment={isAddingComment}
                onChangeFilename={setFilename}
                onChangePosition={setPosition}
                onChangeCommentBody={setCommentBody}
                onClose={resetCommentForm}
                onSubmit={handleAddComment}
            />

            <ReviewSubmitModal
                isOpen={submitting}
                reviewEvent={reviewEvent}
                reviewBody={reviewBody}
                isSubmittingReview={isSubmittingReview}
                onChangeReviewEvent={setReviewEvent}
                onChangeReviewBody={setReviewBody}
                onClose={() => setSubmitting(false)}
                onSubmit={handleSubmitReview}
            />

            {showPlugins && (
                <PluginsPanel
                    pluginOutputs={pluginOutputs}
                    executingPlugins={executingPlugins}
                    onRefresh={loadPluginOutputs}
                    onExecutePlugin={executePlugin}
                    onClose={() => setShowPlugins(false)}
                />
            )}

            {activeOutdatedFile && (
                <OutdatedCommentsPanel
                    file={activeOutdatedFile}
                    outdatedComments={outdatedComments}
                    diffTheme={customDiffTheme}
                    onClose={() => setActiveOutdatedFile(null)}
                />
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

            {isMobile && metadata && (
                <MobileReviewBar
                    loading={loading}
                    onAction={event => {
                        setReviewEvent(event);
                        setReviewBody(feedbackBody);
                        setSubmitting(true);
                    }}
                />
            )}

            {/* Transient notifications (e.g., sync result) */}
            {toast && (
                <Toast
                    key={toast.id}
                    message={toast.message}
                    variant={toast.variant}
                    bottomOffset={isMobile ? 84 : 24}
                    onDismiss={() => setToast(null)}
                />
            )}
        </div>
    );
}
