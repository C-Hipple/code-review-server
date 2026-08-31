import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { emptyAnnotationIndex } from '../../annotation_utils';
import { parseDiff } from '../../diff_utils';
import type { LspData } from '../../hooks/useLsp';
import DiffView, { type DiffViewProps } from './DiffView';
import { buildDiffTheme } from './diff_theme';

const DIFF = `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
-func old() {}
+func added() {}
 // trailing context
`;

const LSP_DATA: LspData = {
    hover: { contents: { language: 'go', value: 'func added()' } },
    refs: [
        {
            uri: 'file:///repo/other.go',
            range: { start: { line: 41, character: 0 }, end: { line: 41, character: 9 } },
        },
    ],
    definitions: null,
    typeDefinitions: null,
};

const noop = () => {};

const render = (overrides: Partial<DiffViewProps>) => {
    const props: DiffViewProps = {
        parsedLines: parseDiff(DIFF),
        comments: [],
        outdatedComments: [],
        collapsedFiles: new Set(),
        visibleThreadIds: new Set(),
        annotations: emptyAnnotationIndex,
        visibleAnnotationKeys: new Set(),
        activeLineIndex: null,
        activeLspIndex: null,
        replyToId: null,
        editingCommentId: null,
        commentBody: '',
        editingCommentBody: '',
        isAddingComment: false,
        isMobile: false,
        wrapLines: false,
        diffTheme: buildDiffTheme('dark'),
        lspData: null,
        onToggleThreadsVisible: noop,
        onToggleAnnotations: noop,
        onAnnotationToComment: noop,
        onCommentClick: noop,
        onCodeClick: noop,
        onThreadClick: noop,
        onDeleteComment: noop,
        onToggleFileCollapse: noop,
        onShowOutdated: noop,
        onDockDiffFile: noop,
        onExpandHunk: noop,
        onOpenCodeViewer: noop,
        onClearLspData: noop,
        onCloseLsp: noop,
        onCancelInline: noop,
        onChangeCommentBody: noop,
        onChangeEditingCommentBody: noop,
        onSubmitInline: noop,
        ...overrides,
    };
    return renderToStaticMarkup(<DiffView {...props} />);
};

// The index of the first clickable code row, which is what a user clicks to
// query the LSP.
const codeRowIndex = parseDiff(DIFF).findIndex(l => l.lineType === 'addition');

describe('DiffView LSP popover', () => {
    test('code rows normally take the brightness hover cue', () => {
        const html = render({});
        expect(html).toContain('class="hover-line"');
        expect(html).not.toContain('hover-line--flat');
    });

    test('the row showing the popover drops the filtering hover class', () => {
        const html = render({ activeLspIndex: codeRowIndex, lspData: LSP_DATA });
        // `filter` on the row would make it a stacking context, so the popover
        // would paint below every diff row after it and those rows would
        // swallow the clicks meant for its links. The flat variant keeps the
        // outline cue without the filter.
        expect(html).toContain('class="hover-line--flat"');
        // Exactly one row opts out: the one holding the popover.
        expect(html.match(/hover-line--flat/g)).toHaveLength(1);
        // And the popover it guards is actually rendered, links included.
        expect(html).toContain('func added()');
        expect(html).toContain('other.go : 42');
    });
});
