import { expect, test, describe } from 'bun:test';
import {
    dockViewer,
    undockTab,
    closeDockedTab,
    dockDiffFile,
    moveTabToPanel,
    toggleSplit,
    findTabPanel,
    allDockedTabs,
} from './dock_utils';
import type { DockState } from './dock_utils';

const viewer = (id: number, filePath = `src/file${id}.ts`, line = 1) => ({
    id,
    filePath,
    line,
    position: { x: 100, y: 100 },
});

const emptyPanel = () => ({ dockedTabs: [], activeTab: 'review' as const });

const makeState = (overrides: Partial<DockState> = {}): DockState => ({
    codeViewers: [],
    split: false,
    left: emptyPanel(),
    right: emptyPanel(),
    ...overrides,
});

describe('dockViewer', () => {
    test('moves a floating viewer to left panel and activates it', () => {
        const state = makeState({ codeViewers: [viewer(1), viewer(2)] });
        const next = dockViewer(state, 1);
        expect(next.codeViewers).toHaveLength(1);
        expect(next.codeViewers[0].id).toBe(2);
        expect(next.left.dockedTabs).toHaveLength(1);
        expect(next.left.dockedTabs[0]).toEqual({
            id: 1,
            filePath: 'src/file1.ts',
            line: 1,
        });
        expect(next.left.activeTab).toBe(1);
    });

    test('can dock to right panel', () => {
        const state = makeState({ codeViewers: [viewer(1)] });
        const next = dockViewer(state, 1, 'right');
        expect(next.right.dockedTabs).toHaveLength(1);
        expect(next.right.activeTab).toBe(1);
        expect(next.left.dockedTabs).toHaveLength(0);
    });

    test('returns state unchanged if viewer id not found', () => {
        const state = makeState({ codeViewers: [viewer(1)] });
        const next = dockViewer(state, 999);
        expect(next).toBe(state);
    });

    test('appends to existing docked tabs', () => {
        const state = makeState({
            codeViewers: [viewer(2)],
            left: { dockedTabs: [{ id: 1, filePath: 'src/file1.ts', line: 1 }], activeTab: 1 },
        });
        const next = dockViewer(state, 2);
        expect(next.left.dockedTabs).toHaveLength(2);
        expect(next.left.dockedTabs[1].id).toBe(2);
        expect(next.left.activeTab).toBe(2);
        expect(next.codeViewers).toHaveLength(0);
    });

    test('strips position from docked tab', () => {
        const state = makeState({ codeViewers: [viewer(1)] });
        const next = dockViewer(state, 1);
        expect(next.left.dockedTabs[0]).toEqual({
            id: 1,
            filePath: 'src/file1.ts',
            line: 1,
        });
        expect('position' in next.left.dockedTabs[0]).toBe(false);
    });
});

describe('undockTab', () => {
    test('moves a docked tab back to floating viewers', () => {
        const state = makeState({
            left: { dockedTabs: [{ id: 1, filePath: 'src/file1.ts', line: 10 }], activeTab: 1 },
        });
        const next = undockTab(state, 1);
        expect(next.left.dockedTabs).toHaveLength(0);
        expect(next.codeViewers).toHaveLength(1);
        expect(next.codeViewers[0].id).toBe(1);
        expect(next.codeViewers[0].filePath).toBe('src/file1.ts');
        expect(next.codeViewers[0].line).toBe(10);
        expect(next.codeViewers[0].position).toEqual({ x: 100, y: 100 });
    });

    test('switches to review tab if undocked tab was active', () => {
        const state = makeState({
            left: {
                dockedTabs: [
                    { id: 1, filePath: 'a.ts', line: 1 },
                    { id: 2, filePath: 'b.ts', line: 1 },
                ],
                activeTab: 1,
            },
        });
        const next = undockTab(state, 1);
        expect(next.left.activeTab).toBe('review');
    });

    test('preserves active tab if undocked tab was not active', () => {
        const state = makeState({
            left: {
                dockedTabs: [
                    { id: 1, filePath: 'a.ts', line: 1 },
                    { id: 2, filePath: 'b.ts', line: 1 },
                ],
                activeTab: 2,
            },
        });
        const next = undockTab(state, 1);
        expect(next.left.activeTab).toBe(2);
    });

    test('returns state unchanged if tab id not found', () => {
        const state = makeState({
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = undockTab(state, 999);
        expect(next).toBe(state);
    });

    test('uses custom float position', () => {
        const state = makeState({
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = undockTab(state, 1, { x: 200, y: 300 });
        expect(next.codeViewers[0].position).toEqual({ x: 200, y: 300 });
    });

    test('appends to existing floating viewers', () => {
        const state = makeState({
            codeViewers: [viewer(2)],
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = undockTab(state, 1);
        expect(next.codeViewers).toHaveLength(2);
        expect(next.codeViewers[1].id).toBe(1);
    });

    test('undocks from right panel', () => {
        const state = makeState({
            split: true,
            right: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = undockTab(state, 1);
        expect(next.right.dockedTabs).toHaveLength(0);
        expect(next.codeViewers).toHaveLength(1);
        expect(next.right.activeTab).toBe('review');
    });
});

describe('closeDockedTab', () => {
    test('removes a docked tab', () => {
        const state = makeState({
            left: {
                dockedTabs: [
                    { id: 1, filePath: 'a.ts', line: 1 },
                    { id: 2, filePath: 'b.ts', line: 1 },
                ],
                activeTab: 2,
            },
        });
        const next = closeDockedTab(state, 1);
        expect(next.left.dockedTabs).toHaveLength(1);
        expect(next.left.dockedTabs[0].id).toBe(2);
        expect(next.left.activeTab).toBe(2);
    });

    test('switches to review if closed tab was active', () => {
        const state = makeState({
            codeViewers: [viewer(3)],
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = closeDockedTab(state, 1);
        expect(next.left.dockedTabs).toHaveLength(0);
        expect(next.left.activeTab).toBe('review');
        expect(next.codeViewers).toHaveLength(1);
    });

    test('preserves active tab if closed tab was not active', () => {
        const state = makeState({
            left: {
                dockedTabs: [
                    { id: 1, filePath: 'a.ts', line: 1 },
                    { id: 2, filePath: 'b.ts', line: 1 },
                ],
                activeTab: 'review',
            },
        });
        const next = closeDockedTab(state, 2);
        expect(next.left.dockedTabs).toHaveLength(1);
        expect(next.left.activeTab).toBe('review');
    });

    test('closes tab from right panel', () => {
        const state = makeState({
            split: true,
            right: {
                dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }],
                activeTab: 1,
            },
        });
        const next = closeDockedTab(state, 1);
        expect(next.right.dockedTabs).toHaveLength(0);
        expect(next.right.activeTab).toBe('review');
    });
});

describe('moveTabToPanel', () => {
    test('moves tab from left to right', () => {
        const state = makeState({
            split: true,
            left: {
                dockedTabs: [
                    { id: 1, filePath: 'a.ts', line: 1 },
                    { id: 2, filePath: 'b.ts', line: 1 },
                ],
                activeTab: 1,
            },
        });
        const next = moveTabToPanel(state, 1, 'right');
        expect(next.left.dockedTabs).toHaveLength(1);
        expect(next.left.activeTab).toBe('review');
        expect(next.right.dockedTabs).toHaveLength(1);
        expect(next.right.dockedTabs[0].id).toBe(1);
        expect(next.right.activeTab).toBe(1);
    });

    test('moves tab from right to left', () => {
        const state = makeState({
            split: true,
            right: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = moveTabToPanel(state, 1, 'left');
        expect(next.right.dockedTabs).toHaveLength(0);
        expect(next.left.dockedTabs).toHaveLength(1);
        expect(next.left.activeTab).toBe(1);
    });

    test('returns unchanged if tab already in target panel', () => {
        const state = makeState({
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = moveTabToPanel(state, 1, 'left');
        expect(next).toBe(state);
    });

    test('returns unchanged if tab not found', () => {
        const state = makeState();
        const next = moveTabToPanel(state, 999, 'right');
        expect(next).toBe(state);
    });

    test('preserves source active tab if moved tab was not active', () => {
        const state = makeState({
            split: true,
            left: {
                dockedTabs: [
                    { id: 1, filePath: 'a.ts', line: 1 },
                    { id: 2, filePath: 'b.ts', line: 1 },
                ],
                activeTab: 2,
            },
        });
        const next = moveTabToPanel(state, 1, 'right');
        expect(next.left.activeTab).toBe(2);
    });
});

describe('toggleSplit', () => {
    test('enables split mode', () => {
        const state = makeState();
        const next = toggleSplit(state);
        expect(next.split).toBe(true);
    });

    test('disables split and merges right tabs into left', () => {
        const state = makeState({
            split: true,
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
            right: { dockedTabs: [{ id: 2, filePath: 'b.ts', line: 1 }], activeTab: 2 },
        });
        const next = toggleSplit(state);
        expect(next.split).toBe(false);
        expect(next.left.dockedTabs).toHaveLength(2);
        expect(next.left.activeTab).toBe(1);
        expect(next.right.dockedTabs).toHaveLength(0);
    });

    test('preserves left active tab when merging', () => {
        const state = makeState({
            split: true,
            left: { dockedTabs: [], activeTab: 'review' },
            right: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        const next = toggleSplit(state);
        expect(next.left.activeTab).toBe('review');
        expect(next.left.dockedTabs).toHaveLength(1);
    });
});

describe('findTabPanel', () => {
    test('finds tab in left panel', () => {
        const state = makeState({
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        expect(findTabPanel(state, 1)).toBe('left');
    });

    test('finds tab in right panel', () => {
        const state = makeState({
            right: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
        });
        expect(findTabPanel(state, 1)).toBe('right');
    });

    test('returns null if not found', () => {
        const state = makeState();
        expect(findTabPanel(state, 999)).toBeNull();
    });
});

describe('allDockedTabs', () => {
    test('combines tabs from both panels', () => {
        const state = makeState({
            left: { dockedTabs: [{ id: 1, filePath: 'a.ts', line: 1 }], activeTab: 1 },
            right: { dockedTabs: [{ id: 2, filePath: 'b.ts', line: 1 }], activeTab: 2 },
        });
        expect(allDockedTabs(state)).toHaveLength(2);
    });
});

describe('dockDiffFile', () => {
    test('creates a diff tab in the specified panel', () => {
        const state = makeState();
        const next = dockDiffFile(state, 'src/app.ts', 'left');
        expect(next.left.dockedTabs).toHaveLength(1);
        expect(next.left.dockedTabs[0].filePath).toBe('src/app.ts');
        expect(next.left.dockedTabs[0].tabType).toBe('diff');
        expect(next.left.activeTab).toBe(next.left.dockedTabs[0].id);
    });

    test('activates existing diff tab instead of duplicating', () => {
        const state = makeState();
        const first = dockDiffFile(state, 'src/app.ts');
        const tabId = first.left.dockedTabs[0].id;
        // Dock another tab to change activeTab
        const second = dockDiffFile(first, 'src/other.ts');
        expect(second.left.dockedTabs).toHaveLength(2);
        // Try to dock same file again
        const third = dockDiffFile(second, 'src/app.ts');
        expect(third.left.dockedTabs).toHaveLength(2);
        expect(third.left.activeTab).toBe(tabId);
    });

    test('can dock to right panel', () => {
        const state = makeState({ split: true });
        const next = dockDiffFile(state, 'src/app.ts', 'right');
        expect(next.right.dockedTabs).toHaveLength(1);
        expect(next.right.dockedTabs[0].tabType).toBe('diff');
    });

    test('undocking a diff tab closes it instead of floating', () => {
        const state = makeState();
        const docked = dockDiffFile(state, 'src/app.ts');
        const tabId = docked.left.dockedTabs[0].id;
        const undocked = undockTab(docked, tabId);
        expect(undocked.left.dockedTabs).toHaveLength(0);
        expect(undocked.codeViewers).toHaveLength(0); // not floated
    });
});

describe('round-trip: dock then undock', () => {
    test('dock followed by undock restores the viewer', () => {
        const initial = makeState({ codeViewers: [viewer(1, 'src/main.ts', 42)] });
        const docked = dockViewer(initial, 1);
        expect(docked.codeViewers).toHaveLength(0);
        expect(docked.left.dockedTabs).toHaveLength(1);

        const undocked = undockTab(docked, 1);
        expect(undocked.codeViewers).toHaveLength(1);
        expect(undocked.left.dockedTabs).toHaveLength(0);
        expect(undocked.codeViewers[0].filePath).toBe('src/main.ts');
        expect(undocked.codeViewers[0].line).toBe(42);
        expect(undocked.left.activeTab).toBe('review');
    });

    test('dock then close removes entirely', () => {
        const initial = makeState({ codeViewers: [viewer(1)] });
        const docked = dockViewer(initial, 1);
        const closed = closeDockedTab(docked, 1);
        expect(closed.codeViewers).toHaveLength(0);
        expect(closed.left.dockedTabs).toHaveLength(0);
        expect(closed.left.activeTab).toBe('review');
    });
});
