import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { PullRef } from '../github_url';
import type { ReviewItem } from '../types';
import { PanelContext, type PanelEnv } from './context';
import { Header } from './Header';
import { findItem } from './list_utils';
import { linkTarget, requestClose, type PanelParams } from './params';
import { PRList, useReviewList } from './PRList';
import { PRTools } from './PRTools';
import { useHostStatus } from './useHostStatus';

export type Route = { view: 'list' } | { view: 'pr'; pr: PullRef };

export function initialRoute(params: PanelParams): Route {
    return params.pr ? { view: 'pr', pr: params.pr } : { view: 'list' };
}

function extensionId(): string | undefined {
    return typeof chrome !== 'undefined' ? chrome.runtime?.id : undefined;
}

export function App({ params }: { params: PanelParams }) {
    const [route, setRoute] = useState<Route>(() => initialRoute(params));
    const [status, refreshStatus] = useHostStatus();
    const reviews = useReviewList(route.view === 'list', refreshStatus);
    const main = useRef<HTMLElement>(null);
    const listScroll = useRef(0);
    const firstRoute = useRef(true);

    const env = useMemo<PanelEnv>(
        () => ({
            standalone: params.standalone,
            target: linkTarget(params),
            currentPr: params.pr,
        }),
        [params]
    );

    // Esc inside the iframe closes the modal, as Esc on the page does.
    useEffect(() => {
        if (params.standalone) return;
        const onKey = (e: KeyboardEvent) => {
            if (e.key === 'Escape' && !e.defaultPrevented) requestClose();
        };
        window.addEventListener('keydown', onKey);
        return () => window.removeEventListener('keydown', onKey);
    }, [params.standalone]);

    // On a route change: put the list back where it was scrolled to, and
    // move focus to the new view's heading for keyboard and screen-reader users.
    useLayoutEffect(() => {
        const el = main.current;
        if (!el) return;
        el.scrollTop = route.view === 'list' ? listScroll.current : 0;
        if (firstRoute.current) {
            firstRoute.current = false;
            return;
        }
        // The PR view's title arrives with GetPR; until then, the view itself.
        (el.querySelector<HTMLElement>('h1') ?? el).focus({ preventScroll: true });
    }, [route]);

    const openTools = (item: ReviewItem) => {
        listScroll.current = main.current?.scrollTop ?? 0;
        setRoute({ view: 'pr', pr: { owner: item.owner, repo: item.repo, number: item.number } });
    };

    const pr = route.view === 'pr' ? route.pr : null;
    const id = extensionId();

    return (
        <PanelContext.Provider value={env}>
            <div className="panel">
                <Header
                    pr={pr}
                    status={status}
                    standalone={params.standalone}
                    onBack={() => setRoute({ view: 'list' })}
                    onClose={requestClose}
                />
                <main className="content" ref={main} tabIndex={-1}>
                    {pr ? (
                        <PRTools
                            key={`${pr.owner}/${pr.repo}#${pr.number}`}
                            pr={pr}
                            listItem={findItem(reviews.list.value, pr)}
                            extensionId={id}
                            onActivity={refreshStatus}
                        />
                    ) : (
                        <PRList state={reviews} onOpenTools={openTools} extensionId={id} />
                    )}
                </main>
            </div>
        </PanelContext.Provider>
    );
}
