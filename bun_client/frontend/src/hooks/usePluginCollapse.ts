import { useState } from 'react';
import { collapsePluginsByDefault, type PluginResult } from '../plugin_utils';

/**
 * Which plugins' output is collapsed on a plugin output surface.
 *
 * A plugin the reviewer hasn't toggled follows the default — collapsed when
 * the output of every plugin together is over the line limit — so output that
 * arrives after the surface opens (the first load, a re-run) still gets it.
 * A toggle sticks for as long as the surface stays open.
 */
export function usePluginCollapse(plugins: Record<string, PluginResult>) {
    const [toggled, setToggled] = useState<Record<string, boolean>>({});
    const collapsedByDefault = collapsePluginsByDefault(plugins);

    const isCollapsed = (name: string) => toggled[name] ?? collapsedByDefault;
    const toggle = (name: string) =>
        setToggled(prev => ({ ...prev, [name]: !(prev[name] ?? collapsedByDefault) }));

    return { isCollapsed, toggle };
}
