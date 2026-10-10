// What every part of the panel needs to know about where it is running.

import { createContext, useContext } from 'react';
import type { PullRef } from '../github_url';

export interface PanelEnv {
    /** In its own popup window rather than the modal over GitHub. */
    standalone: boolean;
    /** Where links to GitHub open (see linkTarget in params.ts). */
    target: '_top' | '_blank';
    /** The PR the GitHub tab is on, when it is on one. */
    currentPr: PullRef | null;
}

export const PanelContext = createContext<PanelEnv>({
    standalone: false,
    target: '_top',
    currentPr: null,
});

export function usePanel(): PanelEnv {
    return useContext(PanelContext);
}
