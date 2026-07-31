/**
 * Plugin response contract
 *
 * A plugin's stdout may be plain text, or JSON declaring how its body should be
 * rendered plus line-level annotations on the diff (see docs/plugins.md). The
 * server parses that contract and serves both the raw stdout (`result`) and the
 * parsed `body` / `annotations` alongside it. The helpers here are the client's
 * single interpretation of those fields.
 */

import type { StatusVariant } from './design';

export type PluginBodyType = 'markdown' | 'html';

export interface PluginBody {
    body_type: PluginBodyType;
    body_content: string;
}

export interface PluginAnnotation {
    filename: string;
    line: number;
    severity: string;
    content: string;
}

export interface PluginResult {
    /** Raw captured stdout of the plugin, unchanged. */
    result: string;
    status: string;
    /** Parsed body. Absent when talking to a server predating the contract. */
    body?: PluginBody;
    /** Diff annotations declared by the plugin; absent or empty for plain output. */
    annotations?: PluginAnnotation[];
}

/** A plugin annotation as it arrives on a PR payload, tagged with its source plugin. */
export interface PRAnnotation extends PluginAnnotation {
    plugin: string;
}

/**
 * Resolve which body to render for a plugin result.
 *
 * A server that speaks the contract always sends a body, and we trust it — an
 * empty body_content with annotations attached is a legitimate response from an
 * annotations-only plugin. Only when the body is missing or carries an
 * unrecognized type do we fall back to rendering the raw stdout as markdown,
 * which is what clients did before the contract existed.
 */
export function resolvePluginBody(plugin: PluginResult): PluginBody {
    const bodyType = (plugin.body?.body_type || '').toLowerCase();
    if (bodyType === 'markdown' || bodyType === 'html') {
        return { body_type: bodyType, body_content: plugin.body?.body_content ?? '' };
    }
    return { body_type: 'markdown', body_content: plugin.result || '' };
}

/**
 * Annotations of a plugin result in display order: grouped by file, then by
 * ascending line number. Annotations that can't be anchored to a line are
 * dropped by the server, so anything here has a filename and a line.
 */
export function sortedAnnotations(plugin: PluginResult): PluginAnnotation[] {
    return [...(plugin.annotations ?? [])].sort(
        (a, b) => a.filename.localeCompare(b.filename) || a.line - b.line
    );
}

/**
 * Badge variant for an annotation's severity. Severity is free-form in the
 * contract, so unrecognized values render as neutral rather than being hidden.
 */
export function annotationSeverityVariant(severity: string): StatusVariant {
    switch (severity.toLowerCase()) {
        case 'error':
        case 'critical':
        case 'high':
            return 'danger';
        case 'warning':
        case 'warn':
        case 'medium':
            return 'warning';
        case 'info':
        case 'note':
        case 'low':
            return 'info';
        default:
            return 'neutral';
    }
}

/**
 * Whether a plugin can be re-run from the UI.
 *
 * Only plugins that have finished executing offer a re-run: a `deferred`
 * plugin has never run (it gets an Execute button instead) and a `pending`
 * one is still in flight. Everything else — success, error, or a status a
 * newer server invents — has a result on screen worth refreshing.
 */
export function canRerunPlugin(plugin: PluginResult): boolean {
    const status = (plugin.status || '').toLowerCase();
    return status !== '' && status !== 'deferred' && status !== 'pending';
}

/** Total number of annotations across every plugin, for summary counts. */
export function totalAnnotationCount(plugins: Record<string, PluginResult>): number {
    return Object.values(plugins).reduce((sum, p) => sum + (p.annotations?.length ?? 0), 0);
}
