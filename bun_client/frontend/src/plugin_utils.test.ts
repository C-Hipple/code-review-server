import { expect, test, describe } from 'bun:test';
import {
    annotationSeverityVariant,
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
