import { describe, expect, test } from 'bun:test';
import type { AIFeature, AIFeatureOutput, PRMetadata } from '../types';
import {
    aiRunAction,
    aiStatusLabel,
    aiStatusTone,
    appliedFeatures,
    bodyPreview,
    changeClassesUsed,
    changeDiagramSource,
    disabledFeatures,
    fileOrderingFiles,
    hasResult,
    reportFeatures,
    reviewEaseOf,
    runOutcomeNote,
    svgSize,
} from './ai_utils';

function output(overrides: Partial<AIFeatureOutput> = {}): AIFeatureOutput {
    return {
        feature: 'comments-addressed',
        name: 'Comments addressed?',
        status: 'success',
        body: { body_type: 'markdown', body_content: 'All good.' },
        annotations: null,
        report: null,
        outstanding: null,
        covers_sha: 'abc',
        covers_digest: 'd',
        current_sha: 'abc',
        current_digest: 'd',
        stale: false,
        truncated: false,
        updated_at: '2026-10-10T10:00:00Z',
        ...overrides,
    };
}

function feature(id: string, overrides: Partial<AIFeature> = {}): AIFeature {
    return {
        id,
        name: id,
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

describe('aiRunAction: Run vs Re-run', () => {
    test.each([
        ['no output yet', undefined, { label: 'Run', force: false, disabled: false }],
        [
            'never run',
            output({ status: 'not-run', updated_at: '' }),
            { label: 'Run', force: false, disabled: false },
        ],
        ['stale', output({ stale: true }), { label: 'Run', force: false, disabled: false }],
        ['failed', output({ status: 'error' }), { label: 'Run', force: false, disabled: false }],
        ['current result', output(), { label: 'Re-run', force: true, disabled: false }],
        [
            'insufficient input',
            output({ status: 'insufficient-input' }),
            { label: 'Re-run', force: true, disabled: false },
        ],
        [
            'running',
            output({ status: 'pending' }),
            { label: 'Running…', force: false, disabled: true },
        ],
        [
            'running over a stale result',
            output({ status: 'pending', stale: true }),
            { label: 'Running…', force: false, disabled: true },
        ],
    ])('%s', (_name, out, expected) => {
        expect(aiRunAction(out)).toEqual(expected as ReturnType<typeof aiRunAction>);
    });
});

describe('status labels and tones', () => {
    test.each([
        ['not-run', 'Not run', 'neutral'],
        ['pending', 'Running', 'accent'],
        ['success', 'Success', 'success'],
        ['error', 'Failed', 'danger'],
        ['insufficient-input', 'Insufficient input', 'attention'],
        ['something-new', 'something-new', 'neutral'],
    ])('%s', (status, label, tone) => {
        expect(aiStatusLabel(status)).toBe(label);
        expect(aiStatusTone(status)).toBe(tone as ReturnType<typeof aiStatusTone>);
    });

    test('hasResult', () => {
        expect(hasResult(undefined)).toBe(false);
        expect(hasResult(output({ status: 'not-run', updated_at: '' }))).toBe(false);
        expect(hasResult(output({ status: 'pending', updated_at: '' }))).toBe(false);
        expect(hasResult(output({ status: 'pending' }))).toBe(true);
        expect(hasResult(output({ status: 'error' }))).toBe(true);
    });

    test('runOutcomeNote', () => {
        expect(runOutcomeNote('up-to-date')).toContain('Already up to date');
        expect(runOutcomeNote('already-running')).toContain('already in progress');
        expect(runOutcomeNote('started')).toBe('');
    });
});

describe('feature lists', () => {
    const features = [
        feature('comments-addressed'),
        feature('change-diagram'),
        feature('file-ordering', { applied: true }),
        feature('review-ease', { applied: true }),
        feature('feature-flags', { enabled: false }),
        feature('review-ease-off', { enabled: false, applied: true }),
    ];

    test('report cards: enabled and not applied', () => {
        expect(reportFeatures(features).map(f => f.id)).toEqual([
            'comments-addressed',
            'change-diagram',
        ]);
    });

    test('applied: enabled and applied', () => {
        expect(appliedFeatures(features).map(f => f.id)).toEqual(['file-ordering', 'review-ease']);
    });

    test('disabled', () => {
        expect(disabledFeatures(features).map(f => f.id)).toEqual([
            'feature-flags',
            'review-ease-off',
        ]);
    });
});

describe('reports', () => {
    test('changeDiagramSource', () => {
        const diagram = output({
            feature: 'change-diagram',
            report: { mermaid: 'flowchart LR\n A --> B', diagram_type: 'flowchart' },
        });
        expect(changeDiagramSource(diagram)).toBe('flowchart LR\n A --> B');
        expect(
            changeDiagramSource(output({ feature: 'change-diagram', report: { mermaid: '  ' } }))
        ).toBeNull();
        expect(changeDiagramSource(output({ feature: 'change-diagram' }))).toBeNull();
        expect(changeDiagramSource(output({ report: { mermaid: 'flowchart LR' } }))).toBeNull();
    });

    test('fileOrderingFiles', () => {
        const ordering = (report: unknown) => output({ feature: 'file-ordering', report });
        expect(fileOrderingFiles(ordering({ files: ['b.go', 'a.go'] }))).toEqual(['b.go', 'a.go']);
        expect(fileOrderingFiles(ordering({ files: ['a.go', 3, ''] }))).toEqual(['a.go']);
        expect(fileOrderingFiles(ordering({ files: [] }))).toBeNull();
        expect(fileOrderingFiles(ordering(null))).toBeNull();
        expect(fileOrderingFiles(undefined)).toBeNull();
    });

    test('reviewEaseOf: metadata first, then the review-ease report', () => {
        const meta = { review_ease: 'easy' } as PRMetadata;
        const outputs = {
            'review-ease': output({ feature: 'review-ease', report: { rating: 'hard' } }),
        };
        expect(reviewEaseOf(meta, outputs)).toBe('easy');
        expect(reviewEaseOf({ review_ease: '' } as PRMetadata, outputs)).toBe('hard');
        expect(reviewEaseOf(null, null)).toBe('');
        expect(
            reviewEaseOf(null, { 'review-ease': output({ report: { rating: 'trivial' } }) })
        ).toBe('');
    });

    test('changeClassesUsed: only classes nodes use, in legend order', () => {
        const source = [
            'flowchart LR',
            '  A[Router]:::changed --> B[Limiter]:::added',
            '  C[Old]',
            '  class C removed',
            '  classDef added fill:#dcfce7',
            '  classDef changed fill:#fef3c7',
            '  classDef removed fill:#fee2e2',
        ].join('\n');
        expect(changeClassesUsed(source)).toEqual(['added', 'changed', 'removed']);
        expect(changeClassesUsed('flowchart LR\n A:::added --> B\n classDef changed x')).toEqual([
            'added',
        ]);
        expect(changeClassesUsed('flowchart LR\n A --> B')).toEqual([]);
    });

    test('svgSize reads the root viewBox', () => {
        expect(svgSize('<svg id="x" viewBox="-8 -8 843.5 260"><g/></svg>')).toEqual({
            width: 843.5,
            height: 260,
        });
        expect(svgSize('<svg width="10"></svg>')).toBeNull();
        expect(svgSize('<svg viewBox="0 0 0 10"></svg>')).toBeNull();
    });
});

describe('bodyPreview', () => {
    test('the first line of prose, without markdown', () => {
        expect(
            bodyPreview('### Heading\n\n**2 of 5** changes run with `flags` off.\n\nMore.')
        ).toBe('2 of 5 changes run with flags off.');
        expect(bodyPreview('- [link](https://x) first item\n- second')).toBe('link first item');
        expect(bodyPreview('```\ncode\n```\n| a | b |\n|---|---|\nText')).toBe('Text');
        expect(bodyPreview('')).toBe('');
    });

    test('keeps the underscores inside identifiers', () => {
        expect(bodyPreview('Run 1 of `security_check`: _no_ new __rate_limit_v2__ findings.')).toBe(
            'Run 1 of security_check: no new rate_limit_v2 findings.'
        );
    });

    test('cut to length', () => {
        const preview = bodyPreview('word '.repeat(100), 20);
        expect(preview.length).toBeLessThanOrEqual(20);
        expect(preview.endsWith('…')).toBe(true);
    });
});
