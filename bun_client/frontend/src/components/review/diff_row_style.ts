import type { CSSProperties } from 'react';
import { colors } from '../../design';
import type { ParsedLine } from '../../diff_utils';

// The visual language of a single diff row — tints, gutter and line box —
// shared by the diff itself (DiffView) and every read-only re-render of a few
// diff lines outside it (DiffContextLines, used by the review preview), so a
// comment's context looks like the diff it was left on.

export interface DiffRowStyleOptions {
    // Wrap long lines instead of letting them run past the row.
    wrapLines: boolean;
    // Mobile horizontal scrolling: the code span sizes to its content and never
    // shrinks, so the row grows wider than the viewport instead of squeezing.
    mobileScroll?: boolean;
    // Stop the text at a trailing badge instead of letting an unwrapped line
    // reappear on the far side of it.
    clipLine?: boolean;
}

export interface DiffRowStyles {
    container: CSSProperties;
    prefix: CSSProperties;
    line: CSSProperties;
}

/**
 * The row's own tint: green for an addition, red for a deletion, null for a
 * context row, which takes the diff's background unchanged. Callers re-apply it
 * behind anything they paint at the end of the line.
 */
export const diffRowTint = (item: ParsedLine): string | null =>
    item.lineType === 'addition'
        ? colors.diffAddBg
        : item.lineType === 'deletion'
          ? colors.diffDelBg
          : null;

/** The +/- character shown in a code row's prefix gutter ('' for context). */
export const diffRowPrefixChar = (item: ParsedLine): string =>
    item.lineType === 'addition' ? '+' : item.lineType === 'deletion' ? '-' : '';

export function diffRowStyles(item: ParsedLine, opts: DiffRowStyleOptions): DiffRowStyles {
    const { wrapLines, mobileScroll = false, clipLine = false } = opts;
    const isAddition = item.lineType === 'addition';
    const isDeletion = item.lineType === 'deletion';
    const isHunkHeader = item.lineType === 'hunk';

    let container: CSSProperties = {
        display: 'flex',
        alignItems: 'stretch',
        minHeight: '20px',
        position: 'relative',
    };

    let prefix: CSSProperties = {
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

    let line: CSSProperties = {
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
        ...(clipLine ? { overflow: 'hidden' } : {}),
    };

    if (isAddition) {
        container = { ...container, background: colors.diffAddBg };
        prefix = {
            ...prefix,
            color: colors.success,
            background: colors.diffAddGutterBg,
        };
    } else if (isDeletion) {
        container = { ...container, background: colors.diffDelBg };
        prefix = {
            ...prefix,
            color: colors.danger,
            background: colors.diffDelGutterBg,
        };
    } else if (isHunkHeader) {
        // Let .diff-hunk-row own `position` (sticky on desktop, static on
        // mobile). An inline `position: relative` here would override the
        // class's sticky while its `top` offset still applied, painting
        // the header below its slot and overlapping the rows beneath it.
        container = {
            ...container,
            background: colors.diffHunkBg,
            position: undefined,
        };
        line = {
            ...line,
            color: colors.accent,
            fontStyle: 'italic',
            fontSize: '0.92em',
        };
    }

    return { container, prefix, line };
}
