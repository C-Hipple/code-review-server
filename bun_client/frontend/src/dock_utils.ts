/**
 * Pure state helpers for docking/undocking code viewer tabs
 * with split-pane support.
 */

export interface CodeViewer {
    id: number;
    filePath: string;
    line: number;
    position?: { x: number; y: number };
}

export interface DockedTab {
    id: number;
    filePath: string;
    line: number;
    /**
     * 'code' = source file via CodeViewerModal, 'diff' = single-file diff
     * view, 'ai' = an AI feature's report (`featureId`), as its modal shows it
     */
    tabType?: 'code' | 'diff' | 'ai';
    /** The AI feature an 'ai' tab shows. */
    featureId?: string;
}

export type ActiveTab = 'review' | number;
export type PanelId = 'left' | 'right';

export interface PanelState {
    dockedTabs: DockedTab[];
    activeTab: ActiveTab;
}

export interface DockState {
    codeViewers: CodeViewer[];
    split: boolean;
    left: PanelState;
    right: PanelState;
    /**
     * The split was opened by docking an AI report beside the review, not by
     * the reviewer: closing the right panel's last tab closes it again.
     */
    autoSplit?: boolean;
}

/** Get all docked tabs across both panels. */
export function allDockedTabs(state: DockState): DockedTab[] {
    return [...state.left.dockedTabs, ...state.right.dockedTabs];
}

/** Find which panel owns a tab. */
export function findTabPanel(state: DockState, tabId: number): PanelId | null {
    if (state.left.dockedTabs.some(t => t.id === tabId)) return 'left';
    if (state.right.dockedTabs.some(t => t.id === tabId)) return 'right';
    return null;
}

/** An id for a new tab: the time, unless a tab docked this millisecond has it. */
function newTabId(state: DockState): number {
    const ids = allDockedTabs(state).map(t => t.id);
    const now = Date.now();
    return ids.includes(now) ? Math.max(...ids) + 1 : now;
}

/** The tab showing an AI feature's report, if it is docked. */
export function findAITab(state: DockState, featureId: string): DockedTab | null {
    return allDockedTabs(state).find(t => t.tabType === 'ai' && t.featureId === featureId) ?? null;
}

/**
 * Dock an AI feature's report as a tab. With `beside`, it goes in the right
 * panel and the view splits, so the report sits beside the code the left
 * panel shows; without (on a phone, where the view never splits), it's a tab
 * of the single panel. A report already docked is brought to the front of
 * its panel instead.
 */
export function dockAIFeature(
    state: DockState,
    featureId: string,
    { beside = true }: { beside?: boolean } = {}
): DockState {
    const existing = findAITab(state, featureId);
    if (existing) {
        const ownerPanel = findTabPanel(state, existing.id)!;
        return { ...state, [ownerPanel]: { ...state[ownerPanel], activeTab: existing.id } };
    }
    const tab: DockedTab = {
        id: newTabId(state),
        filePath: '',
        line: 0,
        tabType: 'ai',
        featureId,
    };
    const panel: PanelId = beside ? 'right' : 'left';
    const target = state[panel];
    return {
        ...state,
        ...(beside && !state.split ? { split: true, autoSplit: true } : {}),
        [panel]: {
            dockedTabs: [...target.dockedTabs, tab],
            activeTab: tab.id,
        },
    };
}

/**
 * Make sure a panel on screen shows the review, as jumping to a thread from
 * a docked tab needs, keeping `keepTab` (the tab jumped from) in view where
 * the split leaves room for both. `split` is whether the view shows both
 * panels now.
 */
export function revealReview(state: DockState, keepTab: number, split: boolean): DockState {
    const shown: PanelId[] = split ? ['left', 'right'] : ['left'];
    if (shown.some(p => state[p].activeTab === 'review')) return state;
    const target = shown.find(p => findTabPanel(state, keepTab) !== p) ?? 'left';
    return { ...state, [target]: { ...state[target], activeTab: 'review' } };
}

/** Dock a floating code viewer as a tab in a specific panel. */
export function dockViewer(state: DockState, viewerId: number, panel: PanelId = 'left'): DockState {
    const viewer = state.codeViewers.find(v => v.id === viewerId);
    if (!viewer) return state;
    const tab: DockedTab = { id: viewer.id, filePath: viewer.filePath, line: viewer.line };
    const targetPanel = state[panel];
    return {
        ...state,
        codeViewers: state.codeViewers.filter(v => v.id !== viewerId),
        [panel]: {
            dockedTabs: [...targetPanel.dockedTabs, tab],
            activeTab: viewer.id,
        },
    };
}

/** Dock a diff file directly as a tab (no floating viewer). */
export function dockDiffFile(
    state: DockState,
    filePath: string,
    panel: PanelId = 'left'
): DockState {
    // Don't dock the same file twice
    const all = allDockedTabs(state);
    const existing = all.find(t => t.tabType === 'diff' && t.filePath === filePath);
    if (existing) {
        // Just activate the existing tab
        const ownerPanel = findTabPanel(state, existing.id);
        if (ownerPanel) {
            return { ...state, [ownerPanel]: { ...state[ownerPanel], activeTab: existing.id } };
        }
    }
    const tab: DockedTab = { id: newTabId(state), filePath, line: 0, tabType: 'diff' };
    const targetPanel = state[panel];
    return {
        ...state,
        [panel]: {
            dockedTabs: [...targetPanel.dockedTabs, tab],
            activeTab: tab.id,
        },
    };
}

/** Undock a tab back to a floating modal. Searches both panels.
 *  Only code tabs float — diff and AI tabs are simply closed instead. */
export function undockTab(
    state: DockState,
    tabId: number,
    floatPosition: { x: number; y: number } = { x: 100, y: 100 }
): DockState {
    const panelId = findTabPanel(state, tabId);
    if (!panelId) return state;
    const panel = state[panelId];
    const tab = panel.dockedTabs.find(t => t.id === tabId)!;
    // Diff and AI tabs can't float as a code viewer — just close them
    if (tab.tabType === 'diff' || tab.tabType === 'ai') {
        return closeDockedTab(state, tabId);
    }
    return {
        ...state,
        codeViewers: [
            ...state.codeViewers,
            { id: tab.id, filePath: tab.filePath, line: tab.line, position: floatPosition },
        ],
        [panelId]: {
            dockedTabs: panel.dockedTabs.filter(t => t.id !== tabId),
            activeTab: panel.activeTab === tabId ? 'review' : panel.activeTab,
        },
    };
}

/** Close a docked tab. Searches both panels. A split that docking an AI
 *  report opened closes with the right panel's last tab. */
export function closeDockedTab(state: DockState, tabId: number): DockState {
    const panelId = findTabPanel(state, tabId);
    if (!panelId) return state;
    const panel = state[panelId];
    const dockedTabs = panel.dockedTabs.filter(t => t.id !== tabId);
    if (state.autoSplit && panelId === 'right' && dockedTabs.length === 0) {
        return {
            ...state,
            split: false,
            autoSplit: false,
            right: { dockedTabs: [], activeTab: 'review' },
        };
    }
    return {
        ...state,
        [panelId]: {
            dockedTabs,
            activeTab: panel.activeTab === tabId ? 'review' : panel.activeTab,
        },
    };
}

/** Move a tab from one panel to another. */
export function moveTabToPanel(state: DockState, tabId: number, targetPanel: PanelId): DockState {
    const sourcePanel = findTabPanel(state, tabId);
    if (!sourcePanel || sourcePanel === targetPanel) return state;
    const source = state[sourcePanel];
    const target = state[targetPanel];
    const tab = source.dockedTabs.find(t => t.id === tabId)!;
    return {
        ...state,
        // The reviewer has arranged the panels: the split is theirs now.
        autoSplit: false,
        [sourcePanel]: {
            dockedTabs: source.dockedTabs.filter(t => t.id !== tabId),
            activeTab: source.activeTab === tabId ? 'review' : source.activeTab,
        },
        [targetPanel]: {
            dockedTabs: [...target.dockedTabs, tab],
            activeTab: tab.id,
        },
    };
}

/** Toggle split mode. When turning off, merges right tabs into left. */
export function toggleSplit(state: DockState): DockState {
    if (state.split) {
        // Merge right into left
        return {
            ...state,
            split: false,
            autoSplit: false,
            left: {
                dockedTabs: [...state.left.dockedTabs, ...state.right.dockedTabs],
                activeTab: state.left.activeTab,
            },
            right: { dockedTabs: [], activeTab: 'review' },
        };
    }
    return { ...state, split: true, autoSplit: false };
}
