import { expect, test, describe } from 'bun:test';
import { parseDiff } from './diff_utils';

// Regression test for comments disappearing after creation.
//
// Hunk expansion splices extra context lines into the displayed diff. Those
// lines are NOT part of the PR's canonical diff, so they must not consume a
// GitHub comment "position". If they do, every line below an expansion shifts,
// and a comment created while a hunk is expanded is stored at a position that
// no longer matches any line once the canonical diff is restored — so it
// silently vanishes from the UI.

const baseDiff = `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,4 +1,4 @@
 package main
-import "old"
+import "new"
 func main() {
@@ -20,3 +20,4 @@ func main() {
 	a := 1
 	b := 2
+	c := 3
`;

describe('comment position is invariant to hunk expansion', () => {
    const canonical = parseDiff(baseDiff);
    const canonicalTarget = canonical.find(l => l.text.includes('c := 3'))!;

    test('sanity: the comment target has a stable canonical position', () => {
        expect(canonicalTarget).toBeTruthy();
        expect(canonicalTarget.pos).not.toBeNull();
    });

    test('expanded context lines keep real-line positions unchanged', () => {
        // Simulate handleExpandHunk inserting two context lines after the
        // first hunk header (index 5 in the split lines: the "@@ -1,4..." line
        // is index 4, so the inserted lines land at indices 5 and 6).
        const lines = baseDiff.split('\n');
        const hunkHeaderIdx = lines.findIndex(l => l.startsWith('@@ -1,4'));
        const insertAt = hunkHeaderIdx + 1;
        const contextLines = [' ctxA', ' ctxB'];
        const expandedLines = [
            ...lines.slice(0, insertAt),
            ...contextLines,
            ...lines.slice(insertAt),
        ];
        const expandedIndices = new Set<number>([insertAt, insertAt + 1]);

        const expanded = parseDiff(expandedLines.join('\n'), expandedIndices);

        // The expanded context lines are rendered but not commentable...
        const ctxA = expanded.find(l => l.text === ' ctxA')!;
        expect(ctxA.clickable).toBe(false);
        expect(ctxA.pos).toBeNull();

        // ...and the real line the user clicks reports the SAME position as in
        // the canonical diff, so the stored comment survives the diff reload.
        const expandedTarget = expanded.find(l => l.text.includes('c := 3'))!;
        expect(expandedTarget.pos).toBe(canonicalTarget.pos);
    });

    test('without the fix, expansion would shift the position (control)', () => {
        // Same expansion, but NOT flagging the inserted lines as expanded:
        // positions shift and the bug reproduces.
        const lines = baseDiff.split('\n');
        const hunkHeaderIdx = lines.findIndex(l => l.startsWith('@@ -1,4'));
        const insertAt = hunkHeaderIdx + 1;
        const expandedLines = [
            ...lines.slice(0, insertAt),
            ' ctxA',
            ' ctxB',
            ...lines.slice(insertAt),
        ];
        const shifted = parseDiff(expandedLines.join('\n')); // no expanded set
        const shiftedTarget = shifted.find(l => l.text.includes('c := 3'))!;
        expect(shiftedTarget.pos).not.toBe(canonicalTarget.pos);
    });
});
