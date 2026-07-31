import { Badge } from '../design';
import { annotationSeverityVariant, type PluginAnnotation } from '../plugin_utils';

interface PluginAnnotationsProps {
    annotations: PluginAnnotation[];
    /** Called when an annotation is clicked, e.g. to jump to the line in the diff. */
    onSelect?: (annotation: PluginAnnotation) => void;
}

/**
 * Lists a plugin's line-level annotations beneath its body — the flat view on
 * the plugin output surfaces. The review view also renders them inline in the
 * diff (see annotation_utils and DiffView).
 */
export default function PluginAnnotations({ annotations, onSelect }: PluginAnnotationsProps) {
    if (annotations.length === 0) return null;

    return (
        <div
            style={{
                borderTop: '1px solid var(--border)',
                padding: '12px 16px',
                display: 'flex',
                flexDirection: 'column',
                gap: '8px',
            }}
        >
            <div
                style={{
                    fontSize: '11px',
                    fontWeight: 600,
                    letterSpacing: '0.04em',
                    textTransform: 'uppercase',
                    color: 'var(--text-secondary)',
                }}
            >
                {annotations.length} annotation{annotations.length === 1 ? '' : 's'}
            </div>
            {annotations.map((annotation, i) => (
                <div
                    key={`${annotation.filename}:${annotation.line}:${i}`}
                    onClick={onSelect ? () => onSelect(annotation) : undefined}
                    style={{
                        display: 'flex',
                        gap: '10px',
                        alignItems: 'baseline',
                        fontSize: '13px',
                        lineHeight: '1.5',
                        cursor: onSelect ? 'pointer' : 'default',
                    }}
                >
                    {annotation.severity && (
                        <Badge
                            variant={annotationSeverityVariant(annotation.severity)}
                            size="sm"
                            pill
                        >
                            {annotation.severity}
                        </Badge>
                    )}
                    <span
                        style={{
                            fontFamily: 'var(--font-mono)',
                            fontSize: '12px',
                            color: 'var(--accent)',
                            whiteSpace: 'nowrap',
                        }}
                    >
                        {annotation.filename}:{annotation.line}
                    </span>
                    <span style={{ color: 'var(--text-primary)', wordBreak: 'break-word' }}>
                        {annotation.content}
                    </span>
                </div>
            ))}
        </div>
    );
}
