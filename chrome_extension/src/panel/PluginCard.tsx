import type { PullRef } from '../github_url';
import type { RpcError } from '../rpc';
import type { PluginConfig, PluginOutput } from '../types';
import { bodyPreview } from './ai_utils';
import { Annotations } from './Annotations';
import { Button } from './Button';
import { PlayIcon, RefreshIcon } from './icons';
import { OutputBody } from './OutputBody';
import {
    pluginRunAction,
    pluginStatus,
    pluginStatusLabel,
    pluginStatusTone,
    resolvePluginBody,
} from './plugin_utils';
import { Pill, StatusChip } from './StatusChip';
import { CardNotice, ToolCard } from './ToolCard';

export interface PluginCardProps {
    pr: PullRef;
    plugin: PluginConfig;
    output: PluginOutput | undefined;
    outputLoading?: boolean;
    busy?: boolean;
    error?: RpcError | null;
    onRun: (name: string) => void;
    defaultExpanded?: boolean;
}

/** One configured plugin's output for the PR, with Run / Re-run. */
export function PluginCard({
    pr,
    plugin,
    output,
    outputLoading = false,
    busy = false,
    error,
    onRun,
    defaultExpanded,
}: PluginCardProps) {
    const status = pluginStatus(output);
    const action = pluginRunAction(output);
    const body = resolvePluginBody(output);
    const annotationCount = output?.annotations?.length ?? 0;
    const hasOutput =
        status !== 'none' &&
        status !== 'deferred' &&
        (!!body.body_content.trim() || annotationCount > 0);

    let summary = '';
    if (status === 'deferred') summary = 'Runs on demand: press Run to run it for this PR.';
    else if (status === 'none') summary = "Hasn't run for this PR yet.";
    else if (hasOutput && body.body_type !== 'html') summary = bodyPreview(body.body_content);

    return (
        <ToolCard
            storageKey={`plugin:${plugin.Name}`}
            title={plugin.Name}
            titleHint={plugin.Command}
            chips={
                outputLoading ? (
                    <StatusChip tone="neutral" label="Loading" spinning />
                ) : (
                    <>
                        <StatusChip
                            tone={pluginStatusTone(status)}
                            label={pluginStatusLabel(status)}
                            spinning={status === 'pending'}
                        />
                        {annotationCount > 0 && (
                            <Pill>
                                {annotationCount}{' '}
                                {annotationCount === 1 ? 'annotation' : 'annotations'}
                            </Pill>
                        )}
                    </>
                )
            }
            actions={
                <Button
                    size="sm"
                    variant={action.label === 'Run' ? 'primary' : 'default'}
                    icon={action.label === 'Re-run' ? <RefreshIcon /> : <PlayIcon />}
                    busy={busy || action.disabled}
                    disabled={outputLoading}
                    onClick={() => onRun(plugin.Name)}
                    aria-label={`${action.label.replace('…', '')} ${plugin.Name}`}
                >
                    {action.label}
                </Button>
            }
            summary={summary || undefined}
            notice={
                error ? (
                    <CardNotice tone="danger">Couldn&apos;t run it: {error.message}</CardNotice>
                ) : null
            }
            expandable={hasOutput}
            defaultExpanded={defaultExpanded}
        >
            {/* An annotations-only plugin has an empty body by design. */}
            {(body.body_content.trim() || annotationCount === 0) && <OutputBody body={body} />}
            {output && <Annotations pr={pr} annotations={output.annotations} />}
        </ToolCard>
    );
}
