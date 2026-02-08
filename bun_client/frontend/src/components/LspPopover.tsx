import React from 'react';
import Markdown from 'react-markdown';
import { shadows } from '../design';
import type { LspHover, LspLocation } from '../lsp';

export function getHoverContent(hover: LspHover | null): string {
    if (!hover || !hover.contents) return '';
    if (typeof hover.contents === 'string') return hover.contents;
    if (Array.isArray(hover.contents)) {
        return hover.contents
            .map((c: string | { language: string; value: string }) =>
                typeof c === 'string' ? c : c.value
            )
            .join('\n');
    }
    return (hover.contents as { value: string }).value || '';
}

interface LspPopoverProps {
    hover: LspHover | null;
    refs: LspLocation[] | null;
    definitions: LspLocation[] | null;
    variant: 'floating' | 'inline';
    onRefClick?: (ref: LspLocation, e: React.MouseEvent) => void;
    onClose?: () => void;
}

export default function LspPopover({
    hover,
    refs,
    definitions,
    variant,
    onRefClick,
}: LspPopoverProps) {
    if (variant === 'floating') {
        return (
            <div
                style={{
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
                    maxHeight: '300px',
                }}
                onClick={e => e.stopPropagation()}
            >
                {hover && (
                    <div
                        style={{
                            fontSize: '13px',
                            lineHeight: '1.5',
                            color: 'var(--text-primary)',
                        }}
                    >
                        <Markdown>{getHoverContent(hover)}</Markdown>
                    </div>
                )}
                {definitions && definitions.length > 0 && (
                    <div
                        style={{
                            marginTop: '10px',
                            paddingTop: '10px',
                            borderTop: '1px solid var(--border)',
                            fontSize: '12px',
                        }}
                    >
                        <div
                            style={{
                                fontWeight: 600,
                                marginBottom: '5px',
                                color: 'var(--text-secondary)',
                            }}
                        >
                            Definition:
                        </div>
                        <ul style={{ margin: '0 0 0 15px', padding: 0 }}>
                            {definitions.map((d, i) => (
                                <li
                                    key={i}
                                    onClick={e => {
                                        e.stopPropagation();
                                        onRefClick?.(d, e);
                                    }}
                                    style={{
                                        cursor: onRefClick ? 'pointer' : 'default',
                                        color: 'var(--accent)',
                                        textDecoration: onRefClick ? 'underline' : 'none',
                                        marginBottom: '2px',
                                    }}
                                    className={onRefClick ? 'hover-link' : ''}
                                >
                                    {d.uri.split('/').pop()} : {d.range.start.line + 1}
                                </li>
                            ))}
                        </ul>
                    </div>
                )}
                {refs && refs.length > 0 && (
                    <div
                        style={{
                            marginTop: '10px',
                            paddingTop: '10px',
                            borderTop: '1px solid var(--border)',
                            fontSize: '12px',
                        }}
                    >
                        <div
                            style={{
                                fontWeight: 600,
                                marginBottom: '5px',
                                color: 'var(--text-secondary)',
                            }}
                        >
                            References ({refs.length}):
                        </div>
                        <ul style={{ margin: '0 0 0 15px', padding: 0 }}>
                            {refs.map((r, i) => (
                                <li
                                    key={i}
                                    onClick={e => {
                                        e.stopPropagation();
                                        onRefClick?.(r, e);
                                    }}
                                    style={{
                                        cursor: onRefClick ? 'pointer' : 'default',
                                        color: 'var(--accent)',
                                        textDecoration: onRefClick ? 'underline' : 'none',
                                        marginBottom: '2px',
                                    }}
                                    className={onRefClick ? 'hover-link' : ''}
                                >
                                    {r.uri.split('/').pop()} : {r.range.start.line + 1}
                                </li>
                            ))}
                        </ul>
                    </div>
                )}
            </div>
        );
    }

    // inline variant
    return (
        <div
            style={{
                marginBottom: '10px',
                fontSize: '13px',
                border: '1px solid var(--border)',
                borderRadius: '4px',
                overflow: 'hidden',
            }}
        >
            <div
                style={{
                    background: 'var(--bg-secondary)',
                    padding: '5px 10px',
                    fontWeight: 600,
                    borderBottom: '1px solid var(--border)',
                }}
            >
                LSP Info
            </div>
            <div
                style={{
                    padding: '10px',
                    background: 'var(--bg-primary)',
                }}
            >
                {hover && (
                    <div style={{ marginBottom: '10px' }}>
                        <strong>Hover:</strong>
                        <pre
                            style={{
                                whiteSpace: 'pre-wrap',
                                marginTop: '5px',
                            }}
                        >
                            {getHoverContent(hover)}
                        </pre>
                    </div>
                )}
                {definitions && definitions.length > 0 && (
                    <div style={{ marginBottom: '10px' }}>
                        <strong>Definition:</strong>
                        <ul
                            style={{
                                margin: '5px 0 0 20px',
                                padding: 0,
                            }}
                        >
                            {definitions.map((d, i) => (
                                <li
                                    key={i}
                                    onClick={e => {
                                        e.stopPropagation();
                                        onRefClick?.(d, e);
                                    }}
                                    style={{
                                        cursor: onRefClick ? 'pointer' : 'default',
                                        color: onRefClick ? 'var(--accent)' : 'inherit',
                                        textDecoration: onRefClick ? 'underline' : 'none',
                                    }}
                                >
                                    {d.uri} : {d.range.start.line + 1}
                                </li>
                            ))}
                        </ul>
                    </div>
                )}
                {refs && refs.length > 0 && (
                    <div>
                        <strong>References ({refs.length}):</strong>
                        <ul
                            style={{
                                margin: '5px 0 0 20px',
                                padding: 0,
                            }}
                        >
                            {refs.map((r, i) => (
                                <li
                                    key={i}
                                    onClick={e => {
                                        e.stopPropagation();
                                        onRefClick?.(r, e);
                                    }}
                                    style={{
                                        cursor: onRefClick ? 'pointer' : 'default',
                                        color: onRefClick ? 'var(--accent)' : 'inherit',
                                        textDecoration: onRefClick ? 'underline' : 'none',
                                    }}
                                >
                                    {r.uri} : {r.range.start.line + 1}
                                </li>
                            ))}
                        </ul>
                    </div>
                )}
                {!hover &&
                    (!refs || refs.length === 0) &&
                    (!definitions || definitions.length === 0) && (
                        <div
                            style={{
                                fontStyle: 'italic',
                                color: 'var(--text-secondary)',
                            }}
                        >
                            No information found.
                        </div>
                    )}
            </div>
        </div>
    );
}
