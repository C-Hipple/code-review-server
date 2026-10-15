import { useMemo } from 'react';
import type { PullRef } from '../github_url';
import type { RpcError } from '../rpc';
import type { ReviewItem } from '../types';
import { Button } from './Button';
import { ErrorCard } from './ErrorCard';
import { RefreshIcon, SearchIcon } from './icons';
import { filterReviews, groupReviews, isItem } from './list_utils';
import { ReviewRow } from './ReviewRow';
import { SkeletonLine } from './Skeleton';

export interface ReviewListProps {
    /** The list, once loaded. */
    items: ReviewItem[] | null;
    loading: boolean;
    /** The last load's failure (inline; a fatal one is the caller's to show). */
    error: RpcError | null;
    filter: string;
    onFilterChange: (filter: string) => void;
    onRefresh: () => void;
    currentPr: PullRef | null;
    now: number;
    onOpenTools: (item: ReviewItem) => void;
}

/** The review list: filter, refresh, and the PRs grouped by section. */
export function ReviewList({
    items,
    loading,
    error,
    filter,
    onFilterChange,
    onRefresh,
    currentPr,
    now,
    onOpenTools,
}: ReviewListProps) {
    const groups = useMemo(
        () => (items ? groupReviews(filterReviews(items, filter)) : []),
        [items, filter]
    );
    const total = items?.length ?? 0;
    const shown = groups.reduce((n, g) => n + g.items.length, 0);

    return (
        <div className="review-list">
            <h1 className="sr-only" tabIndex={-1}>
                Review list
            </h1>
            <div className="toolbar">
                <label className="search">
                    <SearchIcon />
                    <span className="sr-only">Filter pull requests</span>
                    <input
                        type="search"
                        value={filter}
                        placeholder="Filter by title, repo, author or number"
                        onChange={e => onFilterChange(e.target.value)}
                        onKeyDown={e => {
                            // Esc clears a filter before it closes the panel.
                            if (e.key === 'Escape' && filter) {
                                e.stopPropagation();
                                e.preventDefault();
                                onFilterChange('');
                            }
                        }}
                    />
                </label>
                <Button icon={<RefreshIcon />} busy={loading} onClick={onRefresh}>
                    Refresh
                </Button>
            </div>

            {items && (
                <p className="list-summary muted small" aria-live="polite">
                    {filter.trim()
                        ? `${shown} of ${total} ${total === 1 ? 'PR' : 'PRs'} match`
                        : `${total} ${total === 1 ? 'PR' : 'PRs'} in ${groups.length} ${groups.length === 1 ? 'section' : 'sections'}`}
                </p>
            )}

            {error && <ErrorCard error={error} what="the review list" onRetry={onRefresh} />}

            {!items && loading && <ListSkeleton />}

            {items && total === 0 && (
                <div className="empty-state">
                    <strong>No PRs in your review list.</strong>
                    <p>
                        Configure workflows in <code>~/.config/codereviewserver.toml</code> and the
                        server fills this list on its next cycle. The PR tools still work on any PR:
                        open the panel on a pull request page.
                    </p>
                </div>
            )}

            {items && total > 0 && shown === 0 && (
                <p className="empty-state">No PRs match “{filter.trim()}”.</p>
            )}

            {groups.map(group => (
                <section key={group.section} className="group" aria-label={group.section}>
                    <h2 className="group-header">
                        <span className="group-name">{group.section}</span>
                        <span className="counter">{group.items.length}</span>
                    </h2>
                    <ul className="rows">
                        {group.items.map(item => (
                            <ReviewRow
                                key={`${item.owner}/${item.repo}#${item.number}`}
                                item={item}
                                current={isItem(item, currentPr)}
                                now={now}
                                onOpenTools={onOpenTools}
                            />
                        ))}
                    </ul>
                </section>
            ))}
        </div>
    );
}

function ListSkeleton() {
    return (
        <div className="group" aria-busy="true">
            <div className="group-header">
                <SkeletonLine width="140px" height={14} />
            </div>
            <ul className="rows">
                {[0, 1, 2, 3].map(i => (
                    <li key={i} className="row row-skeleton">
                        <SkeletonLine width={`${60 - i * 8}%`} height={14} />
                        <SkeletonLine width="30%" height={10} />
                    </li>
                ))}
            </ul>
            <span className="sr-only">Loading the review list…</span>
        </div>
    );
}
