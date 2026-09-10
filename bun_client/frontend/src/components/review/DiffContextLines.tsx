import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter';
import type { ParsedLine } from '../../diff_utils';
import type { DiffTheme } from './diff_theme';
import { diffRowPrefixChar, diffRowStyles } from './diff_row_style';
import { getLanguageFromFilename } from './types';

interface DiffContextLinesProps {
    // Rows to render, already sliced out of the parsed diff.
    lines: ParsedLine[];
    // File the rows belong to; picks the syntax-highlighting language.
    file: string;
    diffTheme: DiffTheme;
    // Highlight one row as the line a comment is anchored to.
    anchorIndex?: number;
    wrapLines?: boolean;
}

// A handful of diff rows rendered read-only, outside the diff: same tints,
// line-number gutters, +/- prefix and syntax highlighting as DiffView (they
// share diff_row_style), minus every interaction. Used to show a pending
// comment's context in the review preview.
export default function DiffContextLines({
    lines,
    file,
    diffTheme,
    anchorIndex,
    wrapLines = false,
}: DiffContextLinesProps) {
    if (lines.length === 0) return null;
    const language = getLanguageFromFilename(file);

    return (
        <div
            style={{
                fontFamily: 'var(--font-mono)',
                // Same base as the diff container, so the preview tracks the
                // Review Diff Font Size preference too.
                fontSize: 'var(--diff-font-size, 13px)',
                overflowX: 'auto',
            }}
        >
            {lines.map((item, i) => {
                const isHunkHeader = item.lineType === 'hunk';
                const isCodeLine =
                    item.lineType === 'addition' ||
                    item.lineType === 'deletion' ||
                    item.lineType === 'code';
                const { container, prefix, line } = diffRowStyles(item, { wrapLines });
                const isAnchor = anchorIndex === i;

                return (
                    <div
                        key={`${item.originalLineIndex}-${i}`}
                        style={{
                            ...container,
                            // The commented line itself, marked the way an
                            // active row is marked in the diff.
                            borderLeft: isAnchor
                                ? '3px solid var(--accent)'
                                : '3px solid transparent',
                        }}
                    >
                        {isCodeLine && !isHunkHeader && (
                            <>
                                <span className="diff-line-no" aria-hidden="true">
                                    {item.oldLineNo ?? ''}
                                </span>
                                <span className="diff-line-no" aria-hidden="true">
                                    {item.newLineNo ?? ''}
                                </span>
                                <span style={prefix}>{diffRowPrefixChar(item)}</span>
                            </>
                        )}
                        <span style={line}>
                            {isCodeLine && !isHunkHeader ? (
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
                                    {item.text.slice(1) || ' '}
                                </SyntaxHighlighter>
                            ) : (
                                item.text
                            )}
                        </span>
                    </div>
                );
            })}
        </div>
    );
}
