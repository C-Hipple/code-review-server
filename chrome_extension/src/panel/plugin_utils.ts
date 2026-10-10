// The panel's reading of ListPlugins / GetPluginOutput replies: a plugin
// card's status, its button, the body to render, and annotation order.

import type { PluginAnnotation, PluginBody, PluginConfig, PluginOutput } from '../types';
import type { Tone } from './list_utils';

/** A plugin's status for the PR; `none` when it has no entry (never ran). */
export function pluginStatus(output: PluginOutput | null | undefined): string {
    return output ? (output.status || 'none').toLowerCase() : 'none';
}

export function pluginStatusLabel(status: string): string {
    switch (status) {
        case 'none':
            return 'Not run yet';
        case 'deferred':
            return 'On demand';
        case 'pending':
            return 'Running';
        case 'success':
            return 'Success';
        case 'error':
            return 'Failed';
        default:
            return status;
    }
}

export function pluginStatusTone(status: string): Tone {
    switch (status) {
        case 'pending':
            return 'accent';
        case 'success':
            return 'success';
        case 'error':
            return 'danger';
        default:
            return 'neutral';
    }
}

export interface PluginRunAction {
    label: 'Run' | 'Re-run' | 'Running…';
    disabled: boolean;
}

/**
 * **Run** for a plugin that hasn't run (no entry, or `deferred` — on demand
 * and not asked for yet), **Re-run** for one with a result; both are
 * RerunPlugins for that plugin. Nothing to press while it runs.
 */
export function pluginRunAction(output: PluginOutput | null | undefined): PluginRunAction {
    const status = pluginStatus(output);
    if (status === 'pending') return { label: 'Running…', disabled: true };
    if (status === 'none' || status === 'deferred') return { label: 'Run', disabled: false };
    return { label: 'Re-run', disabled: false };
}

/**
 * The plugins Re-run all reruns: the ones that run automatically. They are
 * named rather than left to RerunPlugins' default, which also clears the
 * on-demand plugins' results, so an expensive on-demand run someone asked for
 * keeps its result.
 */
export function automaticPluginNames(plugins: readonly PluginConfig[]): string[] {
    return plugins.filter(p => !p.OnlyOnDemand).map(p => p.Name);
}

export function isPluginPending(output: PluginOutput | null | undefined): boolean {
    return pluginStatus(output) === 'pending';
}

/**
 * The body to render: the parsed body when it has a known type, else the raw
 * stdout as markdown (what a plugin printing plain text produces).
 */
export function resolvePluginBody(output: PluginOutput | null | undefined): PluginBody {
    const type = (output?.body?.body_type || '').toLowerCase();
    if (type === 'markdown' || type === 'html') {
        return { body_type: type, body_content: output?.body?.body_content ?? '' };
    }
    return { body_type: 'markdown', body_content: output?.result ?? '' };
}

/** Annotations by file, then line. */
export function sortAnnotations<T extends PluginAnnotation>(annotations: readonly T[] | null): T[] {
    return [...(annotations ?? [])].sort(
        (a, b) => a.filename.localeCompare(b.filename) || a.line - b.line
    );
}

/** Severity is free-form; unknown values read as neutral. */
export function severityTone(severity: string): Tone {
    switch (severity.toLowerCase()) {
        case 'error':
        case 'critical':
        case 'high':
            return 'danger';
        case 'warning':
        case 'warn':
        case 'medium':
            return 'attention';
        case 'info':
        case 'note':
        case 'low':
            return 'accent';
        default:
            return 'neutral';
    }
}
