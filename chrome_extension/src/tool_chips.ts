// Which chips a PR page draws beside GitHub's tabs: one for each enabled AI
// feature, then one for each configured plugin. Pure, so the service worker
// and its tests share one definition of what the page is offered.

import type { AIFeature, PluginConfig, ToolChip } from './types';

/**
 * The chips for the PR, in the order the server lists them: the enabled AI
 * features (the disabled ones have nothing to show) then the configured
 * plugins. `id` is what the panel brings into view — a feature's id, a
 * plugin's name.
 */
export function chipsFor(features: AIFeature[] | null, plugins: PluginConfig[] | null): ToolChip[] {
    const chips: ToolChip[] = [];
    for (const feature of features ?? []) {
        if (!feature.enabled || !feature.id) continue;
        chips.push({ kind: 'ai', id: feature.id, name: feature.name || feature.id });
    }
    for (const plugin of plugins ?? []) {
        if (!plugin.Name) continue;
        chips.push({ kind: 'plugin', id: plugin.Name, name: plugin.Name });
    }
    return chips;
}
