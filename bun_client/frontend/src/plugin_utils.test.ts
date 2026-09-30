import { expect, test, describe } from 'bun:test';
import {
    annotationSeverityVariant,
    canRerunPlugin,
    collapsePluginsByDefault,
    COLLAPSE_PLUGINS_OVER_LINES,
    pluginOutputLineCount,
    resolvePluginBody,
    sortedAnnotations,
    totalAnnotationCount,
} from './plugin_utils';
import type { PluginResult } from './plugin_utils';

describe('resolvePluginBody', () => {
    test('uses the parsed markdown body when the server sends one', () => {
        const plugin: PluginResult = {
            result: '{"body":{"body_type":"markdown","body_content":"# Summary"}}',
            status: 'success',
            body: { body_type: 'markdown', body_content: '# Summary' },
        };
        expect(resolvePluginBody(plugin)).toEqual({
            body_type: 'markdown',
            body_content: '# Summary',
        });
    });

    test('uses the parsed html body when the server sends one', () => {
        const plugin: PluginResult = {
            result: 'raw',
            status: 'success',
            body: { body_type: 'html', body_content: '<p>hi</p>' },
        };
        expect(resolvePluginBody(plugin)).toEqual({
            body_type: 'html',
            body_content: '<p>hi</p>',
        });
    });

    test('falls back to the raw result as markdown when no body is present', () => {
        // A server predating the response contract only sends result/status.
        const plugin: PluginResult = { result: 'plain text summary', status: 'success' };
        expect(resolvePluginBody(plugin)).toEqual({
            body_type: 'markdown',
            body_content: 'plain text summary',
        });
    });

    test('falls back to the raw result when body_type is unrecognized', () => {
        const plugin = {
            result: 'plain text summary',
            status: 'success',
            body: { body_type: 'plaintext', body_content: 'ignored' },
        } as unknown as PluginResult;
        expect(resolvePluginBody(plugin)).toEqual({
            body_type: 'markdown',
            body_content: 'plain text summary',
        });
    });

    test('normalizes body_type casing', () => {
        const plugin = {
            result: '',
            status: 'success',
            body: { body_type: 'HTML', body_content: '<b>x</b>' },
        } as unknown as PluginResult;
        expect(resolvePluginBody(plugin).body_type).toBe('html');
    });

    test('keeps an empty body when the plugin only produced annotations', () => {
        // An annotations-only plugin's raw result is the contract JSON, which
        // must not leak into the rendered body.
        const plugin: PluginResult = {
            result: '{"body":{"body_type":"markdown","body_content":""},"annotations":[]}',
            status: 'success',
            body: { body_type: 'markdown', body_content: '' },
            annotations: [{ filename: 'a.py', line: 4, severity: 'warning', content: 'x' }],
        };
        expect(resolvePluginBody(plugin).body_content).toBe('');
    });

    test('handles a pending plugin with no output at all', () => {
        const plugin: PluginResult = { result: '', status: 'pending' };
        expect(resolvePluginBody(plugin)).toEqual({ body_type: 'markdown', body_content: '' });
    });
});

describe('sortedAnnotations', () => {
    test('groups by filename then orders by line', () => {
        const plugin: PluginResult = {
            result: '',
            status: 'success',
            body: { body_type: 'markdown', body_content: '' },
            annotations: [
                { filename: 'b.py', line: 2, severity: 'info', content: 'b2' },
                { filename: 'a.py', line: 75, severity: 'warning', content: 'a75' },
                { filename: 'a.py', line: 7, severity: 'error', content: 'a7' },
            ],
        };
        expect(sortedAnnotations(plugin).map(a => `${a.filename}:${a.line}`)).toEqual([
            'a.py:7',
            'a.py:75',
            'b.py:2',
        ]);
    });

    test('returns an empty list when annotations are absent', () => {
        expect(sortedAnnotations({ result: 'x', status: 'success' })).toEqual([]);
    });

    test('does not mutate the original array', () => {
        const annotations = [
            { filename: 'z.py', line: 1, severity: '', content: 'z' },
            { filename: 'a.py', line: 1, severity: '', content: 'a' },
        ];
        sortedAnnotations({ result: '', status: 'success', annotations });
        expect(annotations[0].filename).toBe('z.py');
    });
});

describe('annotationSeverityVariant', () => {
    test('maps known severities', () => {
        expect(annotationSeverityVariant('error')).toBe('danger');
        expect(annotationSeverityVariant('CRITICAL')).toBe('danger');
        expect(annotationSeverityVariant('warning')).toBe('warning');
        expect(annotationSeverityVariant('warn')).toBe('warning');
        expect(annotationSeverityVariant('info')).toBe('info');
        expect(annotationSeverityVariant('note')).toBe('info');
    });

    test('falls back to neutral for free-form severities', () => {
        expect(annotationSeverityVariant('nitpick')).toBe('neutral');
        expect(annotationSeverityVariant('')).toBe('neutral');
    });
});

describe('canRerunPlugin', () => {
    test('offers a re-run for plugins that finished executing', () => {
        expect(canRerunPlugin({ result: 'summary', status: 'success' })).toBe(true);
        expect(canRerunPlugin({ result: 'Error: boom', status: 'error' })).toBe(true);
    });

    test('does not offer a re-run for a deferred plugin that never ran', () => {
        expect(canRerunPlugin({ result: '', status: 'deferred' })).toBe(false);
        expect(canRerunPlugin({ result: '', status: 'DEFERRED' })).toBe(false);
    });

    test('does not offer a re-run while a plugin is still in flight', () => {
        expect(canRerunPlugin({ result: '', status: 'pending' })).toBe(false);
    });

    test('treats a missing status as not re-runnable', () => {
        expect(canRerunPlugin({ result: '', status: '' })).toBe(false);
    });
});

describe('totalAnnotationCount', () => {
    test('sums annotations across plugins and ignores plugins without any', () => {
        const plugins: Record<string, PluginResult> = {
            linter: {
                result: '',
                status: 'success',
                annotations: [
                    { filename: 'a.py', line: 1, severity: 'error', content: 'x' },
                    { filename: 'a.py', line: 2, severity: 'error', content: 'y' },
                ],
            },
            summarizer: { result: 'text', status: 'success' },
        };
        expect(totalAnnotationCount(plugins)).toBe(2);
    });

    test('is zero with no plugins', () => {
        expect(totalAnnotationCount({})).toBe(0);
    });
});

/** A plugin whose markdown body is `lines` lines long. */
function linesOfOutput(lines: number): PluginResult {
    const content = Array.from({ length: lines }, (_, i) => `line ${i + 1}`).join('\n');
    return {
        result: content,
        status: 'success',
        body: { body_type: 'markdown', body_content: content },
    };
}

describe('pluginOutputLineCount', () => {
    test('counts the lines of the body', () => {
        expect(pluginOutputLineCount(linesOfOutput(12))).toBe(12);
    });

    test('does not count a trailing newline as a line', () => {
        expect(pluginOutputLineCount({ result: 'one\ntwo\n\n', status: 'success' })).toBe(2);
    });

    test('is zero for an empty body', () => {
        expect(pluginOutputLineCount({ result: '', status: 'pending' })).toBe(0);
    });

    test('counts the raw result when there is no parsed body', () => {
        expect(pluginOutputLineCount({ result: 'a\nb\nc', status: 'success' })).toBe(3);
    });

    test('adds one line per annotation', () => {
        const plugin: PluginResult = {
            result: '',
            status: 'success',
            body: { body_type: 'markdown', body_content: 'Found two issues.' },
            annotations: [
                { filename: 'a.py', line: 1, severity: 'error', content: 'x' },
                { filename: 'a.py', line: 2, severity: 'error', content: 'y' },
            ],
        };
        expect(pluginOutputLineCount(plugin)).toBe(3);
    });
});

describe('collapsePluginsByDefault', () => {
    test('keeps output up to the limit expanded', () => {
        expect(
            collapsePluginsByDefault({
                summarize: linesOfOutput(COLLAPSE_PLUGINS_OVER_LINES - 100),
                security: linesOfOutput(100),
            })
        ).toBe(false);
    });

    test('collapses once the output of every plugin together is over the limit', () => {
        // Neither plugin is over the limit on its own.
        expect(
            collapsePluginsByDefault({
                summarize: linesOfOutput(COLLAPSE_PLUGINS_OVER_LINES - 100),
                security: linesOfOutput(101),
            })
        ).toBe(true);
    });

    test('is false with no plugins', () => {
        expect(collapsePluginsByDefault({})).toBe(false);
    });
});
