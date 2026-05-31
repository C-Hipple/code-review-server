import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Prism as SyntaxHighlighter, createElement } from 'react-syntax-highlighter';
import type { createElementProps } from 'react-syntax-highlighter';
import { colors } from '../design';
import { getClickColumn } from '../utils/dom';

// Fixed row geometry. Virtualization math and the active/target line overlays
// all depend on every rendered line being exactly this tall, so the line
// elements below pin their height/line-height to these constants and never wrap.
export const CODE_LINE_HEIGHT = 20;
const CODE_FONT_SIZE = 13;

// Files at or below this many lines are highlighted once and rendered in full
// (correct highlighting, no windowing artifacts). Larger files switch to
// windowed rendering so we never mount thousands of DOM nodes at once.
const VIRTUALIZE_THRESHOLD = 1500;
// Extra lines rendered above/below the viewport in windowed mode. Also gives
// multi-line tokens (block comments, template strings) some context so the
// highlighting at the window edges is usually correct.
const OVERSCAN = 40;

type RowNode = createElementProps['node'];
type Stylesheet = { [key: string]: React.CSSProperties };

interface RendererArgs {
    rows: RowNode[];
    stylesheet: Stylesheet;
    useInlineStyles: boolean;
}

interface VirtualizedCodeBlockProps {
    content: string;
    language: string;
    syntaxStyle: Stylesheet;
    /** 1-based line clicked for an LSP query (gets the "active" highlight). */
    activeLine?: number | null;
    /** 1-based line to emphasise (jump target from a reference/definition). */
    targetLine?: number | null;
    /** 1-based line to scroll into view (centered) when it or the content changes. */
    scrollToLine?: number | null;
    /** Fires with a 1-based line and 0-based column on click. */
    onLineClick?: (line: number, column: number) => void;
    clickable?: boolean;
    /** Rendered inside the scrolled content area (e.g. an LSP popover). */
    children?: React.ReactNode;
}

const PRE_CODE_RESET: React.CSSProperties = {
    margin: 0,
    padding: 0,
    background: 'transparent',
    fontFamily: 'var(--font-mono)',
    fontSize: `${CODE_FONT_SIZE}px`,
    lineHeight: `${CODE_LINE_HEIGHT}px`,
};

/**
 * Renders one source line per row with a sticky gutter. Shared by the full and
 * windowed render paths; `offset` is the 0-based index of the first row so line
 * numbers stay correct when only a window of the file is highlighted.
 */
function renderRows(
    { rows, stylesheet, useInlineStyles }: RendererArgs,
    offset: number,
    gutterWidth: number
): React.ReactNode {
    return rows.map((node, i) => {
        const lineNumber = offset + i + 1;
        return (
            <div
                key={lineNumber}
                data-line={lineNumber}
                style={{
                    display: 'flex',
                    height: `${CODE_LINE_HEIGHT}px`,
                    lineHeight: `${CODE_LINE_HEIGHT}px`,
                }}
            >
                <span
                    aria-hidden="true"
                    style={{
                        flex: `0 0 ${gutterWidth}px`,
                        width: `${gutterWidth}px`,
                        position: 'sticky',
                        left: 0,
                        zIndex: 2,
                        textAlign: 'right',
                        paddingRight: '8px',
                        color: 'var(--text-tertiary)',
                        background: 'var(--bg-secondary)',
                        borderRight: '1px solid var(--border)',
                        userSelect: 'none',
                    }}
                >
                    {lineNumber}
                </span>
                <span className="vcb-code" style={{ paddingLeft: '8px', whiteSpace: 'pre' }}>
                    {createElement({ node, stylesheet, useInlineStyles, key: `c-${i}` })}
                </span>
            </div>
        );
    });
}

/**
 * Displays a whole file with syntax highlighting. Highlighting is decoupled from
 * interaction state (drag, resize, LSP selection) so it is computed once for
 * normal files; very large files are windowed to keep the DOM small.
 */
export default function VirtualizedCodeBlock({
    content,
    language,
    syntaxStyle,
    activeLine,
    targetLine,
    scrollToLine,
    onLineClick,
    clickable = false,
    children,
}: VirtualizedCodeBlockProps) {
    const scrollRef = useRef<HTMLDivElement>(null);
    const rafRef = useRef<number | null>(null);

    const lines = useMemo(() => content.split('\n'), [content]);
    const total = lines.length;
    const virtualize = total > VIRTUALIZE_THRESHOLD;

    const gutterWidth = useMemo(() => {
        const digits = String(Math.max(total, 1)).length;
        return Math.max(48, digits * 9 + 16);
    }, [total]);

    const [scrollTop, setScrollTop] = useState(0);
    const [viewportH, setViewportH] = useState(0);

    // Track viewport height for windowing.
    useEffect(() => {
        const el = scrollRef.current;
        if (!el || !virtualize) return;
        const measure = () => setViewportH(el.clientHeight);
        measure();
        const ro = new ResizeObserver(measure);
        ro.observe(el);
        return () => ro.disconnect();
    }, [virtualize]);

    const handleScroll = useCallback(() => {
        if (!virtualize) return;
        if (rafRef.current !== null) return;
        rafRef.current = requestAnimationFrame(() => {
            rafRef.current = null;
            setScrollTop(scrollRef.current?.scrollTop ?? 0);
        });
    }, [virtualize]);

    useEffect(
        () => () => {
            if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
        },
        []
    );

    // Scroll a requested line into view (centered). Re-runs when the target line
    // changes or when fresh content loads (e.g. navigating to another file).
    useEffect(() => {
        const el = scrollRef.current;
        if (!scrollToLine || !el) return;
        const id = setTimeout(() => {
            const top = Math.max(0, (scrollToLine - 1) * CODE_LINE_HEIGHT - el.clientHeight / 2);
            el.scrollTo({ top, behavior: 'smooth' });
        }, 50);
        return () => clearTimeout(id);
    }, [scrollToLine, content]);

    const fullRenderer = useCallback(
        (args: RendererArgs) => renderRows(args, 0, gutterWidth),
        [gutterWidth]
    );

    // Full highlight: computed once per content/language/theme. Memoizing the
    // element keeps interaction-driven re-renders from re-tokenizing the file.
    const fullBlock = useMemo(
        () => (
            <SyntaxHighlighter
                language={language}
                style={syntaxStyle}
                renderer={fullRenderer}
                PreTag="div"
                CodeTag="div"
                customStyle={PRE_CODE_RESET}
            >
                {content}
            </SyntaxHighlighter>
        ),
        [content, language, syntaxStyle, fullRenderer]
    );

    let codeBody: React.ReactNode;
    if (!virtualize) {
        codeBody = <div style={{ position: 'relative', zIndex: 1 }}>{fullBlock}</div>;
    } else {
        const start = Math.max(0, Math.floor(scrollTop / CODE_LINE_HEIGHT) - OVERSCAN);
        const end = Math.min(
            total,
            Math.ceil((scrollTop + (viewportH || 0)) / CODE_LINE_HEIGHT) + OVERSCAN
        );
        const visibleText = lines.slice(start, end).join('\n');
        codeBody = (
            <div
                style={{
                    position: 'absolute',
                    top: `${start * CODE_LINE_HEIGHT}px`,
                    left: 0,
                    right: 0,
                    zIndex: 1,
                }}
            >
                <SyntaxHighlighter
                    language={language}
                    style={syntaxStyle}
                    renderer={(args: RendererArgs) => renderRows(args, start, gutterWidth)}
                    PreTag="div"
                    CodeTag="div"
                    customStyle={PRE_CODE_RESET}
                >
                    {visibleText || ' '}
                </SyntaxHighlighter>
            </div>
        );
    }

    const lineOverlay = (line: number | null | undefined, bg: string, border: string) => {
        if (!line || line < 1 || line > total) return null;
        return (
            <div
                style={{
                    position: 'absolute',
                    top: `${(line - 1) * CODE_LINE_HEIGHT}px`,
                    left: 0,
                    right: 0,
                    height: `${CODE_LINE_HEIGHT}px`,
                    background: bg,
                    borderLeft: `3px solid ${border}`,
                    zIndex: 0,
                    pointerEvents: 'none',
                }}
            />
        );
    };

    const handleClick = useCallback(
        (e: React.MouseEvent) => {
            if (!clickable || !onLineClick) return;
            const lineEl = (e.target as HTMLElement).closest('[data-line]');
            if (!lineEl) return;
            const line = Number(lineEl.getAttribute('data-line'));
            if (!line) return;
            const codeEl = lineEl.querySelector('.vcb-code') as HTMLElement | null;
            const column = codeEl ? getClickColumn(e, codeEl) : 0;
            onLineClick(line, column);
        },
        [clickable, onLineClick]
    );

    return (
        <div
            ref={scrollRef}
            className="custom-scrollbar"
            onScroll={handleScroll}
            onClick={handleClick}
            style={{
                height: '100%',
                overflow: 'auto',
                background: 'var(--bg-primary)',
                fontFamily: 'var(--font-mono)',
                fontSize: `${CODE_FONT_SIZE}px`,
                cursor: clickable ? 'pointer' : 'default',
            }}
        >
            <div
                style={{
                    position: 'relative',
                    minWidth: '100%',
                    width: 'fit-content',
                    height: virtualize ? `${total * CODE_LINE_HEIGHT}px` : undefined,
                }}
            >
                {lineOverlay(targetLine, colors.bgWarningDim, colors.warning)}
                {lineOverlay(activeLine, colors.bgInfoDim, 'var(--accent)')}
                {codeBody}
                {children}
            </div>
        </div>
    );
}
