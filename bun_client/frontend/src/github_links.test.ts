import { describe, expect, test } from 'bun:test';
import { githubDiffLink, lineLinkTarget, sha256Hex } from './github_links';

describe('sha256Hex', () => {
    // Expected digests from `shasum -a 256`.
    test.each([
        ['', 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'],
        ['abc', 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'],
        ['server/renderer.go', '800b6e76ef1d14721841e647f9e5a58583e6be5fa8653a60f949457736d45680'],
        // Exactly one block of message: the padding takes a second block.
        ['a'.repeat(64), 'ffe054fe7ae0cb6dc65c3af9b61d5209f439851db43d0ba5997337df154668eb'],
        // Multi-byte UTF-8, spanning two blocks.
        ['é/ü'.repeat(30), '14206fccc77715688ed228ad039195696fe84a182e03e3a4f86490074cd24125'],
    ])('%p', (text, digest) => {
        expect(sha256Hex(text)).toBe(digest);
    });
});

describe('githubDiffLink', () => {
    const hash = '800b6e76ef1d14721841e647f9e5a58583e6be5fa8653a60f949457736d45680';
    test('links a side and line of the file in the PR diff', () => {
        expect(
            githubDiffLink('https://github.com/acme/widgets/pull/42', 'server/renderer.go', 'R', 12)
        ).toBe(`https://github.com/acme/widgets/pull/42/files#diff-${hash}R12`);
        expect(
            githubDiffLink('https://github.com/acme/widgets/pull/42/', 'server/renderer.go', 'L', 3)
        ).toBe(`https://github.com/acme/widgets/pull/42/files#diff-${hash}L3`);
    });
    test('links the file without a line', () => {
        expect(
            githubDiffLink('https://github.com/acme/widgets/pull/42/files', 'server/renderer.go')
        ).toBe(`https://github.com/acme/widgets/pull/42/files#diff-${hash}`);
    });
});

describe('lineLinkTarget', () => {
    const context = { file: 'a.go', pos: 4, oldLineNo: 10, newLineNo: 12 };
    test('takes the clicked side on the full diff', () => {
        expect(lineLinkTarget(context, 'L')).toEqual({ side: 'L', line: 10 });
        expect(lineLinkTarget(context, 'R')).toEqual({ side: 'R', line: 12 });
        expect(lineLinkTarget({ ...context, oldLineNo: null }, 'L')).toEqual({
            side: null,
            line: null,
        });
    });

    test("reads a filtered row's base line from the full diff", () => {
        const fullRows = [
            { file: 'a.go', pos: 7, oldLineNo: 20, newLineNo: 12 },
            { file: 'a.go', pos: 8, oldLineNo: null, newLineNo: 13 },
        ];
        const positions: Record<number, number> = { 4: 7, 5: 8 };
        const filtered = {
            fullRows,
            toFullRow: (_: string, pos: number) => positions[pos] ?? null,
        };
        // The filtered row's old number counts the baseline's lines; the full
        // diff's counts the base's.
        expect(lineLinkTarget(context, 'L', filtered)).toEqual({ side: 'L', line: 20 });
        // The head side is the same on both.
        expect(lineLinkTarget(context, 'R', filtered)).toEqual({ side: 'R', line: 12 });
        // An addition in the full diff has no base line: link the head's.
        expect(lineLinkTarget({ ...context, pos: 5, newLineNo: 13 }, 'L', filtered)).toEqual({
            side: 'R',
            line: 13,
        });
        // A removed line the full diff doesn't show: the file.
        expect(
            lineLinkTarget({ file: 'a.go', pos: 9, oldLineNo: 3, newLineNo: null }, 'L', filtered)
        ).toEqual({ side: null, line: null });
    });
});
