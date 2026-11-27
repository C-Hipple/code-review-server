import React, { useEffect, useMemo, useState } from 'react';
import type { LspLocation, LspSymbolInformation } from '../lsp';
import { displayPath, filterOutline, type OutlineSymbol, symbolKindName } from '../lsp_utils';

interface SymbolsPanelProps {
    /** Whether the file on show has a language server. */
    available: boolean | null;
    /** The file's declarations; null while they're asked for. */
    symbols: OutlineSymbol[] | null;
    /** Roots that workspace results' paths are shown relative to. */
    roots: string[];
    searchWorkspace: (query: string, signal?: AbortSignal) => Promise<LspSymbolInformation[]>;
    /** A declaration in this file, by 1-based line. */
    onPickLine: (line: number) => void;
    /** A declaration anywhere in the workspace. */
    onPickLocation: (location: LspLocation) => void;
}

// Asking the workspace on every keystroke would queue a search per letter.
const SEARCH_DEBOUNCE_MS = 250;
const MIN_SEARCH_LENGTH = 2;
const MAX_RESULTS = 200;

const headingStyle: React.CSSProperties = {
    padding: '8px 12px 4px',
    fontSize: '11px',
    fontWeight: 700,
    color: 'var(--text-tertiary)',
    textTransform: 'uppercase',
    letterSpacing: '0.5px',
};

const mutedStyle: React.CSSProperties = {
    padding: '4px 12px',
    fontSize: '12px',
    fontStyle: 'italic',
    color: 'var(--text-secondary)',
};

function KindBadge({ kind }: { kind: number }) {
    return (
        <span
            style={{
                flexShrink: 0,
                fontSize: '10px',
                color: 'var(--text-tertiary)',
                minWidth: '52px',
                textAlign: 'right',
            }}
        >
            {symbolKindName(kind)}
        </span>
    );
}

const rowStyle = (depth = 0): React.CSSProperties => ({
    display: 'flex',
    alignItems: 'baseline',
    gap: '8px',
    padding: '2px 12px',
    paddingLeft: `${12 + depth * 12}px`,
    cursor: 'pointer',
    fontFamily: 'var(--font-mono)',
    fontSize: '12px',
    lineHeight: '1.6',
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
});

/**
 * The code viewer's symbol navigator, the browser's take on an editor's
 * outline (imenu) and go-to-symbol: the open file's declarations, filtered
 * as you type, and once the query is long enough, matching declarations
 * across the workspace from the language server.
 */
export default function SymbolsPanel({
    available,
    symbols,
    roots,
    searchWorkspace,
    onPickLine,
    onPickLocation,
}: SymbolsPanelProps) {
    const [query, setQuery] = useState('');
    const [results, setResults] = useState<LspSymbolInformation[] | null>(null);
    const [searching, setSearching] = useState(false);

    const outline = useMemo(() => filterOutline(symbols ?? [], query), [symbols, query]);
    const trimmed = query.trim();
    const searchable = !!available && trimmed.length >= MIN_SEARCH_LENGTH;

    useEffect(() => {
        if (!searchable) {
            setResults(null);
            setSearching(false);
            return;
        }
        const controller = new AbortController();
        setSearching(true);
        const timer = setTimeout(() => {
            searchWorkspace(trimmed, controller.signal)
                .then(found => {
                    if (!controller.signal.aborted) setResults(found.slice(0, MAX_RESULTS));
                })
                .catch(e => {
                    if (controller.signal.aborted) return;
                    console.error('LSP workspace/symbol failed', e);
                    setResults([]);
                })
                .finally(() => {
                    if (!controller.signal.aborted) setSearching(false);
                });
        }, SEARCH_DEBOUNCE_MS);
        return () => {
            clearTimeout(timer);
            controller.abort();
        };
    }, [trimmed, searchable, searchWorkspace]);

    return (
        <div style={{ display: 'flex', flexDirection: 'column', minHeight: 0, flex: 1 }}>
            <div style={{ padding: '8px', borderBottom: '1px solid var(--border)' }}>
                <input
                    type="search"
                    value={query}
                    onChange={e => setQuery(e.target.value)}
                    onKeyDown={e => {
                        // Escape clears the query rather than closing the viewer.
                        if (e.key === 'Escape' && query) {
                            e.stopPropagation();
                            e.nativeEvent.stopImmediatePropagation();
                            setQuery('');
                        }
                    }}
                    placeholder={available ? 'Filter, or search the workspace…' : 'Filter symbols…'}
                    aria-label="Search symbols"
                    style={{
                        width: '100%',
                        boxSizing: 'border-box',
                        padding: '5px 8px',
                        fontSize: '12px',
                        background: 'var(--bg-primary)',
                        color: 'var(--text-primary)',
                        border: '1px solid var(--border)',
                        borderRadius: '4px',
                    }}
                />
            </div>
            <div
                className="custom-scrollbar"
                style={{ flex: 1, overflowY: 'auto', paddingBottom: '8px' }}
            >
                <div style={headingStyle}>In this file</div>
                {available === false ? (
                    <div style={mutedStyle}>No language server for this file.</div>
                ) : symbols === null ? (
                    <div style={mutedStyle}>Loading…</div>
                ) : outline.length === 0 ? (
                    <div style={mutedStyle}>{trimmed ? 'No matches.' : 'No symbols.'}</div>
                ) : (
                    <div role="list" aria-label="Symbols in this file">
                        {outline.map((sym, i) => (
                            <div
                                key={`${sym.line}:${sym.character}:${i}`}
                                role="listitem"
                                className="lsp-symbol-row"
                                title={`${sym.name}${sym.detail ? ` ${sym.detail}` : ''} — line ${sym.line + 1}`}
                                onClick={() => onPickLine(sym.line + 1)}
                                style={rowStyle(trimmed ? 0 : sym.depth)}
                            >
                                <span
                                    style={{
                                        color: 'var(--text-primary)',
                                        overflow: 'hidden',
                                        textOverflow: 'ellipsis',
                                    }}
                                >
                                    {sym.name}
                                </span>
                                <span style={{ flex: 1 }} />
                                <KindBadge kind={sym.kind} />
                            </div>
                        ))}
                    </div>
                )}

                {searchable && (
                    <>
                        <div style={{ ...headingStyle, paddingTop: '12px' }}>
                            In the workspace
                            {results && results.length > 0 ? ` (${results.length})` : ''}
                        </div>
                        {results === null || (searching && results.length === 0) ? (
                            <div style={mutedStyle}>Searching…</div>
                        ) : results.length === 0 ? (
                            <div style={mutedStyle}>No matches.</div>
                        ) : (
                            <div role="list" aria-label="Symbols in the workspace">
                                {results.map((sym, i) => {
                                    const where = `${displayPath(sym.location.uri, roots)}:${sym.location.range.start.line + 1}`;
                                    return (
                                        <div
                                            key={`${sym.location.uri}:${sym.location.range.start.line}:${i}`}
                                            role="listitem"
                                            className="lsp-symbol-row"
                                            title={`${sym.containerName ? `${sym.containerName}.` : ''}${sym.name} — ${where}`}
                                            onClick={() => onPickLocation(sym.location)}
                                            style={{
                                                ...rowStyle(),
                                                flexDirection: 'column',
                                                alignItems: 'stretch',
                                                gap: 0,
                                            }}
                                        >
                                            <div style={{ display: 'flex', gap: '8px' }}>
                                                <span
                                                    style={{
                                                        color: 'var(--text-primary)',
                                                        overflow: 'hidden',
                                                        textOverflow: 'ellipsis',
                                                    }}
                                                >
                                                    {sym.name}
                                                </span>
                                                <span style={{ flex: 1 }} />
                                                <KindBadge kind={sym.kind} />
                                            </div>
                                            <div
                                                style={{
                                                    fontSize: '11px',
                                                    color: 'var(--text-secondary)',
                                                    overflow: 'hidden',
                                                    textOverflow: 'ellipsis',
                                                }}
                                            >
                                                {where}
                                            </div>
                                        </div>
                                    );
                                })}
                            </div>
                        )}
                    </>
                )}
            </div>
        </div>
    );
}
