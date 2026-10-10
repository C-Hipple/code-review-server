import type { PullRef } from '../github_url';
import type { AIFeature, AIFeatureOutput } from '../types';
import { aiStatusLabel, aiStatusTone, fileOrderingFiles, isAIPending } from './ai_utils';
import { usePanel } from './context';
import { fileHref, useDiffAnchors } from './diff_links';
import { Pill, StatusChip } from './StatusChip';
import { absoluteTime, relativeTime } from './time_utils';
import { ToolCard } from './ToolCard';

export const FILE_ORDERING_NOTE =
    "Applied feature — the suggested order to read this PR's files. A future version of the " +
    "extension will reorder GitHub's Files changed tab to match; for now the order is listed here.";

export interface FileOrderingCardProps {
    pr: PullRef;
    feature: AIFeature;
    output: AIFeatureOutput | undefined;
    outputLoading?: boolean;
    now: number;
}

/**
 * file-ordering, an applied feature: the server orders the diff it serves
 * by it. The extension doesn't reorder GitHub's page yet (see the
 * TODO(file-ordering) hook in content.ts), so this lists the stored order.
 */
export function FileOrderingCard({
    pr,
    feature,
    output,
    outputLoading = false,
    now,
}: FileOrderingCardProps) {
    const files = fileOrderingFiles(output);
    const status = output?.status ?? 'not-run';
    const updated = output?.updated_at ?? '';

    let empty = '';
    if (!files) {
        if (outputLoading) empty = '';
        else if (isAIPending(output)) empty = 'Working out the order…';
        else if (status === 'error' || status === 'insufficient-input')
            empty = output?.body.body_content.trim() || 'The last run produced no order.';
        else empty = 'No order yet: the server works it out when the PR is opened.';
    }

    return (
        <ToolCard
            storageKey={`ai:${feature.id}`}
            title={feature.name || 'File ordering'}
            titleHint={feature.description}
            chips={
                <>
                    <Pill tone="done" title="The server applies this feature's result itself">
                        Applied
                    </Pill>
                    {outputLoading ? (
                        <StatusChip tone="neutral" label="Loading" spinning />
                    ) : (
                        <StatusChip
                            tone={aiStatusTone(status)}
                            label={aiStatusLabel(status)}
                            spinning={isAIPending(output)}
                        />
                    )}
                    {output?.stale && <Pill tone="attention">Stale</Pill>}
                </>
            }
            meta={
                updated && (
                    <time dateTime={updated} title={absoluteTime(updated)}>
                        Updated {relativeTime(updated, now)}
                    </time>
                )
            }
            notice={
                <div className="file-order-note">
                    <p>{FILE_ORDERING_NOTE}</p>
                    {empty && <p className="muted">{empty}</p>}
                </div>
            }
            summary={files ? `${files.length} files in the suggested order.` : undefined}
            expandable={!!files}
            defaultExpanded
        >
            {files && <FileOrderList pr={pr} files={files} />}
        </ToolCard>
    );
}

export function FileOrderList({ pr, files }: { pr: PullRef; files: string[] }) {
    const { target } = usePanel();
    const anchors = useDiffAnchors(files);
    return (
        <ol className="file-order">
            {files.map(path => {
                const slash = path.lastIndexOf('/');
                return (
                    <li key={path}>
                        <a href={fileHref(pr, path, anchors)} target={target} rel="noreferrer">
                            {slash >= 0 && (
                                <span className="file-dir">{path.slice(0, slash + 1)}</span>
                            )}
                            <span className="file-name">{path.slice(slash + 1)}</span>
                        </a>
                    </li>
                );
            })}
        </ol>
    );
}
