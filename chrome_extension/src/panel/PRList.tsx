// The review list's data lives in App (through useReviewList) rather than in
// the view, so going to a PR and Back keeps the loaded list and its filter.

import { useCallback, useEffect, useRef, useState } from 'react';
import { getAllReviews } from '../rpc';
import type { ReviewItem } from '../types';
import { usePanel } from './context';
import { ErrorCard } from './ErrorCard';
import { failed, isFatal, loaded, loading, type Resource } from './resource';
import { ReviewList } from './ReviewList';
import { useNow } from './usePolling';

export interface ReviewListState {
    list: Resource<ReviewItem[]>;
    filter: string;
    setFilter: (filter: string) => void;
    refresh: () => void;
}

/**
 * The review list, fetched the first time `wanted` is true (the list view
 * is shown) and on refresh.
 */
export function useReviewList(wanted: boolean, onActivity?: () => void): ReviewListState {
    const [list, setList] = useState<Resource<ReviewItem[]>>(() => loading());
    const [filter, setFilter] = useState('');
    const requested = useRef(false);
    const generation = useRef(0);
    const activity = useRef(onActivity);
    useEffect(() => {
        activity.current = onActivity;
    });

    const fetchList = useCallback(() => {
        const gen = ++generation.current;
        getAllReviews()
            .then(
                items => gen === generation.current && setList(loaded(items)),
                e => gen === generation.current && setList(prev => failed(e, prev))
            )
            .finally(() => activity.current?.());
    }, []);

    useEffect(() => {
        if (!wanted || requested.current) return;
        requested.current = true;
        fetchList();
    }, [wanted, fetchList]);

    const refresh = useCallback(() => {
        requested.current = true;
        setList(prev => loading(prev));
        fetchList();
    }, [fetchList]);

    return { list, filter, setFilter, refresh };
}

export interface PRListProps {
    state: ReviewListState;
    onOpenTools: (item: ReviewItem) => void;
    extensionId?: string;
}

/** The list view; a connection-level failure takes over the panel. */
export function PRList({ state, onOpenTools, extensionId }: PRListProps) {
    const { currentPr } = usePanel();
    const now = useNow();
    const { list } = state;

    if (list.error && isFatal(list.error.kind)) {
        return (
            <ErrorCard
                variant="panel"
                error={list.error}
                extensionId={extensionId}
                onRetry={state.refresh}
            />
        );
    }

    return (
        <ReviewList
            items={list.value}
            loading={list.loading}
            error={list.error}
            filter={state.filter}
            onFilterChange={state.setFilter}
            onRefresh={state.refresh}
            currentPr={currentPr}
            now={now}
            onOpenTools={onOpenTools}
        />
    );
}
