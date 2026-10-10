import type { PullRef } from '../github_url';
import { FILE_ORDERING, type AIFeature, type AIFeatureOutput } from '../types';
import { aiKey, type ActionState } from './actions';
import { AIFeatureCard } from './AIFeatureCard';
import { appliedFeatures, disabledFeatures, reportFeatures } from './ai_utils';
import { ErrorCard } from './ErrorCard';
import { FileOrderingCard } from './FileOrderingCard';
import { SparkleIcon } from './icons';
import type { Resource } from './resource';
import { SkeletonCards } from './Skeleton';

export interface AIFeaturesSectionProps {
    pr: PullRef;
    features: Resource<AIFeature[]>;
    outputs: Resource<Record<string, AIFeatureOutput>>;
    actions: ActionState;
    now: number;
    onRun: (feature: AIFeature, force: boolean) => void;
    onRetryFeatures: () => void;
    onRetryOutputs: () => void;
    /** Card ids to open regardless of what was remembered (tests). */
    expanded?: readonly string[];
}

/**
 * The AI features: a card per enabled report feature, the file-ordering
 * card when that applied feature is on (review-ease shows as the summary's
 * pill instead), and a line naming the ones that aren't enabled.
 */
export function AIFeaturesSection({
    pr,
    features,
    outputs,
    actions,
    now,
    onRun,
    onRetryFeatures,
    onRetryOutputs,
    expanded,
}: AIFeaturesSectionProps) {
    const list = features.value;
    const reports = list ? reportFeatures(list) : [];
    const fileOrdering = list ? appliedFeatures(list).find(f => f.id === FILE_ORDERING) : undefined;
    const disabled = list ? disabledFeatures(list) : [];
    const outputLoading = outputs.value === null && !outputs.error;
    const output = (id: string) => outputs.value?.[id];
    const enabledCount = reports.length + (fileOrdering ? 1 : 0);

    return (
        <section className="section" aria-labelledby="ai-heading">
            <div className="section-header">
                <h2 id="ai-heading" className="section-title">
                    <SparkleIcon />
                    AI features
                </h2>
                {list && <span className="counter">{enabledCount}</span>}
            </div>

            {!list && features.loading && <SkeletonCards count={2} />}
            {features.error && (
                <ErrorCard
                    error={features.error}
                    what="the AI features"
                    onRetry={onRetryFeatures}
                />
            )}
            {outputs.error && (
                <ErrorCard error={outputs.error} what="the AI results" onRetry={onRetryOutputs} />
            )}

            {list && (
                <div className="cards">
                    {reports.map(f => (
                        <AIFeatureCard
                            key={f.id}
                            pr={pr}
                            feature={f}
                            output={output(f.id)}
                            outputLoading={outputLoading}
                            busy={actions.busy[aiKey(f.id)]}
                            error={actions.errors[aiKey(f.id)]}
                            note={actions.notes[aiKey(f.id)]}
                            now={now}
                            onRun={onRun}
                            defaultExpanded={expanded?.includes(f.id)}
                        />
                    ))}
                    {fileOrdering && (
                        <FileOrderingCard
                            pr={pr}
                            feature={fileOrdering}
                            output={output(fileOrdering.id)}
                            outputLoading={outputLoading}
                            now={now}
                        />
                    )}
                </div>
            )}

            {list && enabledCount === 0 && (
                <p className="empty-state">No AI features are enabled for this server.</p>
            )}
            {disabled.length > 0 && (
                <p className="muted small disabled-line">
                    Not enabled: {disabled.map(f => f.name || f.id).join(', ')} — turn them on with{' '}
                    <code>[[AIFeatures]]</code> in the server config.
                </p>
            )}
        </section>
    );
}
