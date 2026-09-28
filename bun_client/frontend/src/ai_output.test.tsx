import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import type { AIFeatureInfo, AIFeatureOutput, CommentsReport, ReportItem } from './ai_utils';
import AIOutput, { AIFeatureCard } from './components/AIOutput';

const feature: AIFeatureInfo = {
    id: 'comments-addressed',
    name: 'Comments addressed?',
    description: 'Reports which review comments are still outstanding.',
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

const report: CommentsReport = {
    verdict: 'outstanding',
    summary: '1 outstanding of 1 item(s); 0 addressed.',
    counts: { total: 1, addressed: 0, outstanding: 1, unclear: 0, by_model: 0 },
    items: [outstanding],
    change_requests: [],
    model: { consulted: false, mode: 'oneshot', asked: 0 },
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

const renderCard = (initialOutput?: AIFeatureOutput) =>
    renderToStaticMarkup(
        <AIFeatureCard
            feature={feature}
            owner="acme"
            repo="widgets"
            number={42}
            initialOutput={initialOutput}
        />
    );

describe('AIOutput', () => {
    test('names the PR and offers the review while the features load', () => {
        const html = renderToStaticMarkup(
            <AIOutput owner="acme" repo="widgets" number={42} onOpenReview={() => {}} />
        );
        expect(html).toContain('AI Reports for acme/widgets #42');
        expect(html).toContain('Loading AI features…');
        expect(html).toContain('Open review');
        expect(html).toContain('Refresh');
        // Only a page with somewhere to go back to offers to close.
        expect(html).not.toContain('Close (Esc)');
    });
});

describe('AIFeatureCard', () => {
    test('shows the feature and its report', () => {
        const html = renderCard(output());
        expect(html).toContain('Comments addressed?');
        expect(html).toContain('Reports which review comments are still outstanding.');
        expect(html).toContain('↻ Re-run');
        expect(html).toContain(report.summary);
        expect(html).toContain('Needs attention (1)');
        expect(html).toContain('Should punctuation have a default?');
        expect(html).toContain('GitHub ↗');
    });

    test('shows locations as plain text, with no diff to jump into', () => {
        const html = renderCard(output());
        expect(html).toContain('src/greet.ts:3');
        expect(html).not.toContain('Show this thread in the diff');
    });

    test('reads as running while a run is in flight', () => {
        const html = renderCard(output({ status: 'pending' }));
        expect(html).toContain('Running…');
        expect(html).toContain('refreshing');
        expect(html).toContain('Should punctuation have a default?');
    });

    test('says it is loading before the first output lands', () => {
        const html = renderCard();
        expect(html).toContain('Loading…');
        expect(html).toContain('↻ Re-run');
    });
});
