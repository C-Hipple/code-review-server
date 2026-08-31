import { Fragment, useMemo } from 'react';
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import { colors } from '../../design';
import { fileAnnotationKey, lineAnnotationKey, type AnnotationIndex } from '../../annotation_utils';
import { type ParsedLine, sortParsedLinesTestsLast } from '../../diff_utils';
import { groupIntoThreads } from '../../discussion_utils';
import { getClickColumn } from '../../utils/dom';
import type { PRAnnotation } from '../../plugin_utils';
import type { LspData } from '../../hooks/useLsp';
import LspPopover from '../LspPopover';
import AnnotationCard from './AnnotationCard';
import AnnotationIndicator from './AnnotationIndicator';
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
    // Plugin annotations bucketed by the row they anchor to.
    annotations: AnnotationIndex;
    // Annotation bucket keys whose card should render inline; buckets not in
    // this set are collapsed behind an indicator, same as comment threads.
    visibleAnnotationKeys: Set<string>;
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
    onToggleAnnotations: (key: string) => void;
    // Adopt a plugin annotation as a local comment on the row it is anchored to.
    onAnnotationToComment: (annotation: PRAnnotation, file: string, pos: number) => void;
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
    annotations,
    visibleAnnotationKeys,
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
    onToggleAnnotations,
    onAnnotationToComment,
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

    // Threads on a line, each one a root comment plus its whole reply subtree.
    // Grouping transitively matters: replying to a thread targets its *last*
    // comment, so a reply to a reply would otherwise never be rendered.
    const threadsOnLine = (item: ParsedLine): Comment[][] =>
        groupIntoThreads(commentsForLine(item));

    const threadsForLine = (item: ParsedLine, idx: number, stickyOnMobile: boolean) => {
        return threadsOnLine(item)
            .filter(thread => visibleThreadIds.has(thread[0].id))
            .map(thread => {
                const isReplyingToThread =
                    (replyToId !== null && thread.some(c => parseInt(c.id, 10) === replyToId)) ||
                    (editingCommentId !== null &&
                        thread.some(c => parseInt(c.id, 10) === editingCommentId));
                return (
                    <CommentThread
                        key={thread[0].id}
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
        const threads = threadsOnLine(item);
        if (threads.length === 0) return null;
        const rootIds = threads.map(t => t[0].id);
        const allVisible = rootIds.every(id => visibleThreadIds.has(id));
        return (
            <span onClick={e => e.stopPropagation()}>
                <CommentIndicator
                    thread={threads.flat()}
                    visible={allVisible}
                    onToggle={() => onToggleThreadsVisible(rootIds)}
                />
            </span>
        );
    };

    // Plugin annotations for a row: those anchored to the line's head-side line
    // number, or — on a file header — those whose line this diff doesn't render.
    const annotationsForLine = (item: ParsedLine): { key: string; list: PRAnnotation[] } | null => {
        if (!item.file) return null;
        const key =
            item.lineType === 'file-header'
                ? fileAnnotationKey(item.file)
                : item.newLineNo != null
                  ? lineAnnotationKey(item.file, item.newLineNo)
                  : null;
        if (!key) return null;
        const list =
            item.lineType === 'file-header'
                ? annotations.byFile.get(key)
                : annotations.byLine.get(key);
        return list && list.length > 0 ? { key, list } : null;
    };

    // Collapsed badge for an annotated line/file; expands every annotation on
    // that row together and previews them all on hover.
    const annotationIndicatorForLine = (item: ParsedLine, align: 'left' | 'right') => {
        const anchored = annotationsForLine(item);
        if (!anchored) return null;
        return (
            <span onClick={e => e.stopPropagation()}>
                <AnnotationIndicator
                    annotations={anchored.list}
                    visible={visibleAnnotationKeys.has(anchored.key)}
                    align={align}
                    onToggle={() => onToggleAnnotations(anchored.key)}
                />
            </span>
        );
    };

    // The badge's home on a code row: an end cap flush to the right of the whole
    // line, not a second gutter column — the code and its line numbers sit
    // exactly where they would in a PR with no annotations at all.
    //
    // It is opaque (the row's tint over the diff background) so an unwrapped
    // line spilling past the viewport passes behind the badge rather than
    // through it, and sticks to the visible right edge while the diff scrolls
    // horizontally on mobile.
    const annotationCellForLine = (item: ParsedLine, rowTint: string | null) => {
        const badge = annotationIndicatorForLine(item, 'right');
        if (!badge) return null;
        return (
            <span
                style={{
                    position: 'sticky',
                    right: 0,
                    display: 'flex',
                    alignItems: wrapLines ? 'flex-start' : 'center',
                    padding: wrapLines ? '2px 6px 0 10px' : '0 6px 0 10px',
                    backgroundColor: 'var(--bg-primary)',
                    backgroundImage: rowTint
                        ? `linear-gradient(${rowTint}, ${rowTint})`
                        : undefined,
                    userSelect: 'none',
                }}
            >
                {badge}
            </span>
        );
    };

    const annotationCardForLine = (item: ParsedLine, stickyOnMobile: boolean) => {
        const anchored = annotationsForLine(item);
        if (!anchored || !visibleAnnotationKeys.has(anchored.key)) return null;
        // A card only ever renders for a row with a file (see annotationsForLine);
        // the local binding is what tells the closure below so.
        const file = item.file;
        const pos = item.pos;
        return (
            <AnnotationCard
                annotations={anchored.list}
                showLines={item.lineType === 'file-header'}
                stickyOnMobile={stickyOnMobile}
                existingCommentBodies={new Set(commentsForLine(item).map(c => c.body))}
                isAddingComment={isAddingComment}
                onAddAsComment={
                    file && pos !== null
                        ? annotation => onAnnotationToComment(annotation, file, pos)
                        : undefined
                }
                onCollapse={() => onToggleAnnotations(anchored.key)}
            />
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
                                fontSize: '1em',
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
                        {annotationIndicatorForLine(item, 'left')}
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
                    {annotationCardForLine(item, false)}
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
            // Relative to the diff container's font size so the gutter tracks
            // the Review Diff Font Size preference.
            fontSize: '0.92em',
        };

        // On mobile, unwrapped lines scroll horizontally as a unit (see the
        // max-content wrapper below). The code span must size to its content
        // and never shrink so the row grows wider than the viewport.
        const mobileScroll = isMobile && !wrapLines;

        // The row's own tint, re-applied behind the annotation badge at the end
        // of the line (see annotationCellForLine). null for context rows, which
        // take the diff's background unchanged.
        const rowTint = isAddition ? colors.diffAddBg : isDeletion ? colors.diffDelBg : null;
        const annotationCell =
            isCodeLine && !isHunkHeader ? annotationCellForLine(item, rowTint) : null;

        let lineStyle: React.CSSProperties = {
            flex: mobileScroll ? '1 0 auto' : 1,
            minWidth: mobileScroll ? 'auto' : 0,
            padding: '0 8px',
            whiteSpace: wrapLines ? 'pre-wrap' : 'pre',
            overflowWrap: wrapLines ? 'anywhere' : 'normal',
            display: 'flex',
            alignItems: wrapLines ? 'flex-start' : 'center',
            // An unwrapped line longer than the viewport spills past its box.
            // Where a badge caps the row, stop the text at the badge instead of
            // letting it reappear on the far side of it. Nothing readable is
            // lost: the page clips that overflow at the viewport edge anyway.
            ...(annotationCell ? { overflow: 'hidden' } : {}),
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
                fontSize: '0.92em',
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
                        // While this row shows the LSP popover it takes the
                        // `filter`-free hover cue: filtering the row would make
                        // it a stacking context, burying the popover under the
                        // diff rows that follow it (see .hover-line--flat).
                        (item.clickable ? (isLspActive ? 'hover-line--flat' : 'hover-line') : '') +
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
                    {annotationCell}
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
                {annotationCardForLine(item, isMobile)}
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
