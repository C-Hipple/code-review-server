import { getStatusTone } from '../../design';
import { annotationSeverityVariant, type PRAnnotation } from '../../plugin_utils';

interface AnnotationCardProps {
    annotations: PRAnnotation[];
    /**
     * Show each annotation's line number. Set for the file-level card, whose
     * annotations point at lines this diff doesn't render, so the reader can
     * still tell where they land.
     */
    showLines?: boolean;
    /** Pin the card to the visible left edge while the code scrolls on mobile. */
    stickyOnMobile?: boolean;
    onCollapse: () => void;
}

// The expanded form of a line's (or file's) plugin annotations, rendered
// beneath the row it belongs to — the annotation counterpart of CommentThread.
export default function AnnotationCard({
    annotations,
    showLines,
    stickyOnMobile,
    onCollapse,
}: AnnotationCardProps) {
    if (annotations.length === 0) return null;

    return (
        <div
            style={{
                margin: '10px 20px',
                ...(stickyOnMobile
                    ? {
                          position: 'sticky',
                          left: 0,
                          maxWidth: 'calc(100vw - 48px)',
                      }
                    : {}),
                border: '1px solid var(--border)',
                borderRadius: '6px',
                background: 'var(--bg-primary)',
                overflow: 'hidden',
            }}
        >
            <div
                style={{
                    background: 'var(--bg-secondary)',
                    padding: '5px 10px',
                    fontSize: '11px',
                    borderBottom: '1px solid var(--border)',
                    color: 'var(--text-secondary)',
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    gap: '8px',
                }}
            >
                <span>
                    ⚑ {annotations.length} plugin annotation
                    {annotations.length === 1 ? '' : 's'}
                    {showLines && ' outside this diff'}
                </span>
                <button
                    onClick={e => {
                        e.stopPropagation();
                        onCollapse();
                    }}
                    style={{
                        background: 'none',
                        border: 'none',
                        color: 'var(--text-secondary)',
                        cursor: 'pointer',
                        fontSize: '12px',
                        padding: '0 4px',
                        opacity: 0.6,
                        flexShrink: 0,
                    }}
                    title="Collapse annotations"
                >
                    ✕
                </button>
            </div>
            {annotations.map((annotation, i) => {
                const tone = getStatusTone(annotationSeverityVariant(annotation.severity));
                return (
                    <div
                        key={`${annotation.plugin}:${annotation.line}:${i}`}
                        style={{
                            padding: '8px 10px',
                            borderLeft: `3px solid ${tone.fg}`,
                            borderBottom:
                                i === annotations.length - 1 ? 'none' : '1px solid var(--border)',
                        }}
                    >
                        <div
                            style={{
                                display: 'flex',
                                alignItems: 'baseline',
                                flexWrap: 'wrap',
                                gap: '8px',
                                fontSize: '11px',
                                marginBottom: '4px',
                                color: 'var(--text-secondary)',
                            }}
                        >
                            {annotation.severity && (
                                <span
                                    style={{
                                        color: tone.fg,
                                        background: tone.bg,
                                        border: `1px solid ${tone.border}`,
                                        borderRadius: '10px',
                                        padding: '1px 7px',
                                        fontWeight: 600,
                                        textTransform: 'uppercase',
                                        letterSpacing: '0.03em',
                                    }}
                                >
                                    {annotation.severity}
                                </span>
                            )}
                            <span>via {annotation.plugin}</span>
                            {showLines && (
                                <span
                                    style={{
                                        fontFamily: 'var(--font-mono)',
                                        color: 'var(--accent)',
                                    }}
                                >
                                    line {annotation.line}
                                </span>
                            )}
                        </div>
                        <div
                            style={{
                                whiteSpace: 'pre-wrap',
                                fontSize: '13px',
                                lineHeight: 1.5,
                                color: 'var(--text-primary)',
                                wordBreak: 'break-word',
                            }}
                        >
                            {annotation.content}
                        </div>
                    </div>
                );
            })}
        </div>
    );
}
