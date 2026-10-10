import { pullUrl, type PullRef } from '../github_url';
import type { RpcError } from '../rpc';
import type { PRMetadata, PRPayload, ReviewEase, ReviewItem } from '../types';
import { Button } from './Button';
import { usePanel } from './context';
import { ErrorCard } from './ErrorCard';
import { ExternalIcon, PullStateIcon, RefreshIcon } from './icons';
import { EASE_TONE, itemState, prLabel, type PRState } from './list_utils';
import type { Resource } from './resource';
import { SkeletonLine } from './Skeleton';
import { Pill } from './StatusChip';
import { CardNotice } from './ToolCard';

export interface PRSummaryProps {
    pr: PullRef;
    payload: Resource<PRPayload>;
    ease: ReviewEase | '';
    /** The PR's review-list entry, when it has one: tells merged from closed. */
    listItem?: ReviewItem | null;
    syncing?: boolean;
    syncError?: RpcError;
    syncNote?: string;
    onSync: () => void;
    onRetry: () => void;
}

const STATE_LABEL: Record<PRState, string> = {
    open: 'Open',
    draft: 'Draft',
    merged: 'Merged',
    closed: 'Closed',
};

/** The PR's state from GetPR's metadata, using the list entry to spot a merge. */
export function metadataState(meta: PRMetadata, listItem?: ReviewItem | null): PRState {
    if (meta.draft) return 'draft';
    if (meta.state === 'closed') {
        return listItem && itemState(listItem) === 'merged' ? 'merged' : 'closed';
    }
    if (meta.state === 'merged') return 'merged';
    return 'open';
}

/** The top of the PR tools view: what PR this is, and Sync. */
export function PRSummary({
    pr,
    payload,
    ease,
    listItem,
    syncing = false,
    syncError,
    syncNote,
    onSync,
    onRetry,
}: PRSummaryProps) {
    const { target } = usePanel();
    const meta = payload.value?.metadata ?? null;
    const url = meta?.url || listItem?.url || pullUrl(pr);

    if (!meta) {
        if (payload.error) {
            return (
                <div className="summary">
                    <p className="summary-ref">{prLabel(pr)}</p>
                    <ErrorCard error={payload.error} what="the pull request" onRetry={onRetry} />
                </div>
            );
        }
        return (
            <div className="summary" aria-busy="true">
                <p className="summary-ref">{prLabel(pr)}</p>
                <SkeletonLine width="70%" height={22} />
                <SkeletonLine width="45%" height={12} />
                <span className="sr-only">Loading the pull request…</span>
            </div>
        );
    }

    const state = metadataState(meta, listItem);
    return (
        <div className="summary">
            <div className="summary-top">
                <span className={`state-badge state-${state}`}>
                    <PullStateIcon state={state} />
                    {STATE_LABEL[state]}
                </span>
                <span className="summary-repo">
                    {pr.owner}/{pr.repo}
                </span>
                <span className="spacer" />
                <div className="summary-actions">
                    <Button size="sm" icon={<RefreshIcon />} busy={syncing} onClick={onSync}>
                        Sync
                    </Button>
                    <a
                        className="btn btn-default btn-sm"
                        href={url}
                        target={target}
                        rel="noreferrer"
                    >
                        <ExternalIcon />
                        GitHub
                    </a>
                </div>
            </div>
            <h1 className="summary-title" tabIndex={-1}>
                {meta.title || listItem?.title || prLabel(pr)}{' '}
                <span className="summary-number">#{pr.number}</span>
            </h1>
            <p className="summary-meta">
                {meta.author && <strong>{meta.author}</strong>}
                {meta.head_ref && meta.base_ref && (
                    <>
                        <span className="muted"> wants to merge </span>
                        <code className="branch" title={meta.head_ref}>
                            {meta.head_ref}
                        </code>
                        <span className="muted"> into </span>
                        <code className="branch" title={meta.base_ref}>
                            {meta.base_ref}
                        </code>
                    </>
                )}
            </p>
            <div className="summary-stats">
                <span className="diffstat" title="Lines added and removed">
                    <span className="added">+{meta.additions.toLocaleString()}</span>
                    <span className="removed">−{meta.deletions.toLocaleString()}</span>
                </span>
                <span className="muted">
                    {meta.changed_files} {meta.changed_files === 1 ? 'file' : 'files'} changed
                </span>
                {ease && (
                    <Pill tone={EASE_TONE[ease]} title="Rated by the review-ease AI feature">
                        {ease === 'easy' ? 'Easy' : ease === 'medium' ? 'Medium' : 'Hard'} to review
                    </Pill>
                )}
                {listItem?.merge_conflicts && <Pill tone="danger">Conflict</Pill>}
            </div>
            {syncError && (
                <CardNotice tone="danger">Couldn&apos;t sync: {syncError.message}</CardNotice>
            )}
            {syncNote && !syncError && <CardNotice tone="muted">{syncNote}</CardNotice>}
            {payload.error && (
                <ErrorCard error={payload.error} what="the pull request" onRetry={onRetry} />
            )}
        </div>
    );
}
