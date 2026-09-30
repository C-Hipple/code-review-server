import { useCallback, useEffect, useRef, useState, type CSSProperties } from 'react';
import { Button } from '../design';
import {
    appThemeIsDark,
    fitScale,
    renderMermaid,
    stepZoom,
    svgSize,
    type Size,
} from '../mermaid_utils';

interface MermaidDiagramProps {
    /** The diagram's raw Mermaid source. */
    source: string;
    /** Show what the added / changed / removed colors mean (a change diagram's flowchart). */
    legend?: boolean;
    /** Grow to fill the parent, a flex column, rather than taking a fixed height. */
    fill?: boolean;
}

/** A drawing of `source`: its SVG, or why mermaid couldn't draw it. */
type Drawing =
    | { source: string; svg: string; size: Size | null; error?: undefined }
    | { source: string; error: string };

/** Space kept around the diagram inside the canvas. */
const CANVAS_PADDING = 16;

// Mirrors the classes the server defines for a change diagram
// (changeClasses in ai/change_diagram.go).
const LEGEND = [
    { label: 'Added', fill: '#dcfce7', stroke: '#16a34a', dashed: false },
    { label: 'Changed', fill: '#fef3c7', stroke: '#d97706', dashed: false },
    { label: 'Removed', fill: '#fee2e2', stroke: '#dc2626', dashed: true },
];

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * A Mermaid diagram, drawn with the mermaid library (see mermaid_utils).
 *
 * It opens fitted to its canvas; the zoom buttons scale it from there, and
 * dragging pans it once it outgrows the canvas. The raw source is one click
 * away, and shown outright when mermaid can't parse it.
 */
export default function MermaidDiagram({ source, legend, fill }: MermaidDiagramProps) {
    const dark = useAppThemeIsDark();
    const [drawing, setDrawing] = useState<Drawing | null>(null);
    // Null fits the diagram to the canvas.
    const [zoom, setZoom] = useState<number | null>(null);
    const [showSource, setShowSource] = useState(false);
    const [box, setBox] = useState<Size | null>(null);
    const [copied, setCopied] = useState(false);
    const drag = useRef<{ x: number; y: number; left: number; top: number } | null>(null);

    useEffect(() => {
        let cancelled = false;
        renderMermaid(source, dark).then(
            svg => {
                if (!cancelled) setDrawing({ source, svg, size: svgSize(svg) });
            },
            e => {
                if (!cancelled) setDrawing({ source, error: errorText(e) });
            }
        );
        return () => {
            cancelled = true;
        };
    }, [source, dark]);

    // Tracks the canvas's inner size, which "fit" scales the diagram to.
    const canvasRef = useCallback((el: HTMLDivElement | null) => {
        if (!el || typeof ResizeObserver === 'undefined') return;
        const measure = () =>
            setBox({
                width: Math.max(el.clientWidth - 2 * CANVAS_PADDING, 1),
                height: Math.max(el.clientHeight - 2 * CANVAS_PADDING, 1),
            });
        measure();
        const observer = new ResizeObserver(measure);
        observer.observe(el);
        return () => observer.disconnect();
    }, []);

    // A theme change redraws the same source; the old drawing stays until then.
    const current = drawing?.source === source ? drawing : null;
    const svg = current && current.error === undefined ? current : null;
    const error = current?.error;
    const scale = zoom ?? (svg?.size && box ? fitScale(svg.size, box) : 1);

    const copy = () => {
        navigator.clipboard?.writeText(source).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 2000);
        });
    };

    const canvasStyle: CSSProperties = {
        ...(fill ? { flex: 1, minHeight: 0 } : { height: '60vh', minHeight: '320px' }),
        overflow: 'auto',
        display: 'flex',
        padding: `${CANVAS_PADDING}px`,
        background: 'var(--bg-primary)',
        border: '1px solid var(--border)',
        borderRadius: '6px',
        cursor: 'grab',
        userSelect: 'none',
        touchAction: 'pinch-zoom',
    };

    let content;
    if (showSource || error) {
        content = (
            <pre
                data-testid="mermaid-source"
                style={{
                    ...(fill ? { flex: 1, minHeight: 0 } : { maxHeight: '60vh' }),
                    margin: 0,
                    overflow: 'auto',
                    padding: '12px',
                    fontSize: '12px',
                    background: 'var(--bg-primary)',
                    border: '1px solid var(--border)',
                    borderRadius: '6px',
                }}
            >
                {source}
            </pre>
        );
    } else if (svg) {
        content = (
            <div
                ref={canvasRef}
                className="mermaid-canvas"
                style={canvasStyle}
                onPointerDown={e => {
                    if (e.button !== 0) return;
                    const el = e.currentTarget;
                    drag.current = {
                        x: e.clientX,
                        y: e.clientY,
                        left: el.scrollLeft,
                        top: el.scrollTop,
                    };
                    el.setPointerCapture(e.pointerId);
                }}
                onPointerMove={e => {
                    const d = drag.current;
                    if (!d) return;
                    e.currentTarget.scrollLeft = d.left - (e.clientX - d.x);
                    e.currentTarget.scrollTop = d.top - (e.clientY - d.y);
                }}
                onPointerUp={() => (drag.current = null)}
                onPointerCancel={() => (drag.current = null)}
            >
                {/* Auto margins center a diagram smaller than the canvas and
                    collapse for a larger one, so all of it can be scrolled to. */}
                <div
                    className={svg.size ? 'mermaid-sized' : undefined}
                    style={{
                        margin: 'auto',
                        flex: 'none',
                        ...(svg.size && {
                            width: `${svg.size.width * scale}px`,
                            height: `${svg.size.height * scale}px`,
                        }),
                    }}
                    dangerouslySetInnerHTML={{ __html: svg.svg }}
                />
            </div>
        );
    } else {
        content = (
            <div
                style={{
                    ...(fill ? { flex: 1 } : { minHeight: '120px' }),
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    color: 'var(--text-secondary)',
                    fontStyle: 'italic',
                }}
            >
                Drawing the diagram…
            </div>
        );
    }

    return (
        <div
            data-testid="mermaid-diagram"
            style={{
                display: 'flex',
                flexDirection: 'column',
                gap: '8px',
                ...(fill && { flex: 1, minHeight: 0 }),
            }}
        >
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '6px' }}>
                {svg && !showSource && (
                    <>
                        <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => setZoom(stepZoom(scale, -1))}
                            title="Zoom out"
                            aria-label="Zoom out"
                        >
                            −
                        </Button>
                        <Button
                            size="sm"
                            variant={zoom === null ? 'primary' : 'secondary'}
                            onClick={() => setZoom(null)}
                            title="Fit the diagram to the window"
                        >
                            Fit
                        </Button>
                        <Button
                            size="sm"
                            variant="secondary"
                            onClick={() => setZoom(stepZoom(scale, 1))}
                            title="Zoom in"
                            aria-label="Zoom in"
                        >
                            +
                        </Button>
                        <Button
                            size="sm"
                            variant={zoom === 1 ? 'primary' : 'secondary'}
                            onClick={() => setZoom(1)}
                            title="Draw the diagram at its natural size"
                        >
                            1:1
                        </Button>
                        <span
                            data-testid="mermaid-zoom"
                            style={{
                                fontSize: '12px',
                                color: 'var(--text-secondary)',
                                minWidth: '40px',
                            }}
                        >
                            {Math.round(scale * 100)}%
                        </span>
                    </>
                )}
                {legend && <Legend />}
                <span style={{ flex: 1 }} />
                {!error && (
                    <Button size="sm" variant="secondary" onClick={() => setShowSource(s => !s)}>
                        {showSource ? 'Diagram' : 'Source'}
                    </Button>
                )}
                <Button size="sm" variant="secondary" onClick={copy}>
                    {copied ? '✓ Copied' : 'Copy source'}
                </Button>
            </div>
            {error && (
                <div
                    role="alert"
                    style={{
                        padding: '10px 12px',
                        borderRadius: '6px',
                        fontSize: '13px',
                        background: 'var(--bg-warning-dim)',
                        border: '1px solid var(--border-warning-dim)',
                        whiteSpace: 'pre-wrap',
                    }}
                >
                    Mermaid could not draw this diagram, so here is its source. {error}
                </div>
            )}
            {content}
        </div>
    );
}

function Legend() {
    return (
        <div
            style={{
                display: 'flex',
                gap: '10px',
                marginLeft: '8px',
                fontSize: '12px',
                color: 'var(--text-secondary)',
            }}
        >
            {LEGEND.map(item => (
                <span
                    key={item.label}
                    style={{ display: 'inline-flex', alignItems: 'center', gap: '4px' }}
                >
                    <span
                        aria-hidden="true"
                        style={{
                            width: '12px',
                            height: '12px',
                            borderRadius: '3px',
                            background: item.fill,
                            border: `1.5px ${item.dashed ? 'dashed' : 'solid'} ${item.stroke}`,
                        }}
                    />
                    {item.label}
                </span>
            ))}
        </div>
    );
}

/** Whether the app's theme is dark, kept current as the theme changes. */
function useAppThemeIsDark(): boolean {
    const [dark, setDark] = useState(appThemeIsDark);
    useEffect(() => {
        const observer = new MutationObserver(() => setDark(appThemeIsDark()));
        observer.observe(document.documentElement, {
            attributes: true,
            attributeFilter: ['data-theme'],
        });
        return () => observer.disconnect();
    }, []);
    return dark;
}
