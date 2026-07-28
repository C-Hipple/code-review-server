import { Fragment, useMemo } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { colors } from '../../design';
import { type ParsedLine, sortParsedLinesTestsLast } from '../../diff_utils';
import { getClickColumn } from '../../utils/dom';
import type { LspData } from '../../hooks/useLsp';
import LspPopover from '../LspPopover';
import CommentIndicator from './CommentIndicator';
import CommentThread from './CommentThread';
import InlineCommentForm from './InlineCommentForm';
import {
    type Comment,
    MAX_HIGHLIGHT_LINES_PER_FILE,
    getFileStatusInfo,
    getLanguageFromFilename,
    slugify,
} from './types';
import type { DiffTheme } from './diff_theme';

export interface DiffViewProps {
    parsedLines: ParsedLine[];
    // When set, only render lines belonging to this file (used by docked diff tabs).
    filterFile?: string;
    comments: Comment[];
    outdatedComments: Comment[];
    collapsedFiles: Set<string>;
    // Root comment ids whose full thread should render inline; threads not in
    // this set are collapsed behind a hover/click indicator.
    visibleThreadIds: Set<string>;
    activeLineIndex: number | null;
    activeLspIndex: number | null;
    replyToId: number | null;
    editingCommentId: number | null;
    commentBody: string;
    editingCommentBody: string;
    isAddingComment: boolean;
    isMobile: boolean;
    wrapLines: boolean;
    diffTheme: DiffTheme;
    lspData: LspData | null;
    onToggleThreadsVisible: (rootIds: string[]) => void;
    onCommentClick: (idx: number, file: string, pos: number) => void;
    onCodeClick: (
        idx: number,
        file: string,
        pos: number,
        originalLineIndex: number,
        col: number
    ) => void;
    onThreadClick: (thread: Comment[], file: string, pos: number, lineIdx: number) => void;
    onDeleteComment: (id: number) => void;
    onToggleFileCollapse: (file: string) => void;
    onShowOutdated: (file: string) => void;
    onDockDiffFile: (file: string) => void;
    onExpandHunk: (hunkLineIndex: number, file: string, direction: 'before' | 'after') => void;
    onOpenCodeViewer: (filePath: string, line: number, position: { x: number; y: number }) => void;
    // Close the LSP popover shown inline in the comment form (clears data only).
    onClearLspData: () => void;
    // Close the floating LSP popover (clears data and the active line).
    onCloseLsp: () => void;
    onCancelInline: () => void;
    onChangeCommentBody: (body: string) => void;
    onChangeEditingCommentBody: (body: string) => void;
    onSubmitInline: () => void;
}

// The main diff renderer: file headers, hunk rows, syntax-highlighted code
// lines, inline comment threads, and the inline comment editor.
export default function DiffView({
    parsedLines,
    filterFile,
    comments,
    outdatedComments,
    collapsedFiles,
    visibleThreadIds,
    activeLineIndex,
    activeLspIndex,
    replyToId,
    editingCommentId,
    commentBody,
    editingCommentBody,
    isAddingComment,
    isMobile,
    wrapLines,
    diffTheme,
    lspData,
    onToggleThreadsVisible,
    onCommentClick,
    onCodeClick,
    onThreadClick,
    onDeleteComment,
    onToggleFileCollapse,
    onShowOutdated,
    onDockDiffFile,
    onExpandHunk,
    onOpenCodeViewer,
    onClearLspData,
    onCloseLsp,
    onCancelInline,
    onChangeCommentBody,
    onChangeEditingCommentBody,
    onSubmitInline,
}: DiffViewProps) {
    const lines = useMemo(() => sortParsedLinesTestsLast(parsedLines), [parsedLines]);

    // Pre-compute highlighted lines for each file section
    const highlightedLines = useMemo(() => {
        const result: Map<number, React.ReactNode> = new Map();

        // Group lines by file and collect code for bulk highlighting
        let currentFile: string | null = null;
        let fileLines: { idx: number; code: string; prefix: string }[] = [];

        const processFileSection = (file: string, linesData: typeof fileLines) => {
            if (linesData.length === 0) return;

            // Each highlighted line spins up its own Prism instance, so very large
            // file sections are left unhighlighted to keep big diffs responsive.
            // The renderer falls back to plain text when no entry exists for a line.
            if (linesData.length > MAX_HIGHLIGHT_LINES_PER_FILE) return;

            const language = getLanguageFromFilename(file);

            // We'll use SyntaxHighlighter's output directly for each line
            linesData.forEach(lineData => {
                const lineCode = lineData.code || ' '; // Use space for empty lines
                result.set(
                    lineData.idx,
                    <SyntaxHighlighter
                        language={language}
                        style={diffTheme}
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
    }, [lines, diffTheme]);

    if (lines.length === 0) return null;

    // Pre-compute per-file add/del counts for display in file headers.
    const fileCounts = new Map<string, { add: number; del: number }>();
    let fcFile: string | null = null;
    for (const p of lines) {
        if (p.lineType === 'file-header') {
            fcFile = p.file;
            if (fcFile && !fileCounts.has(fcFile)) fileCounts.set(fcFile, { add: 0, del: 0 });
        } else if (fcFile && fileCounts.has(fcFile)) {
            const fc = fileCounts.get(fcFile)!;
            if (p.lineType === 'addition') fc.add++;
            else if (p.lineType === 'deletion') fc.del++;
        }
    }

    // Comments attached to a specific parsed line (file header or code line).
    const commentsForLine = (item: ParsedLine): Comment[] =>
        item.file
            ? comments.filter(
                  c =>
                      c.path === item.file &&
                      (c.position === item.pos?.toString() || (c.position === '' && item.pos === 0))
              )
            : [];

    const rootCommentsForLine = (item: ParsedLine): Comment[] => {
        const lineComments = commentsForLine(item);
        return lineComments.filter(c => !c.in_reply_to);
    };

    const threadForRoot = (rc: Comment, lineComments: Comment[]): Comment[] => [
        rc,
        ...lineComments.filter(c => c.in_reply_to === parseInt(rc.id, 10)),
    ];

    const threadsForLine = (item: ParsedLine, idx: number, stickyOnMobile: boolean) => {
        const lineComments = commentsForLine(item);
        const rootComments = rootCommentsForLine(item);
        return rootComments
            .filter(rc => visibleThreadIds.has(rc.id))
            .map(rc => {
                const thread = threadForRoot(rc, lineComments);
                const isReplyingToThread =
                    (replyToId !== null && thread.some(c => parseInt(c.id, 10) === replyToId)) ||
                    (editingCommentId !== null &&
                        thread.some(c => parseInt(c.id, 10) === editingCommentId));
                return (
                    <CommentThread
                        key={rc.id}
                        thread={thread}
                        isActive={isReplyingToThread}
                        stickyOnMobile={stickyOnMobile}
                        onClick={() => {
                            if (item.file && item.pos !== null) {
                                onThreadClick(thread, item.file, item.pos, idx);
                            }
                        }}
                        onDeleteComment={onDeleteComment}
                    />
                );
            });
    };

    // Indicator badge shown next to a commented line/file; toggles all of that
    // line's threads open/closed together and previews them all on hover.
    const commentIndicatorForLine = (item: ParsedLine) => {
        const rootComments = rootCommentsForLine(item);
        if (rootComments.length === 0) return null;
        const lineComments = commentsForLine(item);
        const rootIds = rootComments.map(rc => rc.id);
        const allVisible = rootIds.every(id => visibleThreadIds.has(id));
        const allThreadComments = rootComments.flatMap(rc => threadForRoot(rc, lineComments));
        return (
            <span onClick={e => e.stopPropagation()}>
                <CommentIndicator
                    thread={allThreadComments}
                    visible={allVisible}
                    onToggle={() => onToggleThreadsVisible(rootIds)}
                />
            </span>
        );
    };

    let currentFile: string | null = null;

    const rows = lines.map((item, idx) => {
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
            const isCollapsed = item.file ? collapsedFiles.has(item.file) : false;
            const fileOutdatedComments = item.file
                ? outdatedComments.filter(c => c.path === item.file)
                : [];
            const hasOutdated = fileOutdatedComments.length > 0;

            return (
                // Fragment, not a wrapper div (same reason as hunk rows below):
                // the header's sticky containing block must be the whole file
                // section, not a box sized to the header itself, or it would
                // have no room to stay pinned while the file's lines scroll by.
                <Fragment key={idx}>
                    <div
                        id={item.file ? `file-${slugify(item.file)}` : undefined}
                        style={{
                            display: 'flex',
                            alignItems: 'center',
                            gap: '10px',
                            padding: '10px 12px',
                            background:
                                'linear-gradient(135deg, var(--bg-tertiary) 0%, var(--bg-secondary) 100%)',
                            // Every file header keeps the same border and no top
                            // margin so all of them render at an identical height:
                            // Review.tsx measures one row to offset the sticky hunk
                            // headers below, and a margin above a pinned row would
                            // open a seam that scrolling content shows through.
                            borderTop: '2px solid var(--border)',
                            borderBottom: '1px solid var(--border)',
                            cursor: 'pointer',
                            borderLeft: isInlineActive
                                ? '3px solid var(--accent)'
                                : '3px solid transparent',
                            // `position` is left to .diff-file-row (sticky on
                            // desktop, static on mobile); an inline value here
                            // would override the class.
                        }}
                        onClick={() =>
                            item.file &&
                            item.pos !== null &&
                            onCommentClick(idx, item.file, item.pos)
                        }
                        className="hover-line diff-file-row"
                        title={`Add comment to ${item.file}`}
                    >
                        <button
                            onClick={e => {
                                e.stopPropagation();
                                if (item.file) onToggleFileCollapse(item.file);
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
                                // Keep the header exactly one line tall: a wrapped
                                // path would make this file's row taller than the
                                // one measured for the sticky hunk offset.
                                minWidth: 0,
                                whiteSpace: 'nowrap',
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                            }}
                            title={item.file || item.text}
                        >
                            {item.fileStatus === 'renamed' && item.origName
                                ? `${item.origName} → ${item.text}`
                                : item.text}
                        </span>
                        {(() => {
                            const fc = item.file ? fileCounts.get(item.file) : null;
                            if (!fc || (fc.add === 0 && fc.del === 0)) return null;
                            return (
                                <span
                                    style={{
                                        display: 'inline-flex',
                                        gap: '4px',
                                        marginLeft: '4px',
                                    }}
                                >
                                    {fc.add > 0 && (
                                        <span
                                            style={{
                                                color: 'var(--text-success)',
                                                fontSize: '12px',
                                            }}
                                        >
                                            +{fc.add}
                                        </span>
                                    )}
                                    {fc.del > 0 && (
                                        <span
                                            style={{
                                                color: 'var(--text-danger)',
                                                fontSize: '12px',
                                            }}
                                        >
                                            −{fc.del}
                                        </span>
                                    )}
                                </span>
                            );
                        })()}
                        {commentIndicatorForLine(item)}
                        {hasOutdated && (
                            <button
                                onClick={e => {
                                    e.stopPropagation();
                                    if (item.file) onShowOutdated(item.file);
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
                                <span>⚠️</span> Outdated Comments ({fileOutdatedComments.length})
                            </button>
                        )}
                        {item.file && (
                            <button
                                onClick={e => {
                                    e.stopPropagation();
                                    onDockDiffFile(item.file!);
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
                    {threadsForLine(item, idx, false)}
                    {isInlineActive && (
                        <InlineCommentForm
                            file={item.file}
                            pos={item.pos}
                            editingCommentId={editingCommentId}
                            replyToId={replyToId}
                            commentBody={commentBody}
                            editingCommentBody={editingCommentBody}
                            isAddingComment={isAddingComment}
                            onChangeCommentBody={onChangeCommentBody}
                            onChangeEditingCommentBody={onChangeEditingCommentBody}
                            onCancel={onCancelInline}
                            onSubmit={onSubmitInline}
                        >
                            {lspData && (
                                <LspPopover
                                    hover={lspData.hover}
                                    refs={lspData.refs}
                                    definitions={lspData.definitions}
                                    typeDefinitions={lspData.typeDefinitions}
                                    variant="inline"
                                    onRefClick={(r, e) => {
                                        const filePath = r.uri.replace('file://', '');
                                        onOpenCodeViewer(filePath, r.range.start.line + 1, {
                                            x: e.clientX + 20,
                                            y: e.clientY - 50,
                                        });
                                    }}
                                    onClose={onClearLspData}
                                />
                            )}
                        </InlineCommentForm>
                    )}
                </Fragment>
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

        // On mobile, unwrapped lines scroll horizontally as a unit (see the
        // max-content wrapper below). The code span must size to its content
        // and never shrink so the row grows wider than the viewport.
        const mobileScroll = isMobile && !wrapLines;
        let lineStyle: React.CSSProperties = {
            flex: mobileScroll ? '1 0 auto' : 1,
            minWidth: mobileScroll ? 'auto' : 0,
            padding: '0 8px',
            whiteSpace: wrapLines ? 'pre-wrap' : 'pre',
            overflowWrap: wrapLines ? 'anywhere' : 'normal',
            display: 'flex',
            alignItems: wrapLines ? 'flex-start' : 'center',
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
            // Let .diff-hunk-row own `position` (sticky on desktop, static on
            // mobile). An inline `position: relative` here would override the
            // class's sticky while its `top` offset still applied, painting
            // the header below its slot and overlapping the rows beneath it.
            containerStyle = {
                ...containerStyle,
                background: colors.diffHunkBg,
                position: undefined,
            };
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

        // Hunk headers render unwrapped (Fragment adds no DOM node): a
        // per-row wrapper div would be their sticky containing block, sized
        // exactly to the row, leaving the header no room to pin under the
        // toolbar while its hunk scrolls.
        const RowWrapper = isHunkHeader ? Fragment : 'div';

        return (
            <RowWrapper key={idx}>
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
                        // Fixed-width comment gutter, to the LEFT of the line
                        // numbers. Always rendered (empty when the line has no
                        // comment) so a collapsed-comment badge never shifts the
                        // line numbers or code. A wider multi-comment badge is
                        // right-aligned here and overflows left into the diff
                        // padding, never over the line numbers to its right.
                        <span
                            style={{
                                width: '22px',
                                minWidth: '22px',
                                display: 'flex',
                                alignItems: 'center',
                                justifyContent: 'flex-end',
                                userSelect: 'none',
                            }}
                        >
                            {commentIndicatorForLine(item)}
                        </span>
                    )}
                    {isCodeLine && !isHunkHeader && !isMobile && (
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
                                    onCommentClick(idx, item.file, item.pos);
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
                                    onExpandHunk(item.originalLineIndex, item.file!, 'before');
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
                                    onExpandHunk(item.originalLineIndex, item.file!, 'after');
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
                                onCodeClick(idx, item.file, item.pos, item.originalLineIndex, col);
                            }
                        }}
                    >
                        {lineContent}
                    </span>
                    {isLspActive && lspData && (
                        <LspPopover
                            hover={lspData.hover}
                            refs={lspData.refs}
                            definitions={lspData.definitions}
                            typeDefinitions={lspData.typeDefinitions}
                            variant="floating"
                            onRefClick={(r, e) => {
                                const filePath = r.uri.replace('file://', '');
                                onOpenCodeViewer(filePath, r.range.start.line + 1, {
                                    x: e.clientX + 20,
                                    y: e.clientY - 50,
                                });
                            }}
                            onClose={onCloseLsp}
                        />
                    )}
                </div>
                {threadsForLine(item, idx, isMobile)}
                {isInlineActive && (
                    <InlineCommentForm
                        file={item.file}
                        pos={item.pos}
                        editingCommentId={editingCommentId}
                        replyToId={replyToId}
                        commentBody={commentBody}
                        editingCommentBody={editingCommentBody}
                        isAddingComment={isAddingComment}
                        stickyOnMobile={isMobile}
                        onChangeCommentBody={onChangeCommentBody}
                        onChangeEditingCommentBody={onChangeEditingCommentBody}
                        onCancel={onCancelInline}
                        onSubmit={onSubmitInline}
                    />
                )}
            </RowWrapper>
        );
    });

    // Group each file's rows under a section wrapper. That wrapper is the
    // sticky containing block for the file's header and hunk rows, so they pin
    // below the toolbar while the file is on screen and scroll away with it —
    // rather than a header from a file you already passed lingering up there.
    const sections: React.ReactNode[] = [];
    let sectionRows: React.ReactNode[] = [];
    const flushSection = () => {
        if (sectionRows.length === 0) return;
        sections.push(<div key={`section-${sections.length}`}>{sectionRows}</div>);
        sectionRows = [];
    };
    lines.forEach((item, idx) => {
        if (item.lineType === 'file-header') flushSection();
        const row = rows[idx];
        if (row) sectionRows.push(row);
    });
    flushSection();

    // On phones, let the whole diff slide horizontally so long lines can be
    // read in full. `max-content` makes every row as wide as the longest
    // line (so add/del backgrounds extend across the full scroll width),
    // while `minWidth: 100%` keeps short diffs filling the viewport. Inline
    // comments are pinned back to the left edge (see their sticky styles) so
    // they stay readable regardless of the horizontal scroll position.
    if (isMobile) {
        return (
            <div style={{ overflowX: 'auto', WebkitOverflowScrolling: 'touch' }}>
                <div
                    style={{
                        display: 'flex',
                        flexDirection: 'column',
                        width: 'max-content',
                        minWidth: '100%',
                    }}
                >
                    {sections}
                </div>
            </div>
        );
    }

    return <>{sections}</>;
}
