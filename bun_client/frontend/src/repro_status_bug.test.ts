import { expect, test, describe } from "bun:test";
import { parseDiff } from "./diff_utils";

describe("parseDiff - new file mode handling", () => {
    test("does not treat 'new file mode' as a file header", () => {
        const diff = `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..789
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+content
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        
        // Should only have ONE header for "new.txt"
        // NOT one for "mode 100644" and one for "new.txt"
        expect(fileHeaders.length).toBe(1);
        expect(fileHeaders[0].text).toBe("new.txt");
        expect(fileHeaders[0].fileStatus).toBe("new");
    });

    test("handles multiple new files correctly", () => {
         const diff = `diff --git a/file1.txt b/file1.txt
new file mode 100644
index 0000000..111
--- /dev/null
+++ b/file1.txt
@@ -0,0 +1 @@
+content1
diff --git a/file2.txt b/file2.txt
new file mode 100644
index 0000000..222
--- /dev/null
+++ b/file2.txt
@@ -0,0 +1 @@
+content2
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        
        expect(fileHeaders.length).toBe(2);
        
        expect(fileHeaders[0].text).toBe("file1.txt");
        expect(fileHeaders[0].fileStatus).toBe("new");

        expect(fileHeaders[1].text).toBe("file2.txt");
        expect(fileHeaders[1].fileStatus).toBe("new");
    });
});
