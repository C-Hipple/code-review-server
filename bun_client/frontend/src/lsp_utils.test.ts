import { describe, expect, test } from 'bun:test';
import type { LspLocation } from './lsp';
import {
    applyProgress,
    canGoBack,
    canGoForward,
    currentEntry,
    displayPath,
    filterOutline,
    goBack,
    goForward,
    groupLocationsByFile,
    hasHoverContent,
    isRetryableLspError,
    isUnder,
    keepsLspSession,
    type LspProgress,
    mergeLocationLines,
    navigate,
    outlineSymbols,
    pathInCheckout,
    progressSummary,
    splitLocationLine,
    startHistory,
    symbolKindName,
    toLocationList,
    uriToPath,
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

describe('toLocationList with links', () => {
    test("a LocationLink becomes its target's name, as rust-analyzer answers", () => {
        const target = loc('file:///r/src/util.rs', 1, 7, 12).range;
        expect(
            toLocationList([
                {
                    targetUri: 'file:///r/src/util.rs',
                    targetRange: loc('file:///r/src/util.rs', 0, 0, 3).range,
                    targetSelectionRange: target,
                    originSelectionRange: loc('file:///r/src/main.rs', 3, 25, 30).range,
                },
            ])
        ).toEqual([{ uri: 'file:///r/src/util.rs', range: target }]);
    });

    test('a link without a selection range points at its whole target', () => {
        const range = loc('file:///a.go', 4, 0, 9).range;
        expect(toLocationList([{ targetUri: 'file:///a.go', targetRange: range }])).toEqual([
            { uri: 'file:///a.go', range },
        ]);
    });
});

describe('paths', () => {
    test('uriToPath decodes what a server percent-encoded', () => {
        expect(uriToPath('file:///home/me/My%20Code/a.ts')).toBe('/home/me/My Code/a.ts');
        expect(uriToPath('file:///bad%zz.ts')).toBe('/bad%zz.ts');
    });

    test('isUnder and pathInCheckout keep sibling directories apart', () => {
        expect(isUnder('/r', '/r/a.go')).toBe(true);
        expect(isUnder('/r/', '/r/a.go')).toBe(true);
        expect(isUnder('/r', '/rr/a.go')).toBe(false);
        expect(isUnder('', '/r/a.go')).toBe(false);
        expect(pathInCheckout('/r', '/r/src/a.go')).toBe('src/a.go');
        expect(pathInCheckout('/r', '/usr/lib/go/src/fmt/print.go')).toBe(
            '/usr/lib/go/src/fmt/print.go'
        );
    });
});

describe('outlineSymbols', () => {
    const range = (line: number, endLine = line, character = 0) => ({
        start: { line, character },
        end: { line: endLine, character: 1 },
    });

    test('flattens hierarchical symbols depth-first, in file order', () => {
        const outline = outlineSymbols([
            {
                name: 'helper',
                kind: 12,
                range: range(20, 22),
                selectionRange: range(20, 20, 5),
            },
            {
                name: 'Server',
                kind: 23,
                detail: 'struct',
                range: range(2, 10),
                selectionRange: range(2, 2, 5),
                children: [
                    { name: 'port', kind: 8, range: range(4), selectionRange: range(4, 4, 1) },
                    { name: 'addr', kind: 8, range: range(3), selectionRange: range(3, 3, 1) },
                ],
            },
        ]);
        expect(outline.map(s => [s.name, s.line, s.character, s.depth])).toEqual([
            ['Server', 2, 5, 0],
            ['addr', 3, 1, 1],
            ['port', 4, 1, 1],
            ['helper', 20, 5, 0],
        ]);
        expect(outline[0].detail).toBe('struct');
    });

    test('nests flat symbols by their ranges', () => {
        const at = (line: number, endLine = line) => ({
            uri: 'file:///a.py',
            range: range(line, endLine),
        });
        const outline = outlineSymbols([
            { name: 'Greeter', kind: 5, location: at(0, 8) },
            { name: 'greet', kind: 6, location: at(2, 4), containerName: 'Greeter' },
            { name: 'main', kind: 12, location: at(10, 12) },
        ]);
        expect(outline.map(s => [s.name, s.depth])).toEqual([
            ['Greeter', 0],
            ['greet', 1],
            ['main', 0],
        ]);
    });

    test('nothing for an empty or missing answer', () => {
        expect(outlineSymbols(null)).toEqual([]);
        expect(outlineSymbols([])).toEqual([]);
    });

    test('filterOutline matches names case-insensitively', () => {
        const outline = outlineSymbols([
            { name: 'formatGreeting', kind: 12, range: range(1), selectionRange: range(1) },
            { name: 'Greeting', kind: 11, range: range(5), selectionRange: range(5) },
            { name: 'main', kind: 12, range: range(9), selectionRange: range(9) },
        ]);
        expect(filterOutline(outline, 'greet').map(s => s.name)).toEqual([
            'formatGreeting',
            'Greeting',
        ]);
        expect(filterOutline(outline, '  ')).toHaveLength(3);
    });

    test('symbolKindName labels LSP symbol kinds', () => {
        expect(symbolKindName(12)).toBe('function');
        expect(symbolKindName(23)).toBe('struct');
        expect(symbolKindName(999)).toBe('symbol');
    });
});

describe('progress', () => {
    const fold = (...steps: [string | number, object][]) =>
        steps.reduce(
            (running, [token, value]) => applyProgress(running, { token, value }),
            new Map<string | number, LspProgress>()
        );

    test('follows work from begin through reports to end', () => {
        const running = fold(
            ['index', { kind: 'begin', title: 'Indexing', percentage: 0 }],
            ['index', { kind: 'report', message: '3/10 (core)', percentage: 30 }]
        );
        expect(progressSummary(running)).toBe('Indexing: 3/10 (core) 30%');
        expect(
            progressSummary(applyProgress(running, { token: 'index', value: { kind: 'end' } }))
        ).toBeNull();
    });

    test('names the latest work, and how much else is running', () => {
        const running = fold(
            ['load', { kind: 'begin', title: 'Loading packages' }],
            [7, { kind: 'begin', title: 'Indexing', percentage: 12.6 }]
        );
        expect(progressSummary(running)).toBe('Indexing 13% (+1 more)');
    });

    test('ignores what it cannot use, keeping the same map', () => {
        const running = fold(['a', { kind: 'begin', title: 'A' }]);
        expect(
            applyProgress(running, { token: 'b', value: { kind: 'report', percentage: 5 } })
        ).toBe(running);
        expect(applyProgress(running, { token: 'b', value: { kind: 'end' } })).toBe(running);
        expect(applyProgress(running, { value: { kind: 'begin', title: 'x' } })).toBe(running);
        expect(applyProgress(running, null)).toBe(running);
    });

    test('content modified and server cancelled are worth asking again', () => {
        expect(isRetryableLspError({ code: -32801, message: 'content modified' })).toBe(true);
        expect(isRetryableLspError({ code: -32802 })).toBe(true);
        expect(isRetryableLspError({ code: -32601 })).toBe(false);
        expect(isRetryableLspError(new Error('closed'))).toBe(false);
        expect(isRetryableLspError(null)).toBe(false);
    });
});

describe('navigation history', () => {
    test('Back returns to the line the jump was made from, Forward redoes it', () => {
        let h = startHistory({ path: 'src/main.go', line: 12 });
        expect(canGoBack(h)).toBe(false);
        h = navigate(h, { path: 'pkg/util/util.go', line: 4 }, 30);
        expect(currentEntry(h)).toEqual({ path: 'pkg/util/util.go', line: 4 });
        h = goBack(h);
        expect(currentEntry(h)).toEqual({ path: 'src/main.go', line: 30 });
        expect(canGoForward(h)).toBe(true);
        h = goForward(h);
        expect(currentEntry(h)).toEqual({ path: 'pkg/util/util.go', line: 4 });
        expect(goForward(h)).toBe(h);
    });

    test('a new jump after going back drops what was forward', () => {
        let h = startHistory({ path: 'a.ts', line: null });
        h = navigate(h, { path: 'b.ts', line: 1 });
        h = navigate(h, { path: 'c.ts', line: 2 });
        h = navigate(goBack(goBack(h)), { path: 'd.ts', line: 3 });
        expect(h.entries.map(e => e.path)).toEqual(['a.ts', 'd.ts']);
        expect(canGoForward(h)).toBe(false);
    });

    test('a jump to where the viewer already is adds nothing', () => {
        const h = navigate(startHistory({ path: 'a.ts', line: 5 }), { path: 'a.ts', line: 5 });
        expect(h.entries).toHaveLength(1);
    });

    test('keeps the last hundred places', () => {
        let h = startHistory({ path: 'f0.ts', line: 1 });
        for (let i = 1; i <= 150; i++) h = navigate(h, { path: `f${i}.ts`, line: 1 });
        expect(h.entries).toHaveLength(100);
        expect(currentEntry(h).path).toBe('f150.ts');
        expect(h.entries[0].path).toBe('f51.ts');
    });
});

describe('keepsLspSession', () => {
    const gopls = { lang: 'go', root: '/r' };

    test('a file of the same language under the root stays on its server', () => {
        expect(keepsLspSession(gopls, { lang: 'go', outside: false }, '/r/pkg/a.go')).toBe(true);
        // node_modules/pkg/package.json would make a nearer root; the
        // project's server still reads it.
        expect(
            keepsLspSession(
                { lang: 'typescript', root: '/r/web' },
                { lang: 'typescript', outside: false },
                '/r/web/node_modules/pkg/index.d.ts'
            )
        ).toBe(true);
    });

    test('so does the standard library a definition led to', () => {
        expect(
            keepsLspSession(gopls, { lang: 'go', outside: true }, '/usr/lib/go/src/fmt/print.go')
        ).toBe(true);
    });

    test('another language or project gets its own', () => {
        expect(keepsLspSession(gopls, { lang: 'python', outside: false }, '/r/x.py')).toBe(false);
        expect(
            keepsLspSession(
                { lang: 'typescript', root: '/r/web' },
                { lang: 'typescript', outside: false },
                '/r/extension/src/a.ts'
            )
        ).toBe(false);
        expect(keepsLspSession(null, { lang: 'go', outside: false }, '/r/a.go')).toBe(false);
    });
});
