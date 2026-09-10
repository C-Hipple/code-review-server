import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import ReviewPreview from './components/review/ReviewPreview';
import { buildDiffTheme } from './components/review/diff_theme';
import type { Comment } from './components/review/types';
import { parseDiff } from './diff_utils';
import { buildPendingPreviews } from './review_preview_utils';

const DIFF = `diff --git a/src/a.ts b/src/a.ts
index 111..222 100644
--- a/src/a.ts
+++ b/src/a.ts
@@ -1,3 +1,3 @@
 const a = 1;
-const c = 3;
+const c = 4;
`;

const lines = parseDiff(DIFF);
const theme = buildDiffTheme('dark');

const localComment = (over: Partial<Comment> & { id: string }): Comment => ({
    author: 'local',
    body: 'this should be a constant',
    path: 'src/a.ts',
    position: '3',
    in_reply_to: 0,
    created_at: '',
    outdated: false,
    diff_hunk: '',
    ...over,
});

// Prism splits a highlighted line into per-token spans, so assertions about
// the code itself run against the rendered text rather than the markup.
const textOf = (html: string): string =>
    html
        .replace(/<[^>]*>/g, '')
        .replace(/&#x27;/g, "'")
        .replace(/&quot;/g, '"')
        .replace(/&amp;/g, '&');

const render = (props: Partial<Parameters<typeof ReviewPreview>[0]> = {}) =>
    renderToStaticMarkup(
        <ReviewPreview
            reviewEvent="APPROVE"
            reviewBody="Looks good."
            username="C-Hipple"
            previews={buildPendingPreviews([localComment({ id: '1' })], lines)}
            diffTheme={theme}
            {...props}
        />
    );

describe('ReviewPreview', () => {
    test('shows the verdict, the body and the pending comment with its context', () => {
        const html = render();
        const text = textOf(html);
        expect(text).toContain('Approved by C-Hipple');
        expect(text).toContain('Looks good.');
        expect(text).toContain('1 comment');
        expect(text).toContain('src/a.ts:2');
        // The commented line and the line above it, rendered as diff rows
        // carrying the diff's own tints and +/- gutter.
        expect(text).toContain('const c = 4;');
        expect(text).toContain('const c = 3;');
        expect(html).toContain('var(--diff-add-bg)');
        expect(html).toContain('var(--diff-del-bg)');
        // The commented line itself is marked, the way an active diff row is.
        expect(html).toContain('border-left:3px solid var(--accent)');
        expect(text).toContain('this should be a constant');
    });

    test('says so when the review carries no body', () => {
        expect(textOf(render({ reviewBody: '   ' }))).toContain('No review body');
    });

    test('drops the comment section when nothing is pending', () => {
        const text = textOf(render({ previews: [] }));
        expect(text).toContain('no comments');
        expect(text).not.toContain('this should be a constant');
    });

    test('names the thread a pending reply answers', () => {
        const root: Comment = {
            id: '10',
            author: 'alice',
            body: 'why not a constant?',
            path: 'src/a.ts',
            position: '3',
            in_reply_to: 0,
            created_at: '2026-01-01T00:00:00Z',
            outdated: false,
            diff_hunk: '',
        };
        const reply = localComment({ id: '11', path: '', position: '0', in_reply_to: 10 });
        const text = textOf(render({ previews: buildPendingPreviews([root, reply], lines) }));
        expect(text).toContain('reply to alice');
        expect(text).toContain('src/a.ts:2');
    });
});
