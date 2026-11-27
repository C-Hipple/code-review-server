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
    dockAIFeature,
    findAITab,
    revealReview,
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

describe('dockAIFeature', () => {
    test('docks the report beside the review, splitting the view', () => {
        const next = dockAIFeature(makeState(), 'comments-addressed');
        expect(next.split).toBe(true);
        expect(next.autoSplit).toBe(true);
        expect(next.left).toEqual(emptyPanel());
        expect(next.right.dockedTabs).toHaveLength(1);
        const tab = next.right.dockedTabs[0];
        expect(tab.tabType).toBe('ai');
        expect(tab.featureId).toBe('comments-addressed');
        expect(next.right.activeTab).toBe(tab.id);
        expect(findAITab(next, 'comments-addressed')).toEqual(tab);
        expect(findAITab(next, 'change-diagram')).toBeNull();
    });

    test('keeps what the left panel shows', () => {
        const state = makeState({
            left: { dockedTabs: [{ id: 1, filePath: 'src/a.ts', line: 3 }], activeTab: 1 },
        });
        const next = dockAIFeature(state, 'comments-addressed');
        expect(next.left).toEqual(state.left);
    });

    test('joins a split the reviewer opened, which stays theirs', () => {
        const state = makeState({
            split: true,
            right: { dockedTabs: [{ id: 1, filePath: 'src/a.ts', line: 3 }], activeTab: 1 },
        });
        const next = dockAIFeature(state, 'change-diagram');
        expect(next.autoSplit).toBeUndefined();
        expect(next.right.dockedTabs).toHaveLength(2);
        expect(next.right.dockedTabs[0].id).toBe(1);
        expect(next.right.activeTab).toBe(next.right.dockedTabs[1].id);
    });

    test('without beside, it is a tab of the single panel', () => {
        const next = dockAIFeature(makeState(), 'comments-addressed', { beside: false });
        expect(next.split).toBe(false);
        expect(next.right.dockedTabs).toHaveLength(0);
        expect(next.left.dockedTabs[0].featureId).toBe('comments-addressed');
        expect(next.left.activeTab).toBe(next.left.dockedTabs[0].id);
    });

    test('brings a docked report to the front rather than docking it twice', () => {
        const docked = dockAIFeature(makeState(), 'comments-addressed');
        const id = docked.right.dockedTabs[0].id;
        const withDiff = dockDiffFile(docked, 'src/app.ts', 'right');
        expect(withDiff.right.activeTab).not.toBe(id);
        const again = dockAIFeature(withDiff, 'comments-addressed');
        expect(again.right.dockedTabs).toHaveLength(2);
        expect(again.right.activeTab).toBe(id);
    });

    test('gives two reports docked in the same millisecond different ids', () => {
        const first = dockAIFeature(makeState(), 'comments-addressed');
        const second = dockAIFeature(first, 'change-diagram');
        const ids = second.right.dockedTabs.map(t => t.id);
        expect(new Set(ids).size).toBe(2);
    });

    test('closing the report closes the split it opened', () => {
        const docked = dockAIFeature(makeState(), 'comments-addressed');
        const closed = closeDockedTab(docked, docked.right.dockedTabs[0].id);
        expect(closed.split).toBe(false);
        expect(closed.autoSplit).toBe(false);
        expect(closed.right).toEqual(emptyPanel());
    });

    test('a split it opened stays while the right panel holds another tab', () => {
        const docked = dockAIFeature(makeState(), 'comments-addressed');
        const withDiff = dockDiffFile(docked, 'src/app.ts', 'right');
        const closed = closeDockedTab(withDiff, docked.right.dockedTabs[0].id);
        expect(closed.split).toBe(true);
        expect(closed.right.dockedTabs).toHaveLength(1);
    });

    test('a split the reviewer opened stays when the report closes', () => {
        const docked = dockAIFeature(makeState({ split: true }), 'comments-addressed');
        const closed = closeDockedTab(docked, docked.right.dockedTabs[0].id);
        expect(closed.split).toBe(true);
        expect(closed.right).toEqual(emptyPanel());
    });

    test("moving the report makes the split the reviewer's", () => {
        const docked = dockAIFeature(makeState(), 'comments-addressed');
        const id = docked.right.dockedTabs[0].id;
        const moved = moveTabToPanel(docked, id, 'left');
        expect(moved.autoSplit).toBe(false);
        // Back again, then closed: the reviewer's split stays open.
        const back = moveTabToPanel(moved, id, 'right');
        expect(closeDockedTab(back, id).split).toBe(true);
    });

    test('undocking the report closes it rather than floating a code viewer', () => {
        const docked = dockAIFeature(makeState(), 'comments-addressed');
        const undocked = undockTab(docked, docked.right.dockedTabs[0].id);
        expect(undocked.codeViewers).toHaveLength(0);
        expect(allDockedTabs(undocked)).toHaveLength(0);
        expect(undocked.split).toBe(false);
    });
});

describe('revealReview', () => {
    test('leaves a view that shows the review alone', () => {
        const docked = dockAIFeature(makeState(), 'comments-addressed');
        const id = docked.right.dockedTabs[0].id;
        expect(revealReview(docked, id, true)).toBe(docked);
    });

    test('shows the review in the other panel, keeping the report in view', () => {
        const state = makeState({
            split: true,
            left: { dockedTabs: [{ id: 1, filePath: 'src/a.ts', line: 3 }], activeTab: 1 },
        });
        const docked = dockAIFeature(state, 'comments-addressed');
        const id = docked.right.dockedTabs[0].id;
        const next = revealReview(docked, id, true);
        expect(next.left.activeTab).toBe('review');
        expect(next.right.activeTab).toBe(id);
    });

    test('in a single panel, switches it to the review', () => {
        const docked = dockAIFeature(makeState(), 'comments-addressed', { beside: false });
        const id = docked.left.dockedTabs[0].id;
        const next = revealReview(docked, id, false);
        expect(next.left.activeTab).toBe('review');
        expect(next.left.dockedTabs).toHaveLength(1);
    });

    test('when the split is not shown, only the left panel counts', () => {
        // A split opened on a wide screen, seen on a phone: only the left
        // panel is drawn, so the right one showing the review doesn't help.
        const state = makeState({
            split: true,
            left: { dockedTabs: [{ id: 1, filePath: 'src/a.ts', line: 3 }], activeTab: 1 },
        });
        const next = revealReview(state, 1, false);
        expect(next.left.activeTab).toBe('review');
    });
});
