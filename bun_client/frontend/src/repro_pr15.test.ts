import { expect, test, describe } from "bun:test";
import { parseDiff } from "./diff_utils";

describe("repro PR 15", () => {
    test("handles new file tests/data/go_diff.code_review_server", () => {
        const diff = `diff --git a/tests/data/go_diff.code_review_server b/tests/data/go_diff.code_review_server
new file mode 100644
index 0000000..8e4d5a5
--- /dev/null
+++ b/tests/data/go_diff.code_review_server
@@ -0,0 +1,681 @@
+Project: * Review C-Hipple/gtdbot #9 *
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        
        expect(fileHeaders.length).toBe(1);
        expect(fileHeaders[0].text).toBe("tests/data/go_diff.code_review_server");
        expect(fileHeaders[0].fileStatus).toBe("new");
    });
});
