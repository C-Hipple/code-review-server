import { expect, test, describe } from 'bun:test';
import { parseDiff } from './diff_utils';
import {
    buildPendingPreviews,
    contextForPosition,
    pendingComments,
    reviewVerdictLabel,
} from './review_preview_utils';
import type { Comment } from './components/review/types';

const comment = (over: Partial<Comment> & { id: string }): Comment => ({
    author: 'alice',
    body: 'body',
    path: 'src/a.ts',
    position: '5',
    in_reply_to: 0,
    created_at: '2026-01-01T00:00:00Z',
    outdated: false,
    diff_hunk: '',
    ...over,
});

const local = (over: Partial<Comment> & { id: string }): Comment =>
    comment({ author: 'local', created_at: '', ...over });

// Two files, so the preview has something to sort and something to bound
// context against.
const DIFF = `diff --git a/src/a.ts b/src/a.ts
index 111..222 100644
--- a/src/a.ts
+++ b/src/a.ts
@@ -1,4 +1,5 @@
 const a = 1;
 const b = 2;
-const c = 3;
+const c = 4;
+const d = 5;
diff --git a/src/b.ts b/src/b.ts
index 333..444 100644
--- a/src/b.ts
+++ b/src/b.ts
@@ -1,2 +1,2 @@
-let x = 1;
+let x = 2;
`;

const lines = parseDiff(DIFF);

// Position of a line, as the diff parser assigned it.
const posOf = (file: string, text: string): string => {
    const line = lines.find(l => l.file === file && l.text === text);
    if (!line || line.pos === null) throw new Error(`no position for ${file} ${text}`);
    return line.pos.toString();
};

describe('pendingComments', () => {
    test('keeps only the unsubmitted local comments', () => {
        const all = [comment({ id: '1' }), local({ id: '2' }), comment({ id: '3' })];
        expect(pendingComments(all).map(c => c.id)).toEqual(['2']);
    });
});

describe('contextForPosition', () => {
    test('ends at the commented line and includes the lines above it', () => {
        const { context, anchorIndex } = contextForPosition(
            lines,
            'src/a.ts',
            posOf('src/a.ts', '+const d = 5;')
        );
        expect(context.map(l => l.text)).toEqual([
            ' const b = 2;',
            '-const c = 3;',
            '+const c = 4;',
            '+const d = 5;',
        ]);
        expect(context[anchorIndex].text).toBe('+const d = 5;');
    });

    test('stops at the hunk header rather than reaching into the file above', () => {
        const { context, anchorIndex } = contextForPosition(
            lines,
            'src/b.ts',
            posOf('src/b.ts', '-let x = 1;')
        );
        expect(context.map(l => l.text)).toEqual(['-let x = 1;']);
        expect(anchorIndex).toBe(0);
        expect(context.every(l => l.file === 'src/b.ts')).toBe(true);
    });

    test('honors the context size', () => {
        const { context } = contextForPosition(
            lines,
            'src/a.ts',
            posOf('src/a.ts', '+const d = 5;'),
            2
        );
        expect(context.map(l => l.text)).toEqual(['+const c = 4;', '+const d = 5;']);
    });

    test('reports no context for a line that is not in the diff', () => {
        expect(contextForPosition(lines, 'src/a.ts', '999')).toEqual({
            context: [],
            anchorIndex: -1,
            sourceIndex: -1,
        });
        expect(contextForPosition(lines, 'src/a.ts', '')).toEqual({
            context: [],
            anchorIndex: -1,
            sourceIndex: -1,
        });
    });
});

describe('buildPendingPreviews', () => {
    test('previews each pending comment with its file, line and context', () => {
        const previews = buildPendingPreviews(
            [local({ id: '7', path: 'src/a.ts', position: posOf('src/a.ts', '+const d = 5;') })],
            lines
        );
        expect(previews).toHaveLength(1);
        expect(previews[0].file).toBe('src/a.ts');
        expect(previews[0].lineNo).toBe(4);
        expect(previews[0].context[previews[0].context.length - 1].text).toBe('+const d = 5;');
        expect(previews[0].fileLevel).toBe(false);
        expect(previews[0].replyTo).toBeUndefined();
    });

    test('orders comments the way the diff does, not the way they were written', () => {
        const first = local({
            id: '1',
            path: 'src/b.ts',
            position: posOf('src/b.ts', '+let x = 2;'),
        });
        const second = local({
            id: '2',
            path: 'src/a.ts',
            position: posOf('src/a.ts', '-const c = 3;'),
        });
        const previews = buildPendingPreviews([first, second], lines);
        expect(previews.map(p => p.comment.id)).toEqual(['2', '1']);
    });

    test('a reply inherits the file and context of the thread it answers', () => {
        const root = comment({
            id: '10',
            path: 'src/a.ts',
            position: posOf('src/a.ts', '+const c = 4;'),
        });
        // A local reply targets the thread's last comment and carries no path
        // or position of its own.
        const reply = local({ id: '11', path: '', position: '0', in_reply_to: 10 });
        const previews = buildPendingPreviews([root, reply], lines);
        expect(previews.map(p => p.comment.id)).toEqual(['11']);
        expect(previews[0].file).toBe('src/a.ts');
        expect(previews[0].context[previews[0].context.length - 1].text).toBe('+const c = 4;');
        expect(previews[0].replyTo?.id).toBe('10');
    });

    test('keeps a comment whose line has left the diff, without context', () => {
        const previews = buildPendingPreviews(
            [local({ id: '3', path: 'src/gone.ts', position: '2' })],
            lines
        );
        expect(previews).toHaveLength(1);
        expect(previews[0].context).toEqual([]);
        expect(previews[0].anchorIndex).toBe(-1);
        expect(previews[0].lineNo).toBeNull();
        expect(previews[0].fileLevel).toBe(false);
    });

    test('marks a comment left on the file itself', () => {
        // A file-header comment stores position 0; an unmapped one comes back
        // with no position at all. Both mean "not on a line".
        for (const position of ['0', '']) {
            const previews = buildPendingPreviews(
                [local({ id: '4', path: 'src/a.ts', position })],
                lines
            );
            expect(previews[0].fileLevel).toBe(true);
            expect(previews[0].context).toEqual([]);
            expect(previews[0].lineNo).toBeNull();
        }
    });
});

describe('reviewVerdictLabel', () => {
    test('names the reviewer when the server knows the login', () => {
        expect(reviewVerdictLabel('APPROVE', 'C-Hipple')).toBe('Approved by C-Hipple');
        expect(reviewVerdictLabel('REQUEST_CHANGES', 'C-Hipple')).toBe(
            'Changes requested by C-Hipple'
        );
        expect(reviewVerdictLabel('COMMENT', 'C-Hipple')).toBe('Commented by C-Hipple');
    });

    test('falls back to the bare verdict without a login', () => {
        expect(reviewVerdictLabel('APPROVE')).toBe('Approved');
        expect(reviewVerdictLabel('REQUEST_CHANGES', '  ')).toBe('Changes requested');
        expect(reviewVerdictLabel('COMMENT', '')).toBe('Commented');
    });
});
