// When the PR tools view polls GetAIOutput / GetPluginOutput.

import type { AIFeatureOutput, PluginOutput } from '../types';
import { isAIPending } from './ai_utils';
import { isPluginPending } from './plugin_utils';

export const POLL_INTERVAL_MS = 3000;

/**
 * How long after a run is requested (or the PR opened, which starts its
 * automatic plugins and applied AI features) the view polls even though
 * nothing reads `pending` yet: a run is dispatched in the background, so the
 * first read can come before it is marked.
 */
export const REQUEST_GRACE_MS = 10_000;

export interface PollInputs {
    aiOutputs: Record<string, AIFeatureOutput> | null;
    pluginOutputs: Record<string, PluginOutput> | null;
    /** A run was requested within the last REQUEST_GRACE_MS. */
    justRequested: boolean;
}

/** Whether any AI output or plugin is running. */
export function anythingPending(
    aiOutputs: Record<string, AIFeatureOutput> | null,
    pluginOutputs: Record<string, PluginOutput> | null
): boolean {
    return (
        Object.values(aiOutputs ?? {}).some(isAIPending) ||
        Object.values(pluginOutputs ?? {}).some(isPluginPending)
    );
}

/** Whether to keep polling. */
export function shouldPoll({ aiOutputs, pluginOutputs, justRequested }: PollInputs): boolean {
    return justRequested || anythingPending(aiOutputs, pluginOutputs);
}
