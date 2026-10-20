import { describe, expect, test } from 'bun:test';
import type { AIFeature, PluginConfig } from './types';
import { chipsFor } from './tool_chips';

function feature(overrides: Partial<AIFeature> & Pick<AIFeature, 'id'>): AIFeature {
    return {
        name: overrides.id,
        description: '',
        enabled: true,
        automatic: false,
        mode: 'oneshot',
        modes: ['oneshot'],
        provider: 'gemini',
        applied: false,
        ...overrides,
    };
}

function plugin(name: string): PluginConfig {
    return {
        Name: name,
        Command: name,
        IncludeDiff: true,
        IncludeHeaders: true,
        IncludeComments: false,
        IncludeBranch: false,
        OnlyOnDemand: false,
        Provider: '',
        Model: '',
    };
}

describe('chipsFor', () => {
    test('a chip per enabled feature, then per plugin', () => {
        const chips = chipsFor(
            [
                feature({ id: 'comments-addressed', name: 'Comments addressed?' }),
                feature({ id: 'change-diagram', name: 'Change diagram' }),
                feature({ id: 'test-gaps', name: 'Test gaps', enabled: false }),
            ],
            [plugin('security_check'), plugin('style_guidelines')]
        );
        expect(chips).toEqual([
            { kind: 'ai', id: 'comments-addressed', name: 'Comments addressed?' },
            { kind: 'ai', id: 'change-diagram', name: 'Change diagram' },
            { kind: 'plugin', id: 'security_check', name: 'security_check' },
            { kind: 'plugin', id: 'style_guidelines', name: 'style_guidelines' },
        ]);
    });

    test('a feature with no name falls back to its id', () => {
        expect(chipsFor([feature({ id: 'review-ease', name: '' })], [])).toEqual([
            { kind: 'ai', id: 'review-ease', name: 'review-ease' },
        ]);
    });

    test('nothing to offer makes no chips', () => {
        expect(chipsFor(null, null)).toEqual([]);
        expect(chipsFor([feature({ id: 'off', enabled: false })], null)).toEqual([]);
    });
});
