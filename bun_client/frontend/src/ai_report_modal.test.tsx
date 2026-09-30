import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import type { AIFeatureInfo, AIFeatureOutput, CommentsReport, ReportItem } from './ai_utils';
import AIReportModal from './components/AIReportModal';

const feature: AIFeatureInfo = {
    id: 'comments-addressed',
    name: 'Comments addressed?',
    description: '',
    enabled: true,
    automatic: false,
    mode: 'oneshot',
    modes: ['oneshot', 'agent'],
    provider: 'gemini',
};

const outstanding: ReportItem = {
    root_comment_id: '5001',
    thread_id: 'PRRT_5001',
    kind: 'thread',
    status: 'outstanding',
    source: 'github',
    rationale: 'Unresolved on GitHub, and bob replied after the latest commit.',
    author: 'bob',
    excerpt: 'Should punctuation have a default?',
    path: 'src/greet.ts',
    line: 3,
    outdated: false,
    resolved: false,
    replies: 1,
    last_author: 'bob',
    last_activity: '2026-09-01T12:00:00Z',
    html_url: 'https://github.com/acme/widgets/pull/42#discussion_r5001',
};

const addressedByModel: ReportItem = {
    ...outstanding,
    root_comment_id: '9001',
    thread_id: null,
    kind: 'conversation',
    status: 'addressed',
    source: 'model',
    rationale: 'The README now documents the flag.',
    author: 'dave',
    excerpt: 'Please mention the flag in the README.',
    path: undefined,
    line: undefined,
};

const report: CommentsReport = {
    verdict: 'outstanding',
    summary: '1 outstanding of 2 item(s); 1 addressed; carol still requests changes.',
    counts: { total: 2, addressed: 1, outstanding: 1, unclear: 0, by_model: 1 },
    items: [outstanding, addressedByModel],
    change_requests: [
        {
            reviewer: 'carol',
            review_id: 701,
            submitted_at: '2026-09-01T11:00:00Z',
            excerpt: 'Please add tests.',
        },
    ],
    model: { consulted: true, provider: 'gemini', mode: 'oneshot', asked: 1 },
    truncated: false,
    missing: [],
};

function output(fields: Partial<AIFeatureOutput> = {}): AIFeatureOutput {
    return {
        feature: 'comments-addressed',
        name: 'Comments addressed?',
        status: 'success',
        body: { body_type: 'markdown', body_content: '**1 outstanding**' },
        annotations: [],
        report,
        outstanding: [outstanding],
        covers_sha: 'sha-1',
        covers_digest: 'd1',
        current_sha: 'sha-1',
        current_digest: 'd1',
        stale: false,
        truncated: false,
        updated_at: new Date().toISOString(),
        ...fields,
    };
}

const render = (initialOutput: AIFeatureOutput, f: AIFeatureInfo = feature) =>
    renderToStaticMarkup(
        <AIReportModal
            feature={f}
            owner="acme"
            repo="widgets"
            number={42}
            initialOutput={initialOutput}
            onClose={() => {}}
            onOutput={() => {}}
            onJumpToItem={() => {}}
        />
    );

describe('AIReportModal', () => {
    test('shows the verdict, what needs attention, and who decided each item', () => {
        const html = render(output());
        expect(html).toContain('Comments addressed?');
        expect(html).toContain(report.summary);
        expect(html).toContain('Needs attention (1)');
        expect(html).toContain('Should punctuation have a default?');
        expect(html).toContain('src/greet.ts:3');
        expect(html).toContain('Show this thread in the diff');
        expect(html).toContain('GitHub');
        expect(html).toContain('Changes requested (1)');
        expect(html).toContain('carol');
        // Addressed items start collapsed.
        expect(html).toContain('Addressed (1)');
        expect(html).not.toContain('Please mention the flag in the README.');
        expect(html).toContain('The model judged 1 item(s)');
    });

    test('warns when the report no longer describes the PR', () => {
        const html = render(output({ stale: true }));
        expect(html).toContain('Out of date');
        expect(html).toContain('The PR has changed since this report was made');
    });

    test('keeps the previous result on screen while a run refreshes it', () => {
        const html = render(output({ status: 'pending' }));
        expect(html).toContain('Running');
        expect(html).toContain('refreshing');
        expect(html).toContain('Should punctuation have a default?');
    });

    test('says it is working when there is no result yet', () => {
        const html = render(
            output({ status: 'pending', report: null, outstanding: null, updated_at: '' })
        );
        expect(html).toContain('Working it out');
    });

    test('lists the missing input of an insufficient-input report', () => {
        const html = render(
            output({
                status: 'insufficient-input',
                report: {
                    ...report,
                    verdict: 'insufficient-input',
                    missing: ["GitHub's review-thread resolution state is unavailable"],
                },
            })
        );
        expect(html).toContain('Insufficient input');
        expect(html).toContain('Missing input:');
    });

    test('draws the change diagram in a modal that takes most of the screen', () => {
        const diagramFeature = { ...feature, id: 'change-diagram', name: 'Change diagram' };
        const html = render(
            output({
                feature: 'change-diagram',
                name: 'Change diagram',
                body: {
                    body_type: 'markdown',
                    body_content: '```mermaid\nflowchart TD\n  a --> b\n```\n',
                },
                report: { mermaid: 'flowchart TD\n  a --> b', diagram_type: 'flowchart' },
                outstanding: null,
            }),
            diagramFeature
        );
        expect(html).toContain('max-width:96vw');
        expect(html).toContain('height:92vh');
        expect(html).toContain('data-testid="mermaid-diagram"');
        // mermaid draws in an effect, after this static render.
        expect(html).toContain('Drawing the diagram');
        expect(html).toContain('Copy source');
        expect(html).toContain('Added');
        expect(html).toContain('Removed');
        expect(html).not.toContain('<code');

        // A failed run has no diagram: its body says why.
        const failed = render(
            output({
                feature: 'change-diagram',
                name: 'Change diagram',
                status: 'error',
                body: { body_type: 'markdown', body_content: '**Change diagram failed.**' },
                report: null,
                outstanding: null,
            }),
            diagramFeature
        );
        expect(failed).not.toContain('mermaid-diagram');
        expect(failed).toContain('<strong>Change diagram failed.</strong>');

        // Every other report opens at the usual width.
        expect(render(output())).toContain('max-width:1000px');
    });

    test("renders another feature's markdown body", () => {
        const html = render(
            output({
                feature: 'mermaid',
                name: 'Diagram',
                report: null,
                outstanding: null,
                body: { body_type: 'markdown', body_content: '## Flow\n\nIt goes **left**.' },
            }),
            { ...feature, id: 'mermaid', name: 'Diagram' }
        );
        expect(html).toContain('<h2>Flow</h2>');
        expect(html).toContain('<strong>left</strong>');
    });
});
