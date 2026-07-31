/**
 * Anchoring plugin annotations to the rendered diff.
 *
 * Plugins report annotations as a filename plus a 1-based line number (see
 * docs/plugins.md). The diff view renders rows, not files, so the helpers here
 * turn a flat annotation list into buckets keyed by the row each annotation
 * belongs to — one bucket per commented line, plus a per-file bucket for
 * annotations pointing at lines the diff doesn't show.
 */

import type { ParsedLine } from './diff_utils';
import { annotationSeverityVariant, type PluginResult, type PRAnnotation } from './plugin_utils';
import type { StatusVariant } from './design';

/** Key for the annotations anchored to one line of one file. */
export const lineAnnotationKey = (file: string, line: number): string => `${file}:L${line}`;

/** Key for the annotations attached to a file as a whole. */
export const fileAnnotationKey = (file: string): string => `${file}:F`;

/**
 * Plugins are expected to report repo-relative paths, the same ones the diff
 * carries. A leading `./` is the one spelling difference common enough to be
 * worth absorbing; anything else has to match the diff exactly.
 */
const normalizePath = (path: string): string => path.replace(/^\.\//, '');

export interface AnnotationIndex {
    /** Annotations anchored to a rendered line, keyed by `lineAnnotationKey`. */
    byLine: Map<string, PRAnnotation[]>;
    /**
     * Annotations for a file in the diff whose line isn't rendered (an
     * unchanged region, or a line that only exists on the base side). They hang
     * off the file header instead of vanishing, keyed by `fileAnnotationKey`.
     */
    byFile: Map<string, PRAnnotation[]>;
    /** Every key in the index, in file-then-line order, for expand/collapse all. */
    keys: string[];
    /** How many annotations the index placed. */
    count: number;
}

export const emptyAnnotationIndex: AnnotationIndex = {
    byLine: new Map(),
    byFile: new Map(),
    keys: [],
    count: 0,
};

/**
 * PR-wide annotations from the loaded plugin results, each tagged with its
 * plugin and ordered by plugin name.
 *
 * The server sends the same aggregate on the PR payload, but deriving it from
 * the plugin outputs keeps the diff in sync when a plugin is rerun from the
 * plugins drawer, which refreshes those outputs without reloading the PR.
 * Only successful runs contribute, matching the server's aggregation.
 */
export function collectPRAnnotations(plugins: Record<string, PluginResult>): PRAnnotation[] {
    return Object.keys(plugins)
        .sort()
        .filter(name => (plugins[name].status || '').toLowerCase() === 'success')
        .flatMap(name =>
            (plugins[name].annotations ?? []).map(annotation => ({
                ...annotation,
                plugin: name,
            }))
        );
}

/**
 * Bucket annotations by the diff row they belong to.
 *
 * A line matches when its head-side (new) line number equals the annotation's
 * line, since plugins read the PR's head state. Annotations for a file that
 * isn't in the diff at all are dropped here — there's no row to hang them on —
 * and remain listed in the plugins drawer.
 */
export function indexAnnotations(
    annotations: PRAnnotation[],
    lines: ParsedLine[]
): AnnotationIndex {
    if (annotations.length === 0) return emptyAnnotationIndex;

    const renderedLines = new Set<string>();
    const filesInDiff = new Set<string>();
    for (const line of lines) {
        if (!line.file) continue;
        filesInDiff.add(line.file);
        if (line.newLineNo != null) {
            renderedLines.add(lineAnnotationKey(line.file, line.newLineNo));
        }
    }

    const byLine = new Map<string, PRAnnotation[]>();
    const byFile = new Map<string, PRAnnotation[]>();
    let count = 0;
    for (const annotation of annotations) {
        const file = normalizePath(annotation.filename);
        if (!filesInDiff.has(file)) continue;
        const lineKey = lineAnnotationKey(file, annotation.line);
        const [bucket, key] = renderedLines.has(lineKey)
            ? [byLine, lineKey]
            : [byFile, fileAnnotationKey(file)];
        const existing = bucket.get(key);
        if (existing) existing.push(annotation);
        else bucket.set(key, [annotation]);
        count++;
    }

    // File-level buckets collect annotations from every plugin, so order them by
    // line; the per-line buckets keep the incoming plugin-name order.
    for (const bucket of byFile.values()) {
        bucket.sort((a, b) => a.line - b.line || a.plugin.localeCompare(b.plugin));
    }

    const keys = [...byLine.keys(), ...byFile.keys()].sort();
    return { byLine, byFile, keys, count };
}

const severityRank: Record<StatusVariant, number> = {
    danger: 4,
    warning: 3,
    info: 2,
    success: 1,
    neutral: 0,
};

/**
 * The variant a group of annotations should be badged with: the most severe one
 * present, so a collapsed indicator reflects the worst thing hiding behind it.
 */
export function mostSevereVariant(annotations: PRAnnotation[]): StatusVariant {
    return annotations.reduce<StatusVariant>((worst, annotation) => {
        const variant = annotationSeverityVariant(annotation.severity);
        return severityRank[variant] > severityRank[worst] ? variant : worst;
    }, 'neutral');
}
