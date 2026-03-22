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
    /** 'code' = source file via CodeViewerModal, 'diff' = single-file diff view */
    tabType?: 'code' | 'diff';
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
    const tab: DockedTab = { id: Date.now(), filePath, line: 0, tabType: 'diff' };
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
 *  Diff tabs cannot float — they are simply closed instead. */
export function undockTab(
    state: DockState,
    tabId: number,
    floatPosition: { x: number; y: number } = { x: 100, y: 100 }
): DockState {
    const panelId = findTabPanel(state, tabId);
    if (!panelId) return state;
    const panel = state[panelId];
    const tab = panel.dockedTabs.find(t => t.id === tabId)!;
    // Diff tabs can't float — just close them
    if (tab.tabType === 'diff') {
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

/** Close a docked tab. Searches both panels. */
export function closeDockedTab(state: DockState, tabId: number): DockState {
    const panelId = findTabPanel(state, tabId);
    if (!panelId) return state;
    const panel = state[panelId];
    return {
        ...state,
        [panelId]: {
            dockedTabs: panel.dockedTabs.filter(t => t.id !== tabId),
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
            left: {
                dockedTabs: [...state.left.dockedTabs, ...state.right.dockedTabs],
                activeTab: state.left.activeTab,
            },
            right: { dockedTabs: [], activeTab: 'review' },
        };
    }
    return { ...state, split: true };
}
