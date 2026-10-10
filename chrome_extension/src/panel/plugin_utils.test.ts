import { describe, expect, test } from 'bun:test';
import type { PluginOutput } from '../types';
import {
    pluginRunAction,
    pluginStatus,
    pluginStatusLabel,
    pluginStatusTone,
    resolvePluginBody,
    severityTone,
    sortAnnotations,
} from './plugin_utils';

function output(overrides: Partial<PluginOutput> = {}): PluginOutput {
    return {
        result: '',
        status: 'success',
        body: { body_type: 'markdown', body_content: 'ok' },
        annotations: null,
        ...overrides,
    };
}

describe('plugin status and button', () => {
    test.each([
        ['no entry', undefined, 'none', 'Not run yet', 'Run', false],
        ['deferred', output({ status: 'deferred' }), 'deferred', 'On demand', 'Run', false],
        ['pending', output({ status: 'pending' }), 'pending', 'Running', 'Running…', true],
        ['success', output(), 'success', 'Success', 'Re-run', false],
        ['error', output({ status: 'error' }), 'error', 'Failed', 'Re-run', false],
        ['upper case', output({ status: 'SUCCESS' }), 'success', 'Success', 'Re-run', false],
    ])('%s', (_name, out, status, label, button, disabled) => {
        expect(pluginStatus(out)).toBe(status);
        expect(pluginStatusLabel(status)).toBe(label);
        expect(pluginRunAction(out)).toEqual({
            label: button as 'Run' | 'Re-run' | 'Running…',
            disabled,
        });
    });

    test('tones', () => {
        expect(pluginStatusTone('success')).toBe('success');
        expect(pluginStatusTone('error')).toBe('danger');
        expect(pluginStatusTone('pending')).toBe('accent');
        expect(pluginStatusTone('deferred')).toBe('neutral');
        expect(pluginStatusTone('none')).toBe('neutral');
    });
});

describe('resolvePluginBody', () => {
    test('a known body type is used as is, even when empty', () => {
        expect(
            resolvePluginBody(output({ body: { body_type: 'HTML', body_content: '<b>x</b>' } }))
        ).toEqual({ body_type: 'html', body_content: '<b>x</b>' });
        expect(
            resolvePluginBody(
                output({ result: 'raw', body: { body_type: 'markdown', body_content: '' } })
            )
        ).toEqual({ body_type: 'markdown', body_content: '' });
    });

    test('otherwise the raw stdout, as markdown', () => {
        expect(
            resolvePluginBody(
                output({ result: 'plain text', body: { body_type: '', body_content: '' } })
            )
        ).toEqual({ body_type: 'markdown', body_content: 'plain text' });
        expect(resolvePluginBody(undefined)).toEqual({ body_type: 'markdown', body_content: '' });
    });
});

describe('annotations', () => {
    test('sorted by file, then line', () => {
        const sorted = sortAnnotations([
            { filename: 'b.go', line: 3, severity: 'info', content: '' },
            { filename: 'a.go', line: 10, severity: 'info', content: '' },
            { filename: 'a.go', line: 2, severity: 'info', content: '' },
        ]);
        expect(sorted.map(a => `${a.filename}:${a.line}`)).toEqual(['a.go:2', 'a.go:10', 'b.go:3']);
        expect(sortAnnotations(null)).toEqual([]);
    });

    test.each([
        ['error', 'danger'],
        ['Critical', 'danger'],
        ['warning', 'attention'],
        ['medium', 'attention'],
        ['info', 'accent'],
        ['nit', 'neutral'],
    ])('severity %s', (severity, tone) => {
        expect(severityTone(severity)).toBe(tone as ReturnType<typeof severityTone>);
    });
});
