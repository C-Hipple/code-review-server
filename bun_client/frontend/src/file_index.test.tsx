import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import FileIndex from './components/review/FileIndex';
import { parseDiff } from './diff_utils';

const LONG_PATH = 'bun_client/frontend/src/components/review/some_deeply_nested_module_name.tsx';

const DIFF = `diff --git a/${LONG_PATH} b/${LONG_PATH}
index 111..222 100644
--- a/${LONG_PATH}
+++ b/${LONG_PATH}
@@ -1,2 +1,2 @@
 const a = 1;
-const c = 3;
+const c = 4;
`;

describe('FileIndex', () => {
    test('shows a long path in full rather than truncated', () => {
        const html = renderToStaticMarkup(
            <FileIndex parsed={parseDiff(DIFF)} onSelectFile={() => {}} />
        );
        expect(LONG_PATH.length).toBeGreaterThan(40);
        expect(html).toContain(`<span class="file-index-name">${LONG_PATH}</span>`);
        expect(html).not.toContain('…');
    });
});
