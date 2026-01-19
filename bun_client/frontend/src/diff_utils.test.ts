import { expect, test, describe } from "bun:test";
import { parseDiff } from "./diff_utils";

describe("parseDiff", () => {
    test("handles new empty file", () => {
        const diff = `diff --git a/newfile.txt b/newfile.txt
new file mode 100644
index 0000000..e69de29
`;
        const lines = parseDiff(diff);
        // Expect one file header
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(1);
        expect(fileHeaders[0].text).toBe("newfile.txt");
        expect(fileHeaders[0].fileStatus).toBe("new");
    });

    test("handles modified file followed by new empty file", () => {
        const diff = `diff --git a/mod.txt b/mod.txt
index 123..456 100644
--- a/mod.txt
+++ b/mod.txt
@@ -1 +1 @@
-foo
+bar
diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..789
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(2);
        expect(fileHeaders[0].text).toBe("mod.txt");
        expect(fileHeaders[1].text).toBe("new.txt");
        expect(fileHeaders[1].fileStatus).toBe("new");
    });

    test("handles new empty file followed by modified file", () => {
         const diff = `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..789
diff --git a/mod.txt b/mod.txt
index 123..456 100644
--- a/mod.txt
+++ b/mod.txt
@@ -1 +1 @@
-foo
+bar
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(2);
        
        expect(fileHeaders[0].text).toBe("new.txt");
        expect(fileHeaders[0].fileStatus).toBe("new");

        expect(fileHeaders[1].text).toBe("mod.txt");
        expect(fileHeaders[1].fileStatus).toBe("modified");
    });

    test("handles new empty file with spaces", () => {
        const diff = `diff --git a/my new file.txt b/my new file.txt
new file mode 100644
index 0000000..e69de29
`;
        const lines = parseDiff(diff);
        const fileHeaders = lines.filter(l => l.lineType === 'file-header');
        expect(fileHeaders.length).toBe(1);
        expect(fileHeaders[0].text).toBe("my new file.txt");
        expect(fileHeaders[0].fileStatus).toBe("new");
    });
});
