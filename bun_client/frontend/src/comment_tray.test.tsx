import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { buildTrayGroups, fileOrderOf } from './comment_tray_utils';
import CommentTray from './components/review/CommentTray';
import type { Comment } from './components/review/types';
import { parseDiff } from './diff_utils';

const DIFF = `diff --git a/src/a.ts b/src/a.ts
index 111..222 100644
--- a/src/a.ts
+++ b/src/a.ts
@@ -1,3 +1,3 @@
 const a = 1;
-const c = 3;
+const c = 4;
`;

const rows = parseDiff(DIFF);

const comment = (over: Partial<Comment> & { id: string }): Comment => ({
    author: 'bob',
    body: 'body',
    path: 'src/a.ts',
    position: '3',
    in_reply_to: 0,
    created_at: '2026-01-01T00:00:00Z',
    outdated: false,
    diff_hunk: '',
    ...over,
});

const render = (comments: Comment[], selectedId: string | null = null) =>
    renderToStaticMarkup(
        <CommentTray
            groups={buildTrayGroups(comments, [], rows, fileOrderOf(rows))}
            selectedId={selectedId}
            isMobile={false}
            width={420}
            busy={false}
            onResize={() => {}}
            onResizeEnd={() => {}}
            onSelect={() => {}}
            onReply={async () => true}
            onEdit={async () => true}
            onDelete={() => {}}
            onJumpToFile={() => {}}
            onClose={() => {}}
        />
    );

describe('CommentTray', () => {
    test('lists every thread under its file, with the line it is on', () => {
        const html = render([
            comment({ id: '1', body: 'Why 4?' }),
            comment({ id: '2', author: 'alice', body: 'Off by one.', in_reply_to: 1 }),
        ]);
        expect(html).toContain('data-tray-thread="1"');
        expect(html).toContain('a.ts');
        expect(html).toContain('const c = 4;');
        expect(html).toContain('R2');
        expect(html).toContain('Why 4?');
        expect(html).toContain('Off by one.');
        expect(html).toContain('1 thread · 1 open');
    });

    test('shows every author, bots included', () => {
        const html = render([
            comment({ id: '1', author: 'github-advanced-security[bot]', body: 'Unused variable.' }),
        ]);
        expect(html).toContain('github-advanced-security[bot]');
        expect(html).toContain('Unused variable.');
    });

    test('puts PR-level comments under Conversation, with no reply box', () => {
        const html = render([comment({ id: '1', path: '', position: '', body: 'LGTM' })]);
        expect(html).toContain('Conversation');
        expect(html).toContain('LGTM');
        expect(html).not.toContain('Reply…');
    });

    test('folds a resolved thread to one line until it is selected', () => {
        const resolved = comment({
            id: '1',
            body: 'Rename this please',
            resolved: true,
            resolved_by: 'alice',
        });
        const folded = render([resolved]);
        expect(folded).toContain('ct-folded');
        expect(folded).toContain('✓ Resolved');
        expect(folded).not.toContain('Reply…');

        const open = render([resolved], '1');
        expect(open).not.toContain('ct-folded');
        expect(open).toContain('Reply…');
    });

    test('marks your pending comments and lets you edit or delete them', () => {
        const html = render([comment({ id: '9001', author: 'local', body: 'Mine' })]);
        expect(html).toContain('Pending');
        expect(html).toContain('You');
        expect(html).toContain('Edit');
        expect(html).toContain('title="Delete local comment"');
    });

    test('says so when the PR has no comments', () => {
        expect(render([])).toContain('No comments on this PR yet.');
    });
});
