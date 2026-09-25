import React from 'react';
import Markdown from 'react-markdown';
import { shadows } from '../design';
import type { LspHover, LspLocation } from '../lsp';
import type { LspData } from '../hooks/useLsp';
import { displayPath, groupLocationsByFile, splitLocationLine } from '../lsp_utils';

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
    data: LspData;
    variant: 'floating' | 'inline';
    onRefClick?: (ref: LspLocation, e: React.MouseEvent) => void;
    onClose?: () => void;
}

const mutedStyle: React.CSSProperties = {
    fontStyle: 'italic',
    color: 'var(--text-secondary)',
};

// One file's locations: its path, then a row per location with the code on
// that line and the symbol highlighted — enough to tell a call from a
// declaration or a test from production code without opening each one.
function LocationGroup({
    uri,
    locations,
    data,
    onRefClick,
}: {
    uri: string;
    locations: LspLocation[];
    data: LspData;
    onRefClick?: (ref: LspLocation, e: React.MouseEvent) => void;
}) {
    const fileLines = data.lines[uri];
    return (
        <div style={{ marginBottom: '6px' }}>
            <div
                style={{
                    color: 'var(--text-secondary)',
                    fontFamily: 'var(--font-mono)',
                    fontSize: '11px',
                    marginBottom: '2px',
                    overflowWrap: 'anywhere',
                }}
                title={uri.replace(/^file:\/\//, '')}
            >
                {displayPath(uri, data.roots)}
            </div>
            {locations.map((location, i) => {
                const text = fileLines?.[location.range.start.line];
                const parts = text !== undefined ? splitLocationLine(text, location.range) : null;
                return (
                    <div
                        key={i}
                        className={onRefClick ? 'lsp-location-row' : undefined}
                        onClick={e => {
                            e.stopPropagation();
                            onRefClick?.(location, e);
                        }}
                        style={{
                            display: 'flex',
                            gap: '8px',
                            padding: '1px 4px',
                            borderRadius: '3px',
                            cursor: onRefClick ? 'pointer' : 'default',
                            fontFamily: 'var(--font-mono)',
                            fontSize: '12px',
                            lineHeight: '1.5',
                        }}
                    >
                        <span
                            style={{
                                color: 'var(--accent)',
                                minWidth: '3.5em',
                                textAlign: 'right',
                                flexShrink: 0,
                                userSelect: 'none',
                            }}
                        >
                            {location.range.start.line + 1}
                        </span>
                        <span
                            style={{
                                flex: 1,
                                minWidth: 0,
                                whiteSpace: 'pre',
                                overflow: 'hidden',
                                textOverflow: 'ellipsis',
                                tabSize: 4,
                                color: 'var(--text-primary)',
                            }}
                        >
                            {parts ? (
                                <>
                                    {parts.before}
                                    <mark
                                        style={{
                                            background: 'var(--accent-dim)',
                                            color: 'inherit',
                                            fontWeight: 600,
                                            borderRadius: '2px',
                                        }}
                                    >
                                        {parts.match}
                                    </mark>
                                    {parts.after}
                                </>
                            ) : (
                                <span style={{ color: 'var(--text-tertiary)' }}>…</span>
                            )}
                        </span>
                    </div>
                );
            })}
        </div>
    );
}

function LocationSection({
    title,
    locations,
    loading,
    data,
    onRefClick,
}: {
    title: string;
    locations: LspLocation[] | null;
    loading: boolean;
    data: LspData;
    onRefClick?: (ref: LspLocation, e: React.MouseEvent) => void;
}) {
    if (!loading && !locations) return null;
    return (
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
                {title}
                {locations && locations.length > 1 ? ` (${locations.length})` : ''}
            </div>
            {locations ? (
                groupLocationsByFile(locations).map(group => (
                    <LocationGroup
                        key={group.uri}
                        uri={group.uri}
                        locations={group.locations}
                        data={data}
                        onRefClick={onRefClick}
                    />
                ))
            ) : (
                <div style={mutedStyle}>Loading…</div>
            )}
        </div>
    );
}

function LspSections({
    data,
    onRefClick,
}: {
    data: LspData;
    onRefClick?: (ref: LspLocation, e: React.MouseEvent) => void;
}) {
    const nothingYet = !data.hover && !data.definitions && !data.typeDefinitions && !data.refs;
    const stillLoading = Object.values(data.loading).some(Boolean);
    if (nothingYet) {
        return <div style={mutedStyle}>{stillLoading ? 'Loading…' : 'No information found.'}</div>;
    }
    return (
        <>
            <LocationSection
                title="Definition"
                locations={data.definitions}
                loading={data.loading.definitions}
                data={data}
                onRefClick={onRefClick}
            />
            <LocationSection
                title="Type Definition"
                locations={data.typeDefinitions}
                loading={data.loading.typeDefinitions}
                data={data}
                onRefClick={onRefClick}
            />
            <LocationSection
                title="References"
                locations={data.refs}
                loading={data.loading.refs}
                data={data}
                onRefClick={onRefClick}
            />
        </>
    );
}

export default function LspPopover({ data, variant, onRefClick, onClose }: LspPopoverProps) {
    const hover = data.hover;

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
                    width: 'max-content',
                    minWidth: '240px',
                    maxWidth: 'min(760px, calc(100vw - 80px))',
                    overflow: 'auto',
                    maxHeight: '360px',
                }}
                onClick={e => e.stopPropagation()}
            >
                {onClose && (
                    <button
                        onClick={e => {
                            e.stopPropagation();
                            onClose();
                        }}
                        style={{
                            position: 'absolute',
                            top: '6px',
                            right: '6px',
                            background: 'none',
                            border: 'none',
                            cursor: 'pointer',
                            color: 'var(--text-secondary)',
                            fontSize: '16px',
                            lineHeight: 1,
                            padding: '2px 4px',
                        }}
                        aria-label="Close"
                    >
                        ×
                    </button>
                )}
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
                <LspSections data={data} onRefClick={onRefClick} />
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
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                }}
            >
                LSP Info
                {onClose && (
                    <button
                        onClick={e => {
                            e.stopPropagation();
                            onClose();
                        }}
                        style={{
                            background: 'none',
                            border: 'none',
                            cursor: 'pointer',
                            color: 'var(--text-secondary)',
                            fontSize: '16px',
                            lineHeight: 1,
                            padding: '0 2px',
                        }}
                        aria-label="Close"
                    >
                        ×
                    </button>
                )}
            </div>
            <div
                style={{
                    padding: '10px',
                    background: 'var(--bg-primary)',
                    maxHeight: '360px',
                    overflow: 'auto',
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
                <LspSections data={data} onRefClick={onRefClick} />
            </div>
        </div>
    );
}
