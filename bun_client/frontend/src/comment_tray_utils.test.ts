import { describe, expect, test } from 'bun:test';
import {
    authorHue,
    authorInitials,
    buildTrayGroups,
    clampTrayWidth,
    countTrayThreads,
    displayAuthor,
    fileOrderOf,
    filterTrayGroups,
    hunkTail,
    stepThread,
    threadPreview,
    trayOrder,
} from './comment_tray_utils';
import { parseDiff } from './diff_utils';
import type { Comment } from './components/review/types';

const comment = (over: Partial<Comment> & { id: string }): Comment => ({
    author: 'alice',
    body: 'body',
    path: 'src/a.ts',
    position: '1',
    in_reply_to: 0,
    created_at: '2026-01-01T00:00:00Z',
    outdated: false,
    diff_hunk: '',
    ...over,
});

const DIFF = [
    'diff --git a/src/a.ts b/src/a.ts',
    'index 1..2 100644',
    '--- a/src/a.ts',
    '+++ b/src/a.ts',
    '@@ -1,3 +1,3 @@',
    ' const a = 1;',
    '-const b = 2;',
    '+const b = 3;',
    ' const c = 4;',
    'diff --git a/src/b.ts b/src/b.ts',
    'index 1..2 100644',
    '--- a/src/b.ts',
    '+++ b/src/b.ts',
    '@@ -10,2 +10,3 @@',
    ' x();',
    '+y();',
    ' z();',
].join('\n');

const rows = parseDiff(DIFF);

describe('hunkTail', () => {
    test('reads the commented line and its number from the end of the hunk', () => {
        const tail = hunkTail(
            ['@@ -3,7 +3,8 @@', ' a', ' b', '+    punctuation: string;'].join('\n')
        );
        expect(tail).toEqual({
            text: '    punctuation: string;',
            kind: 'addition',
            lineNo: 5,
            side: 'R',
        });
    });

    test('numbers a removed line on the base side', () => {
        const tail = hunkTail(['@@ -10,3 +10,2 @@', ' a', '-gone'].join('\n'));
        expect(tail).toEqual({ text: 'gone', kind: 'deletion', lineNo: 11, side: 'L' });
    });

    test('has nothing for an empty hunk', () => {
        expect(hunkTail('')).toBeNull();
    });
});

describe('buildTrayGroups', () => {
    test('puts the conversation first, then files in diff order, then outdated-only files', () => {
        const groups = buildTrayGroups(
            [
                comment({ id: '1', path: 'src/b.ts', position: '2' }),
                comment({ id: '2', path: '', position: '', body: 'LGTM overall' }),
                comment({ id: '3', path: 'src/a.ts', position: '3' }),
            ],
            [comment({ id: '4', path: 'src/old.ts', outdated: true })],
            rows,
            fileOrderOf(rows)
        );
        expect(groups.map(g => g.key)).toEqual([
            'conversation',
            'file:src/a.ts',
            'file:src/b.ts',
            'file:src/old.ts',
        ]);
    });

    test('keeps every author, bots included', () => {
        const groups = buildTrayGroups(
            [
                comment({ id: '1', author: 'github-advanced-security[bot]' }),
                comment({ id: '2', author: 'bob', position: '2' }),
            ],
            [],
            rows,
            fileOrderOf(rows)
        );
        expect(groups[0].threads.map(t => t.comments[0].author)).toEqual([
            'github-advanced-security[bot]',
            'bob',
        ]);
    });

    test('takes the line from the diff row the comment is on', () => {
        const [group] = buildTrayGroups(
            [comment({ id: '1', path: 'src/a.ts', position: '3' })],
            [],
            rows,
            fileOrderOf(rows)
        );
        // Position 3 of src/a.ts is the added `const b = 3;`, head line 2.
        expect(group.threads[0].line).toEqual({
            text: 'const b = 3;',
            kind: 'addition',
            lineNo: 2,
            side: 'R',
        });
    });

    test('takes an outdated thread’s line from its hunk, and lists it after the live ones', () => {
        const [group] = buildTrayGroups(
            [comment({ id: '1', path: 'src/a.ts', position: '1' })],
            [
                comment({
                    id: '2',
                    path: 'src/a.ts',
                    position: '',
                    outdated: true,
                    created_at: '2025-12-01T00:00:00Z',
                    diff_hunk: '@@ -1,2 +1,2 @@\n one\n-two',
                }),
            ],
            rows,
            fileOrderOf(rows)
        );
        expect(group.threads.map(t => t.rootId)).toEqual(['1', '2']);
        expect(group.threads[1].line).toEqual({
            text: 'two',
            kind: 'deletion',
            lineNo: 2,
            side: 'L',
        });
    });

    test('gathers replies under their root and marks pending local ones', () => {
        const [group] = buildTrayGroups(
            [
                comment({ id: '1', author: 'bob' }),
                comment({ id: '2', author: 'alice', in_reply_to: 1 }),
                comment({ id: '9001', author: 'local', in_reply_to: 2, path: '', position: '' }),
            ],
            [],
            rows,
            fileOrderOf(rows)
        );
        expect(group.threads).toHaveLength(1);
        const [thread] = group.threads;
        expect(thread.comments.map(c => c.id)).toEqual(['1', '2', '9001']);
        expect(thread.pending).toBe(true);
        expect(thread.participants).toEqual(['bob', 'alice', 'local']);
    });

    test('shows a comment the server lists twice only once', () => {
        const dup = comment({ id: '1' });
        const groups = buildTrayGroups([dup], [dup], rows, fileOrderOf(rows));
        expect(countTrayThreads(groups).all).toBe(1);
    });
});

describe('filters and counts', () => {
    const groups = buildTrayGroups(
        [
            comment({ id: '1', resolved: true, resolved_by: 'bob' }),
            comment({ id: '2', position: '2' }),
            comment({ id: '3', path: 'src/b.ts', position: '1', author: 'local' }),
        ],
        [],
        rows,
        fileOrderOf(rows)
    );

    test('counts open, resolved and pending threads', () => {
        expect(countTrayThreads(groups)).toEqual({ all: 3, open: 2, resolved: 1, pending: 1 });
    });

    test('drops a group the filter empties', () => {
        const resolved = filterTrayGroups(groups, 'resolved');
        expect(resolved.map(g => g.key)).toEqual(['file:src/a.ts']);
        expect(trayOrder(resolved)).toEqual(['1']);
        expect(trayOrder(filterTrayGroups(groups, 'open'))).toEqual(['2', '3']);
        expect(filterTrayGroups(groups, 'all')).toBe(groups);
    });
});

describe('stepThread', () => {
    const order = ['a', 'b', 'c'];

    test('steps forward and back, wrapping at either end', () => {
        expect(stepThread(order, 'a', 1)).toBe('b');
        expect(stepThread(order, 'c', 1)).toBe('a');
        expect(stepThread(order, 'a', -1)).toBe('c');
    });

    test('starts at the top going forward and the bottom going back', () => {
        expect(stepThread(order, null, 1)).toBe('a');
        expect(stepThread(order, 'gone', -1)).toBe('c');
        expect(stepThread([], null, 1)).toBeNull();
    });
});

describe('authors', () => {
    test('a local comment is yours', () => {
        expect(displayAuthor('local')).toBe('You');
        expect(displayAuthor('bob')).toBe('bob');
        expect(authorInitials('local')).toBe('You');
    });

    test('initials come from the login’s words, bots without their suffix', () => {
        expect(authorInitials('bob')).toBe('B');
        expect(authorInitials('github-advanced-security[bot]')).toBe('GA');
        expect(authorInitials('C-Hipple')).toBe('CH');
    });

    test('each author keeps one hue', () => {
        expect(authorHue('bob')).toBe(authorHue('bob'));
        expect(authorHue('bob')).toBeGreaterThanOrEqual(0);
        expect(authorHue('bob')).toBeLessThan(360);
    });
});

test('threadPreview gives one plain line of the root comment', () => {
    const [group] = buildTrayGroups(
        [comment({ id: '1', body: '**Nit:** rename\n\n```ts\ncode\n```\nthis `x`' })],
        [],
        rows,
        fileOrderOf(rows)
    );
    expect(threadPreview(group.threads[0])).toBe('Nit: rename this x');
    expect(threadPreview(group.threads[0], 8)).toBe('Nit: re…');
});

test('clampTrayWidth keeps the tray between its minimum and most of the window', () => {
    expect(clampTrayWidth(100, 1600)).toBe(320);
    expect(clampTrayWidth(500, 1600)).toBe(500);
    expect(clampTrayWidth(2000, 1600)).toBe(960);
});
