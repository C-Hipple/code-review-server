import type { PullRef } from '../github_url';
import type { RpcError } from '../rpc';
import type { AIFeature, AIFeatureOutput } from '../types';
import {
    aiRunAction,
    aiStatusLabel,
    aiStatusTone,
    bodyPreview,
    changeDiagramSource,
    hasResult,
    isAIPending,
} from './ai_utils';
import { Annotations } from './Annotations';
import { Button } from './Button';
import { PlayIcon, RefreshIcon } from './icons';
import { MermaidDiagram } from './MermaidDiagram';
import { OutputBody } from './OutputBody';
import { Pill, StatusChip } from './StatusChip';
import { absoluteTime, relativeTime } from './time_utils';
import { CardNotice, ToolCard } from './ToolCard';

export interface AIFeatureCardProps {
    pr: PullRef;
    feature: AIFeature;
    /** The feature's output; undefined until GetAIOutput has answered. */
    output: AIFeatureOutput | undefined;
    /** GetAIOutput hasn't answered yet. */
    outputLoading?: boolean;
    /** A RunAIFeature call for it is in flight. */
    busy?: boolean;
    /** Why the last Run failed. */
    error?: RpcError | null;
    /** What the last Run did, when worth saying ("Already up to date"). */
    note?: string;
    now: number;
    onRun: (feature: AIFeature, force: boolean) => void;
    defaultExpanded?: boolean;
}

/** One AI feature's report: status, Run / Re-run, and the expandable result. */
export function AIFeatureCard({
    pr,
    feature,
    output,
    outputLoading = false,
    busy = false,
    error,
    note,
    now,
    onRun,
    defaultExpanded,
}: AIFeatureCardProps) {
    const status = output?.status ?? 'not-run';
    const action = aiRunAction(output);
    const result = hasResult(output);
    const diagram = changeDiagramSource(output);
    const bodyText = output?.body?.body_content ?? '';
    const expandable = !!output && (result || bodyText.trim() !== '' || !!diagram);
    const updated = output?.updated_at ?? '';

    const chips = outputLoading ? (
        <StatusChip tone="neutral" label="Loading" spinning />
    ) : (
        <>
            <StatusChip
                tone={aiStatusTone(status)}
                label={aiStatusLabel(status)}
                spinning={isAIPending(output)}
            />
            {output?.stale && (
                <Pill
                    tone="attention"
                    title="The PR has changed since this ran: new commits or new discussion"
                >
                    Stale
                </Pill>
            )}
            {output?.truncated && (
                <Pill title="Some of the PR was cut to fit the model's prompt">Truncated</Pill>
            )}
        </>
    );

    let summary: string;
    if (!result) summary = feature.description;
    else if (diagram) summary = 'A diagram of what the PR changes — expand to view.';
    else
        summary =
            output?.body.body_type === 'html'
                ? feature.description
                : bodyPreview(bodyText) || feature.description;

    return (
        <ToolCard
            storageKey={`ai:${feature.id}`}
            title={feature.name || feature.id}
            titleHint={feature.description}
            chips={chips}
            meta={
                updated && (
                    <time dateTime={updated} title={absoluteTime(updated)}>
                        Updated {relativeTime(updated, now)}
                    </time>
                )
            }
            actions={
                <Button
                    size="sm"
                    variant={action.label === 'Run' ? 'primary' : 'default'}
                    icon={action.label === 'Re-run' ? <RefreshIcon /> : <PlayIcon />}
                    busy={busy || action.disabled}
                    disabled={outputLoading}
                    onClick={() => onRun(feature, action.force)}
                    aria-label={`${action.label.replace('…', '')} ${feature.name}`}
                >
                    {action.label}
                </Button>
            }
            summary={summary}
            notice={
                error ? (
                    <CardNotice tone="danger">Couldn&apos;t start the run: {error.message}</CardNotice>
                ) : note ? (
                    <CardNotice tone="muted">{note}</CardNotice>
                ) : null
            }
            expandable={expandable}
            defaultExpanded={defaultExpanded}
        >
            {output && isAIPending(output) && result && (
                <p className="muted small previous-note">
                    Showing the previous result while the new run finishes.
                </p>
            )}
            {output &&
                (diagram ? (
                    <MermaidDiagram source={diagram} />
                ) : (
                    <OutputBody body={output.body} />
                ))}
            {output && <Annotations pr={pr} annotations={output.annotations} />}
        </ToolCard>
    );
}
