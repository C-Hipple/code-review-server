import { describe, expect, test } from 'bun:test';
import type { LspLocation } from './lsp';
import {
    displayPath,
    groupLocationsByFile,
    hasHoverContent,
    mergeLocationLines,
    splitLocationLine,
    toLocationList,
} from './lsp_utils';

const loc = (uri: string, line: number, start = 0, end = start): LspLocation => ({
    uri,
    range: { start: { line, character: start }, end: { line, character: end } },
});

describe('hasHoverContent', () => {
    test('is false for empty answers in each shape', () => {
        expect(hasHoverContent(null)).toBe(false);
        expect(hasHoverContent({ contents: '' })).toBe(false);
        expect(hasHoverContent({ contents: [] })).toBe(false);
        expect(hasHoverContent({ contents: { language: 'go', value: '' } })).toBe(false);
        expect(hasHoverContent({ contents: { language: 'go', value: 'func f()' } })).toBe(true);
        expect(hasHoverContent({ contents: ['doc'] })).toBe(true);
    });
});

describe('toLocationList', () => {
    test('wraps a single location and drops empty answers', () => {
        const a = loc('file:///a.go', 1);
        expect(toLocationList(a)).toEqual([a]);
        expect(toLocationList([a])).toEqual([a]);
        expect(toLocationList([])).toBeNull();
        expect(toLocationList(null)).toBeNull();
    });
});

describe('mergeLocationLines', () => {
    test('keeps the lines already read for a file', () => {
        expect(
            mergeLocationLines(
                { 'file:///a.go': { 2: 'refs', 9: 'more refs' } },
                { 'file:///a.go': { 2: 'def' }, 'file:///b.go': { 0: 'package b' } }
            )
        ).toEqual({
            'file:///a.go': { 2: 'def', 9: 'more refs' },
            'file:///b.go': { 0: 'package b' },
        });
    });
});

describe('groupLocationsByFile', () => {
    test('keeps the server’s file order and sorts lines within a file', () => {
        const groups = groupLocationsByFile([
            loc('file:///b.go', 9),
            loc('file:///a.go', 4),
            loc('file:///b.go', 2),
        ]);
        expect(groups.map(g => g.uri)).toEqual(['file:///b.go', 'file:///a.go']);
        expect(groups[0].locations.map(l => l.range.start.line)).toEqual([2, 9]);
    });
});

describe('displayPath', () => {
    test('is relative to the most specific root containing the file', () => {
        const roots = ['/src/crs', '/src/crs/.worktrees/pr-7'];
        expect(displayPath('file:///src/crs/.worktrees/pr-7/server/server.go', roots)).toBe(
            'server/server.go'
        );
        expect(displayPath('file:///src/crs/main.go', roots)).toBe('main.go');
    });

    test('stays absolute outside every root and decodes escapes', () => {
        expect(displayPath('file:///usr/local/go/src/fmt/print.go', ['/src/crs'])).toBe(
            '/usr/local/go/src/fmt/print.go'
        );
        expect(displayPath('file:///src/my%20repo/a.ts', ['/src/my repo/'])).toBe('a.ts');
        expect(displayPath('file:///src/crsx/a.go', ['/src/crs'])).toBe('/src/crsx/a.go');
    });
});

describe('splitLocationLine', () => {
    test('splits around the range and drops indentation', () => {
        expect(splitLocationLine('\t\tresult := serve(ctx)', loc('u', 0, 12, 17).range)).toEqual({
            before: 'result := ',
            match: 'serve',
            after: '(ctx)',
        });
    });

    test('a range running onto later lines takes the rest of the line', () => {
        const range = { start: { line: 3, character: 5 }, end: { line: 6, character: 1 } };
        expect(splitLocationLine('func handler() {', range)).toEqual({
            before: 'func ',
            match: 'handler() {',
            after: '',
        });
    });

    test('a range past the end of a truncated line matches nothing', () => {
        expect(splitLocationLine('short', loc('u', 0, 40, 45).range)).toEqual({
            before: 'short',
            match: '',
            after: '',
        });
    });
});
