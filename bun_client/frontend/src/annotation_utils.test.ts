import { expect, test, describe } from 'bun:test';
import {
    annotationCommentBody,
    collectPRAnnotations,
    fileAnnotationKey,
    indexAnnotations,
    lineAnnotationKey,
    mostSevereVariant,
} from './annotation_utils';
import { parseDiff } from './diff_utils';
import type { PluginResult, PRAnnotation } from './plugin_utils';

// Head-side lines 1 (context) and 2 (added); the deletions only exist on the
// base side, the last of them at base line 4.
const DIFF = `diff --git a/app.py b/app.py
--- a/app.py
+++ b/app.py
@@ -1,5 +1,2 @@
 import os
+import sys
-import json
-import csv
-import io
`;

const annotation = (over: Partial<PRAnnotation> = {}): PRAnnotation => ({
    filename: 'app.py',
    line: 2,
    severity: 'warning',
    content: 'looks wrong',
    plugin: 'linter',
    ...over,
});

describe('collectPRAnnotations', () => {
    test('tags each annotation with its plugin, ordered by plugin name', () => {
        const plugins: Record<string, PluginResult> = {
            linter: {
                result: '',
                status: 'success',
                annotations: [{ filename: 'a.py', line: 1, severity: 'error', content: 'x' }],
            },
            auditor: {
                result: '',
                status: 'success',
                annotations: [{ filename: 'b.py', line: 2, severity: 'info', content: 'y' }],
            },
        };
        expect(collectPRAnnotations(plugins).map(a => `${a.plugin}:${a.filename}`)).toEqual([
            'auditor:b.py',
            'linter:a.py',
        ]);
    });

    test('ignores plugins that did not run successfully', () => {
        // Matches the server's aggregation, which only reads successful runs.
        const plugins: Record<string, PluginResult> = {
            failed: {
                result: '',
                status: 'error',
                annotations: [{ filename: 'a.py', line: 1, severity: 'error', content: 'x' }],
            },
            deferred: { result: '', status: 'deferred' },
        };
        expect(collectPRAnnotations(plugins)).toEqual([]);
    });

    test('ignores plugins with no annotations', () => {
        const plugins: Record<string, PluginResult> = {
            summarizer: { result: 'text summary', status: 'success' },
        };
        expect(collectPRAnnotations(plugins)).toEqual([]);
    });
});

describe('indexAnnotations', () => {
    const lines = parseDiff(DIFF);

    test('anchors an annotation to the head-side line number', () => {
        const index = indexAnnotations([annotation({ line: 2 })], lines);
        expect(index.byLine.get(lineAnnotationKey('app.py', 2))).toHaveLength(1);
        expect(index.count).toBe(1);
        expect(index.keys).toEqual([lineAnnotationKey('app.py', 2)]);
    });

    test('anchors to a context line, not just changed lines', () => {
        const index = indexAnnotations([annotation({ line: 1 })], lines);
        expect(index.byLine.get(lineAnnotationKey('app.py', 1))).toHaveLength(1);
    });

    test('groups multiple annotations on the same line', () => {
        const index = indexAnnotations(
            [
                annotation({ content: 'first' }),
                annotation({ plugin: 'auditor', content: 'second' }),
            ],
            lines
        );
        expect(index.byLine.get(lineAnnotationKey('app.py', 2))?.map(a => a.content)).toEqual([
            'first',
            'second',
        ]);
    });

    test('falls back to the file bucket when the line is not rendered', () => {
        // Line 90 is outside the hunk, so no row can carry it; it hangs off the
        // file header rather than disappearing.
        const index = indexAnnotations([annotation({ line: 90 })], lines);
        expect(index.byLine.size).toBe(0);
        expect(index.byFile.get(fileAnnotationKey('app.py'))).toHaveLength(1);
        expect(index.count).toBe(1);
    });

    test('sorts the file bucket by line then plugin', () => {
        const index = indexAnnotations(
            [
                annotation({ line: 90, plugin: 'linter' }),
                annotation({ line: 40, plugin: 'auditor' }),
                annotation({ line: 40, plugin: 'aardvark' }),
            ],
            lines
        );
        expect(
            index.byFile.get(fileAnnotationKey('app.py'))?.map(a => `${a.line}:${a.plugin}`)
        ).toEqual(['40:aardvark', '40:auditor', '90:linter']);
    });

    test('drops annotations for files that are not in the diff', () => {
        const index = indexAnnotations([annotation({ filename: 'other.py' })], lines);
        expect(index.count).toBe(0);
        expect(index.keys).toEqual([]);
    });

    test('does not anchor to a deleted line, whose number is base-side only', () => {
        // "import io" is line 4 on the base side and has no head-side number.
        const index = indexAnnotations([annotation({ line: 4 })], lines);
        expect(index.byLine.size).toBe(0);
        expect(index.byFile.size).toBe(1);
    });

    test('accepts a leading ./ on the annotation path', () => {
        const index = indexAnnotations([annotation({ filename: './app.py' })], lines);
        expect(index.byLine.get(lineAnnotationKey('app.py', 2))).toHaveLength(1);
    });

    test('is empty for an empty annotation list', () => {
        const index = indexAnnotations([], lines);
        expect(index.count).toBe(0);
        expect(index.keys).toEqual([]);
    });
});

describe('mostSevereVariant', () => {
    test('reports the worst severity in the group', () => {
        expect(
            mostSevereVariant([
                annotation({ severity: 'info' }),
                annotation({ severity: 'error' }),
                annotation({ severity: 'warning' }),
            ])
        ).toBe('danger');
    });

    test('falls back to neutral for free-form severities', () => {
        expect(mostSevereVariant([annotation({ severity: 'nitpick' })])).toBe('neutral');
        expect(mostSevereVariant([])).toBe('neutral');
    });
});

describe('annotationCommentBody', () => {
    test('attributes the comment to the plugin above the annotation text', () => {
        expect(
            annotationCommentBody(
                annotation({ plugin: 'security_check', content: 'unchecked err' })
            )
        ).toBe('Automated comment by security_check\n\nunchecked err');
    });

    test('trims the annotation text so the attribution line stays flush with it', () => {
        expect(annotationCommentBody(annotation({ content: '\n  looks wrong  \n' }))).toBe(
            'Automated comment by linter\n\nlooks wrong'
        );
    });

    test('is stable, so a re-rendered card can tell the annotation was adopted', () => {
        const a = annotation();
        expect(annotationCommentBody(a)).toBe(annotationCommentBody({ ...a }));
    });
});
