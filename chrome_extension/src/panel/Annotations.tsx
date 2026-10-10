import type { PullRef } from '../github_url';
import type { PluginAnnotation } from '../types';
import { usePanel } from './context';
import { lineHref, useDiffAnchors } from './diff_links';
import { severityTone, sortAnnotations } from './plugin_utils';
import { Pill } from './StatusChip';

/**
 * A plugin's or AI feature's diff annotations, by file and line, each
 * linking to its line in GitHub's Files changed tab.
 */
export function Annotations({
    pr,
    annotations,
}: {
    pr: PullRef;
    annotations: readonly PluginAnnotation[] | null;
}) {
    const { target } = usePanel();
    const sorted = sortAnnotations(annotations);
    const anchors = useDiffAnchors(sorted.map(a => a.filename));
    if (sorted.length === 0) return null;
    return (
        <div className="annotations">
            <h4 className="annotations-title">
                {sorted.length} {sorted.length === 1 ? 'annotation' : 'annotations'}
            </h4>
            <ul className="annotation-list">
                {sorted.map((a, i) => (
                    <li key={`${a.filename}:${a.line}:${i}`} className="annotation">
                        <div className="annotation-head">
                            <a
                                className="annotation-loc"
                                href={lineHref(pr, a.filename, a.line, anchors)}
                                target={target}
                                rel="noreferrer"
                                title={`Open ${a.filename} line ${a.line} in the diff`}
                            >
                                {a.filename}
                                <span className="annotation-line">:{a.line}</span>
                            </a>
                            {a.severity && (
                                <Pill tone={severityTone(a.severity)}>{a.severity}</Pill>
                            )}
                        </div>
                        <p className="annotation-content">{a.content}</p>
                    </li>
                ))}
            </ul>
        </div>
    );
}
