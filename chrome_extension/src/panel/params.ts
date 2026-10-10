// What the panel was opened for, from its own URL: the content script adds
// `?owner=&repo=&number=` on a PR page, and the service worker opens the
// popup window with `?standalone=1`.

import { parsePullUrl, type PullRef } from '../github_url';
import type { PanelToPageMessage } from '../types';

export interface PanelParams {
    /** In its own popup window rather than the modal over GitHub. */
    standalone: boolean;
    /** The PR the GitHub tab is on, when it is on one. */
    pr: PullRef | null;
}

export function readPanelParams(search: string): PanelParams {
    const q = new URLSearchParams(search);
    const owner = q.get('owner');
    const repo = q.get('repo');
    const number = q.get('number');
    // Round-trip through a PR URL so the query gets the same validation a
    // GitHub URL does.
    const pr =
        owner && repo && number
            ? parsePullUrl(
                  `https://github.com/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/pull/${encodeURIComponent(number)}`
              )
            : null;
    return { standalone: q.get('standalone') === '1', pr };
}

/**
 * Where links to GitHub open: the GitHub tab itself when embedded (so the
 * modal's page navigates), a new tab from the popup window.
 */
export function linkTarget(params: PanelParams): '_top' | '_blank' {
    return params.standalone ? '_blank' : '_top';
}

/** Asks the content script to close the modal. Only GitHub's page may receive it. */
export function requestClose(): void {
    const message: PanelToPageMessage = { source: 'crs-panel', type: 'close' };
    window.parent.postMessage(message, 'https://github.com');
}
