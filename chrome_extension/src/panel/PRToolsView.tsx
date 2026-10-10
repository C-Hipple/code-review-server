import type { PullRef } from '../github_url';
import type {
    AIFeature,
    AIFeatureOutput,
    PluginConfig,
    PluginOutput,
    PRPayload,
    ReviewItem,
} from '../types';
import { SYNC, type ActionState } from './actions';
import { AIFeaturesSection } from './AIFeaturesSection';
import { reviewEaseOf } from './ai_utils';
import { ErrorCard } from './ErrorCard';
import { PluginsSection } from './PluginsSection';
import { PRSummary } from './PRSummary';
import { fatalError, type Resource } from './resource';

export interface PRToolsData {
    payload: Resource<PRPayload>;
    features: Resource<AIFeature[]>;
    plugins: Resource<PluginConfig[]>;
    aiOutputs: Resource<Record<string, AIFeatureOutput>>;
    pluginOutputs: Resource<Record<string, PluginOutput>>;
}

export interface PRToolsHandlers {
    /** Reload everything (after a connection-level failure). */
    onRetryAll: () => void;
    onRetryPR: () => void;
    onRetryFeatures: () => void;
    onRetryPlugins: () => void;
    onRetryOutputs: () => void;
    onSync: () => void;
    onRunFeature: (feature: AIFeature, force: boolean) => void;
    onRunPlugin: (name: string) => void;
    onRerunAll: () => void;
}

export interface PRToolsViewProps {
    pr: PullRef;
    data: PRToolsData;
    actions: ActionState;
    handlers: PRToolsHandlers;
    now: number;
    listItem?: ReviewItem | null;
    extensionId?: string;
    /** Cards to open regardless of what was remembered (tests). */
    expanded?: { ai?: readonly string[]; plugins?: readonly string[] };
}

/** The PR tools view as drawn from what has been fetched; PRTools does the fetching. */
export function PRToolsView({
    pr,
    data,
    actions,
    handlers,
    now,
    listItem,
    extensionId,
    expanded,
}: PRToolsViewProps) {
    const fatal = fatalError(
        data.payload,
        data.features,
        data.plugins,
        data.aiOutputs,
        data.pluginOutputs
    );
    if (fatal) {
        return (
            <ErrorCard
                variant="panel"
                error={fatal}
                extensionId={extensionId}
                onRetry={handlers.onRetryAll}
            />
        );
    }

    return (
        <div className="pr-tools">
            <PRSummary
                pr={pr}
                payload={data.payload}
                ease={reviewEaseOf(data.payload.value?.metadata, data.aiOutputs.value)}
                listItem={listItem}
                syncing={actions.busy[SYNC]}
                syncError={actions.errors[SYNC]}
                syncNote={actions.notes[SYNC]}
                onSync={handlers.onSync}
                onRetry={handlers.onRetryPR}
            />
            <AIFeaturesSection
                pr={pr}
                features={data.features}
                outputs={data.aiOutputs}
                actions={actions}
                now={now}
                onRun={handlers.onRunFeature}
                onRetryFeatures={handlers.onRetryFeatures}
                onRetryOutputs={handlers.onRetryOutputs}
                expanded={expanded?.ai}
            />
            <PluginsSection
                pr={pr}
                plugins={data.plugins}
                outputs={data.pluginOutputs}
                actions={actions}
                onRun={handlers.onRunPlugin}
                onRerunAll={handlers.onRerunAll}
                onRetryPlugins={handlers.onRetryPlugins}
                onRetryOutputs={handlers.onRetryOutputs}
                expanded={expanded?.plugins}
            />
        </div>
    );
}
