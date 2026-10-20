import { useEffect, useState, type CSSProperties } from 'react';
import { changeClassesUsed, svgSize } from './ai_utils';
import { Button } from './Button';
import { usePrefersDark } from './color_scheme';
import { CheckIcon, CopyIcon } from './icons';
import { renderMermaid } from './mermaid_loader';

/** What a drawing of `source` came to: its SVG, or why mermaid couldn't draw it. */
interface Drawing {
    source: string;
    svg?: string;
    error?: string;
}

// The classes the server gives a change diagram's nodes
// (changeClasses in ai/change_diagram.go), for the legend.
const LEGEND = {
    added: { label: 'Added', fill: '#dcfce7', stroke: '#16a34a', dashed: false },
    changed: { label: 'Changed', fill: '#fef3c7', stroke: '#d97706', dashed: false },
    removed: { label: 'Removed', fill: '#fee2e2', stroke: '#dc2626', dashed: true },
} as const;

/**
 * A Mermaid diagram (change-diagram's report), drawn with the lazily loaded
 * mermaid library in the system's light or dark theme and fitted to the
 * card's width. The source is a toggle away, and shown beside mermaid's
 * error when it can't be drawn.
 */
export function MermaidDiagram({ source }: { source: string }) {
    const dark = usePrefersDark();
    const [drawing, setDrawing] = useState<Drawing | null>(null);
    const [showSource, setShowSource] = useState(false);
    // Fit to the card's width (never below a readable scale), or natural size.
    const [actualSize, setActualSize] = useState(false);
    const [copied, setCopied] = useState<'copied' | 'failed' | null>(null);

    useEffect(() => {
        let live = true;
        renderMermaid(source, dark).then(
            svg => live && setDrawing({ source, svg }),
            e => live && setDrawing({ source, error: e instanceof Error ? e.message : String(e) })
        );
        return () => {
            live = false;
        };
    }, [source, dark]);

    // A theme change redraws the same source; the old drawing stays meanwhile.
    const current = drawing?.source === source ? drawing : null;
    const error = current?.error;
    const legend = changeClassesUsed(source);
    const size = current?.svg ? svgSize(current.svg) : null;

    const copy = () => {
        const done = (result: 'copied' | 'failed') => {
            setCopied(result);
            setTimeout(() => setCopied(c => (c === result ? null : c)), 2000);
        };
        if (!navigator.clipboard) return done('failed');
        navigator.clipboard.writeText(source).then(
            () => done('copied'),
            () => done('failed')
        );
    };

    let content;
    if (error) {
        content = (
            <div className="diagram-failed">
                <SourceView source={source} />
                <div className="diagram-error" role="alert">
                    <strong>Mermaid couldn&apos;t draw this diagram.</strong>
                    <pre>{error}</pre>
                </div>
            </div>
        );
    } else if (showSource) {
        content = <SourceView source={source} />;
    } else if (current?.svg) {
        // mermaid's strict mode sanitizes the SVG it returns.
        content = (
            <div
                className={`diagram-canvas${actualSize ? ' diagram-actual' : ''}`}
                role="img"
                aria-label="Change diagram"
                style={
                    size ? ({ '--diagram-width': `${size.width}px` } as CSSProperties) : undefined
                }
                dangerouslySetInnerHTML={{ __html: current.svg }}
            />
        );
    } else {
        content = <div className="diagram-placeholder">Drawing the diagram…</div>;
    }

    return (
        <div className="diagram">
            <div className="diagram-toolbar">
                {legend.length > 0 && !error && (
                    <ul className="legend" aria-label="Legend">
                        {legend.map(name => {
                            const item = LEGEND[name];
                            return (
                                <li key={name}>
                                    <span
                                        className="legend-swatch"
                                        aria-hidden="true"
                                        style={{
                                            background: item.fill,
                                            borderColor: item.stroke,
                                            borderStyle: item.dashed ? 'dashed' : 'solid',
                                        }}
                                    />
                                    {item.label}
                                </li>
                            );
                        })}
                    </ul>
                )}
                <span className="spacer" />
                {size && !error && !showSource && (
                    <Button
                        size="sm"
                        onClick={() => setActualSize(a => !a)}
                        aria-label={
                            actualSize
                                ? 'Fit the diagram to the width'
                                : 'Show the diagram at full size'
                        }
                    >
                        {actualSize ? 'Fit' : '100%'}
                    </Button>
                )}
                {!error && (
                    <Button
                        size="sm"
                        aria-pressed={showSource}
                        onClick={() => setShowSource(s => !s)}
                    >
                        Source
                    </Button>
                )}
                <Button
                    size="sm"
                    icon={copied === 'copied' ? <CheckIcon /> : <CopyIcon />}
                    onClick={copy}
                >
                    {copied === 'copied'
                        ? 'Copied'
                        : copied === 'failed'
                          ? 'Copy failed'
                          : 'Copy source'}
                </Button>
            </div>
            {content}
        </div>
    );
}

function SourceView({ source }: { source: string }) {
    return (
        <pre className="diagram-source" tabIndex={0} aria-label="Mermaid source">
            <code>
                {source.split('\n').map((line, i) => (
                    <span key={i} className="source-line">
                        {line}
                        {'\n'}
                    </span>
                ))}
            </code>
        </pre>
    );
}
