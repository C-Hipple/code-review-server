import { useState } from 'react';
import { getStatusTone, shadows } from '../../design';
import { mostSevereVariant } from '../../annotation_utils';
import { annotationSeverityVariant, type PRAnnotation } from '../../plugin_utils';

interface AnnotationIndicatorProps {
    annotations: PRAnnotation[];
    visible: boolean;
    /**
     * Which edge the hover preview is pinned to. Badges at the right end of a
     * diff line need 'right', or the preview opens off the side of the page.
     */
    align?: 'left' | 'right';
    onToggle: () => void;
}

// Badge shown in a diff line's gutter (or on a file header) when plugins have
// annotated it. Annotations are collapsed by default, like comment threads:
// hovering previews them read-only, clicking expands the card below the line.
// The badge takes the colour of the most severe annotation behind it.
export default function AnnotationIndicator({
    annotations,
    visible,
    align = 'left',
    onToggle,
}: AnnotationIndicatorProps) {
    const [hovered, setHovered] = useState(false);
    const tone = getStatusTone(mostSevereVariant(annotations));

    return (
        <span
            onMouseEnter={() => setHovered(true)}
            onMouseLeave={() => setHovered(false)}
            onClick={e => {
                e.stopPropagation();
                onToggle();
            }}
            style={{
                position: 'relative',
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                gap: '2px',
                minWidth: '20px',
                height: '18px',
                padding: '0 5px',
                borderRadius: '9px',
                background: visible ? tone.fg : tone.bg,
                color: visible ? 'var(--bg-primary)' : tone.fg,
                border: `1px solid ${visible ? tone.fg : tone.border}`,
                fontSize: '10px',
                fontWeight: 600,
                cursor: 'pointer',
                userSelect: 'none',
            }}
            title={
                visible
                    ? 'Hide plugin annotations'
                    : `Click to show ${annotations.length} plugin annotation${
                          annotations.length === 1 ? '' : 's'
                      }`
            }
        >
            <span>⚑</span>
            {annotations.length > 1 && <span>{annotations.length}</span>}
            {hovered && !visible && (
                <div
                    onClick={e => e.stopPropagation()}
                    style={{
                        position: 'absolute',
                        bottom: '100%',
                        ...(align === 'right' ? { right: 0 } : { left: 0 }),
                        marginBottom: '6px',
                        background: 'var(--bg-secondary)',
                        border: '1px solid var(--border)',
                        borderRadius: '6px',
                        padding: '8px',
                        width: '320px',
                        maxWidth: '80vw',
                        maxHeight: '260px',
                        overflowY: 'auto',
                        boxShadow: shadows.md,
                        zIndex: 100,
                        fontSize: '12px',
                        fontWeight: 400,
                        color: 'var(--text-primary)',
                        cursor: 'default',
                        textAlign: 'left',
                        whiteSpace: 'normal',
                    }}
                >
                    {annotations.map((annotation, i) => (
                        <div
                            key={`${annotation.plugin}:${annotation.line}:${i}`}
                            style={{ marginBottom: '6px' }}
                        >
                            <div
                                style={{
                                    color: getStatusTone(
                                        annotationSeverityVariant(annotation.severity)
                                    ).fg,
                                    fontSize: '11px',
                                    marginBottom: '2px',
                                }}
                            >
                                {annotation.plugin}
                                {annotation.severity ? ` · ${annotation.severity}` : ''}
                            </div>
                            <div style={{ whiteSpace: 'pre-wrap' }}>{annotation.content}</div>
                        </div>
                    ))}
                </div>
            )}
        </span>
    );
}
