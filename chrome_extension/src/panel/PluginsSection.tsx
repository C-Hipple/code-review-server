import type { PullRef } from '../github_url';
import type { PluginConfig, PluginOutput } from '../types';
import { ALL_PLUGINS, pluginKey, type ActionState } from './actions';
import { Button } from './Button';
import { ErrorCard } from './ErrorCard';
import { PlugIcon, RefreshIcon } from './icons';
import { PluginCard } from './PluginCard';
import type { Resource } from './resource';
import { SkeletonCards } from './Skeleton';
import { CardNotice } from './ToolCard';

export interface PluginsSectionProps {
    pr: PullRef;
    plugins: Resource<PluginConfig[]>;
    outputs: Resource<Record<string, PluginOutput>>;
    actions: ActionState;
    onRun: (name: string) => void;
    onRerunAll: () => void;
    onRetryPlugins: () => void;
    onRetryOutputs: () => void;
    /** Plugin names to open regardless of what was remembered (tests). */
    expanded?: readonly string[];
}

/** The configured plugins, each with its output for the PR, and Re-run all. */
export function PluginsSection({
    pr,
    plugins,
    outputs,
    actions,
    onRun,
    onRerunAll,
    onRetryPlugins,
    onRetryOutputs,
    expanded,
}: PluginsSectionProps) {
    const list = plugins.value;
    const outputLoading = outputs.value === null && !outputs.error;
    const allError = actions.errors[ALL_PLUGINS];

    return (
        <section className="section" aria-labelledby="plugins-heading">
            <div className="section-header">
                <h2 id="plugins-heading" className="section-title">
                    <PlugIcon />
                    Plugins
                </h2>
                {list && <span className="counter">{list.length}</span>}
                <span className="spacer" />
                {list && list.length > 0 && (
                    <Button
                        size="sm"
                        icon={<RefreshIcon />}
                        busy={actions.busy[ALL_PLUGINS]}
                        disabled={outputLoading}
                        onClick={onRerunAll}
                    >
                        Re-run all
                    </Button>
                )}
            </div>
            {allError && (
                <CardNotice tone="danger">Couldn&apos;t re-run the plugins: {allError.message}</CardNotice>
            )}

            {!list && plugins.loading && <SkeletonCards count={2} />}
            {plugins.error && (
                <ErrorCard error={plugins.error} what="the plugins" onRetry={onRetryPlugins} />
            )}
            {outputs.error && (
                <ErrorCard error={outputs.error} what="the plugin output" onRetry={onRetryOutputs} />
            )}

            {list && list.length === 0 && (
                <p className="empty-state">
                    No plugins configured. Add <code>[[Plugins]]</code> entries to the server config
                    (<code>~/.config/codereviewserver.toml</code>) to run your own checks on each PR.
                </p>
            )}
            {list && list.length > 0 && (
                <div className="cards">
                    {list.map(p => (
                        <PluginCard
                            key={p.Name}
                            pr={pr}
                            plugin={p}
                            output={outputs.value?.[p.Name]}
                            outputLoading={outputLoading}
                            busy={actions.busy[pluginKey(p.Name)]}
                            error={actions.errors[pluginKey(p.Name)]}
                            onRun={onRun}
                            defaultExpanded={expanded?.includes(p.Name)}
                        />
                    ))}
                </div>
            )}
        </section>
    );
}
