import { expect, test, describe } from 'bun:test';
import { parseDiff, parseHunkHeader, planHunkExpansion, type ParsedLine } from './diff_utils';

describe('parseDiff', () => {
    test('handles new empty file', () => {
        const diff = `diff --git a/newfile.txt b/newfile.txt
new file mode 100644
index 0000000..e69de29
`;
        const lines = parseDiff(diff);
        // Expect one file header
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(1);
        expect(fileHeaders[0].text).toBe('newfile.txt');
        expect(fileHeaders[0].fileStatus).toBe('new');
    });

    test('handles modified file followed by new empty file', () => {
        const diff = `diff --git a/mod.txt b/mod.txt
index 123..456 100644
--- a/mod.txt
+++ b/mod.txt
@@ -1 +1 @@
-foo
+bar
diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..789
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(2);
        expect(fileHeaders[0].text).toBe('mod.txt');
        expect(fileHeaders[1].text).toBe('new.txt');
        expect(fileHeaders[1].fileStatus).toBe('new');
    });

    test('handles new empty file followed by modified file', () => {
        const diff = `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..789
diff --git a/mod.txt b/mod.txt
index 123..456 100644
--- a/mod.txt
+++ b/mod.txt
@@ -1 +1 @@
-foo
+bar
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(2);

        expect(fileHeaders[0].text).toBe('new.txt');
        expect(fileHeaders[0].fileStatus).toBe('new');

        expect(fileHeaders[1].text).toBe('mod.txt');
        expect(fileHeaders[1].fileStatus).toBe('modified');
    });

    test('handles new empty file with spaces', () => {
        const diff = `diff --git a/my new file.txt b/my new file.txt
new file mode 100644
index 0000000..e69de29
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(1);
        expect(fileHeaders[0].text).toBe('my new file.txt');
        expect(fileHeaders[0].fileStatus).toBe('new');
    });
});

describe('parseHunkHeader', () => {
    test('reads both ranges and the text after them', () => {
        expect(parseHunkHeader('@@ -10,3 +12,4 @@ func f() {')).toEqual({
            oldStart: 10,
            oldLines: 3,
            newStart: 12,
            newLines: 4,
            context: 'func f() {',
        });
    });

    test('a range without a length is one line long', () => {
        expect(parseHunkHeader('@@ -1 +1 @@')).toEqual({
            oldStart: 1,
            oldLines: 1,
            newStart: 1,
            newLines: 1,
            context: '',
        });
    });

    test('anything else is not a header', () => {
        expect(parseHunkHeader(' @@ -1 +1 @@')).toBeNull();
        expect(parseHunkHeader('+@@ -1 +1 @@')).toBeNull();
    });
});

// f.go has two hunks with lines 5–9 between them, written the way the server
// writes a diff: a blank line ahead of each hunk header, none between files.
const HEAD_F = [
    'type T struct {',
    'B',
    'c',
    'd',
    'e',
    'f',
    'g',
    'h',
    'i',
    'j',
    'k',
    'N',
    'l',
    'm',
    'n',
];
const TWO_HUNKS = [
    'diff --git a/f.go b/f.go', // 0
    'index 1111111..2222222 100644',
    '--- a/f.go',
    '+++ b/f.go',
    '',
    '@@ -1,4 +1,4 @@', // 5
    ' type T struct {',
    '-b',
    '+B',
    ' c',
    ' d', // 10
    '',
    '@@ -10,3 +10,4 @@ func f() {', // 12
    ' j',
    ' k',
    '+N', // 15
    ' l',
    'diff --git a/g.go b/g.go', // 17
    'index 3333333..4444444 100644',
    '--- a/g.go',
    '+++ b/g.go', // 20
    '',
    '@@ -1,2 +1,2 @@', // 22
    '-a',
    '+z',
    ' y', // 25
    '',
].join('\n');

/**
 * Expand a hunk the way the review view does: plan it, answer the request the
 * way the server's GetHunkContext does from `head`, then put its header and
 * lines into the diff, marking those lines expanded.
 */
function expand(
    diff: string,
    expanded: Set<number>,
    hunkLineIndex: number,
    direction: 'before' | 'after',
    count = 20,
    head = HEAD_F
) {
    const lines = diff.split('\n');
    const plan = planHunkExpansion(lines, hunkLineIndex, direction, count);
    if (!plan) throw new Error('nothing to expand');
    const { hunk } = plan;
    const [start, end] =
        direction === 'before'
            ? [Math.max(1, hunk.newStart - plan.count), hunk.newStart - 1]
            : [
                  hunk.newStart + hunk.newLines,
                  Math.min(head.length, hunk.newStart + hunk.newLines - 1 + plan.count),
              ];
    const fetched = head.slice(start - 1, end);
    const n = fetched.length;
    const shift = direction === 'before' ? n : 0;
    lines[plan.headerIndex] =
        `@@ -${hunk.oldStart - shift},${hunk.oldLines + n} +${hunk.newStart - shift},${hunk.newLines + n} @@` +
        (hunk.context ? ` ${hunk.context}` : '');
    lines.splice(plan.insertAt, 0, ...fetched.map(l => ` ${l}`));
    const next = new Set([...expanded].map(i => (i >= plan.insertAt ? i + n : i)));
    for (let k = 0; k < n; k++) next.add(plan.insertAt + k);
    return { diff: lines.join('\n'), expanded: next, plan };
}

const rowsOf = (lines: ParsedLine[], file: string) => lines.filter(l => l.file === file);
const hunkRows = (lines: ParsedLine[], file: string) =>
    rowsOf(lines, file).filter(l => l.lineType === 'hunk');
const codeText = (lines: ParsedLine[], file: string) =>
    rowsOf(lines, file)
        .filter(l => l.lineType !== 'hunk' && l.lineType !== 'file-header')
        .map(l => l.text.slice(1));

describe('planHunkExpansion', () => {
    const lines = TWO_HUNKS.split('\n');

    test('stops at the lines between a hunk and the next one down', () => {
        expect(planHunkExpansion(lines, 5, 'after', 20)).toMatchObject({
            headerIndex: 5,
            hunk: { newStart: 1, newLines: 4 },
            count: 5,
            // Ahead of the next hunk's header.
            insertAt: 12,
        });
    });

    test('stops at the lines between a hunk and the one above it', () => {
        expect(planHunkExpansion(lines, 12, 'before', 20)).toMatchObject({
            headerIndex: 12,
            hunk: { newStart: 10, newLines: 4, context: 'func f() {' },
            count: 5,
            insertAt: 13,
        });
    });

    test('asks for no more than it was asked to', () => {
        expect(planHunkExpansion(lines, 5, 'after', 3)?.count).toBe(3);
        expect(planHunkExpansion(lines, 12, 'before', 3)?.count).toBe(3);
    });

    test("is limited by nothing past the file's first and last hunks", () => {
        // f.go's last hunk ends ahead of g.go's header.
        expect(planHunkExpansion(lines, 12, 'after', 20)).toMatchObject({
            count: 20,
            insertAt: 17,
        });
        // g.go's first hunk sees nothing of f.go's above it.
        expect(planHunkExpansion(lines, 22, 'before', 20)).toMatchObject({
            count: 20,
            insertAt: 23,
        });
        expect(planHunkExpansion(lines, 22, 'after', 20)).toMatchObject({
            count: 20,
            insertAt: lines.length,
        });
    });

    test('grows the last hunk of a touching run going down, its first going up', () => {
        const run = [
            'diff --git a/r.go b/r.go',
            '--- a/r.go',
            '+++ b/r.go',
            '@@ -1,2 +1,2 @@', // 3
            ' a',
            ' b',
            '@@ -3,2 +3,3 @@', // 6
            ' c',
            '+X',
            ' d',
            '@@ -5,1 +6,1 @@', // 10
            ' e',
        ];
        expect(planHunkExpansion(run, 3, 'after', 20)).toMatchObject({
            headerIndex: 10,
            hunk: { newStart: 6 },
            insertAt: run.length,
        });
        expect(planHunkExpansion(run, 10, 'before', 20)).toMatchObject({
            headerIndex: 3,
            insertAt: 4,
        });
    });

    test('fetches nothing when a neighbour already overlaps', () => {
        const overlapping = lines.slice();
        overlapping[5] = '@@ -1,12 +1,12 @@';
        expect(planHunkExpansion(overlapping, 5, 'after', 20)).toBeNull();
        expect(planHunkExpansion(overlapping, 12, 'before', 20)).toBeNull();
    });

    test('plans nothing for a line that is not a hunk header', () => {
        expect(planHunkExpansion(lines, 6, 'after', 20)).toBeNull();
        expect(planHunkExpansion(lines, 99, 'after', 20)).toBeNull();
    });
});

describe('hunks that expansion leaves touching', () => {
    const canonical = parseDiff(TWO_HUNKS);

    test('hunks with lines between them stay apart', () => {
        expect(canonical.some(l => l.coalesced)).toBe(false);
        expect(hunkRows(canonical, 'f.go').map(l => l.text)).toEqual([
            '@@ -1,4 +1,4 @@',
            '@@ -10,3 +10,4 @@ func f() {',
        ]);
    });

    for (const [name, index, direction] of [
        ['growing the first one down', 5, 'after'],
        ['growing the second one up', 12, 'before'],
    ] as const) {
        test(`${name} joins them, every line shown once`, () => {
            const { diff, expanded, plan } = expand(TWO_HUNKS, new Set(), index, direction);
            expect(plan.count).toBe(5);
            const parsed = parseDiff(diff, expanded);

            // One header spans both hunks…
            const hunks = hunkRows(parsed, 'f.go');
            expect(hunks.filter(h => !h.coalesced).map(h => h.text)).toEqual(['@@ -1,12 +1,13 @@']);
            expect(hunks[1].coalesced).toBe(true);
            // …over the file's first 13 head lines, each once, numbered in order.
            expect(codeText(parsed, 'f.go')).toEqual([
                'type T struct {',
                'b',
                'B',
                'c',
                'd',
                'e',
                'f',
                'g',
                'h',
                'i',
                'j',
                'k',
                'N',
                'l',
            ]);
            const newNumbers = rowsOf(parsed, 'f.go')
                .map(l => l.newLineNo)
                .filter(n => n != null);
            expect(newNumbers).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13]);

            // The second header still takes its position, so every comment
            // anchor below it is where it was in the canonical diff.
            const positions = (lines: ParsedLine[]) =>
                rowsOf(lines, 'f.go')
                    .filter(l => l.clickable)
                    .map(l => [l.text, l.pos]);
            expect(positions(parsed)).toEqual(positions(canonical));
            expect(hunks[1].pos).toBe(hunkRows(canonical, 'f.go')[1].pos);
            // g.go is untouched.
            expect(rowsOf(parsed, 'g.go').map(l => [l.text, l.pos])).toEqual(
                rowsOf(canonical, 'g.go').map(l => [l.text, l.pos])
            );
        });
    }

    test('growing the joined hunk down grows its last part', () => {
        const once = expand(TWO_HUNKS, new Set(), 5, 'after');
        const head = parseDiff(once.diff, once.expanded).find(
            l => l.file === 'f.go' && l.lineType === 'hunk'
        )!;
        const twice = expand(once.diff, once.expanded, head.originalLineIndex, 'after');
        // f.go's second hunk, lines 10–13, grows by the file's last two lines.
        expect(twice.plan.hunk).toMatchObject({ newStart: 10, newLines: 4 });
        const parsed = parseDiff(twice.diff, twice.expanded);
        expect(
            hunkRows(parsed, 'f.go')
                .filter(h => !h.coalesced)
                .map(h => h.text)
        ).toEqual(['@@ -1,14 +1,15 @@']);
        expect(codeText(parsed, 'f.go').slice(-3)).toEqual(['l', 'm', 'n']);
    });

    test("a joined run keeps the first hunk's text after its ranges", () => {
        const diff = TWO_HUNKS.replace('@@ -1,4 +1,4 @@', '@@ -1,4 +1,4 @@ package f');
        const { diff: grown, expanded } = expand(diff, new Set(), 5, 'after');
        expect(hunkRows(parseDiff(grown, expanded), 'f.go').find(h => !h.coalesced)?.text).toBe(
            '@@ -1,12 +1,13 @@ package f'
        );
    });

    test('runs of touching hunks join whole', () => {
        const run = [
            'diff --git a/r.go b/r.go',
            '--- a/r.go',
            '+++ b/r.go',
            '@@ -1,2 +1,2 @@',
            ' a',
            ' b',
            '@@ -3,2 +3,3 @@ two',
            ' c',
            '+X',
            ' d',
            '@@ -5,1 +6,1 @@ three',
            ' e',
            '',
        ].join('\n');
        const parsed = parseDiff(run);
        expect(parsed.filter(l => l.lineType === 'hunk').map(l => [l.text, l.coalesced])).toEqual([
            ['@@ -1,5 +1,6 @@', undefined],
            ['@@ -3,2 +3,3 @@ two', true],
            ['@@ -5,1 +6,1 @@ three', true],
        ]);
        // Positions count every header after the first, as GitHub does.
        expect(
            parsed.filter(l => l.clickable && l.lineType !== 'file-header').map(l => l.pos)
        ).toEqual([1, 2, 4, 5, 6, 8]);
    });

    test("a file's first hunk never joins the last one of the file above", () => {
        const twoFiles = [
            'diff --git a/a.go b/a.go',
            '--- a/a.go',
            '+++ b/a.go',
            '@@ -1,2 +1,2 @@',
            '-x',
            '+y',
            'diff --git a/b.go b/b.go',
            '--- a/b.go',
            '+++ b/b.go',
            '@@ -3,1 +3,1 @@',
            '-p',
            '+q',
            '',
        ].join('\n');
        expect(parseDiff(twoFiles).some(l => l.coalesced)).toBe(false);
    });
});
