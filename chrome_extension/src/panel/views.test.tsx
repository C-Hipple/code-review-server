// Static renders of the panel's components (no DOM, no effects): what each
// shows for a given set of server replies.

import { beforeAll, describe, expect, test } from 'bun:test';
import type { ReactElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { RpcError } from '../rpc';
import type {
    AIFeature,
    AIFeatureOutput,
    PluginConfig,
    PluginOutput,
    PRPayload,
    ReviewItem,
} from '../types';
import { NO_ACTIONS, type ActionState } from './actions';
import { AIFeatureCard } from './AIFeatureCard';
import { Annotations } from './Annotations';
import { PanelContext, type PanelEnv } from './context';
import { primeDiffAnchors } from './diff_links';
import { ErrorCard, errorTitle } from './ErrorCard';
import { FILE_ORDERING_NOTE, FileOrderingCard } from './FileOrderingCard';
import { Header } from './Header';
import { PluginCard } from './PluginCard';
import { PRToolsView, type PRToolsData, type PRToolsHandlers } from './PRToolsView';
import { loaded, loading } from './resource';
import { ReviewList } from './ReviewList';
import { ReviewRow } from './ReviewRow';

const PR = { owner: 'acme', repo: 'widgets', number: 42 };
const NOW = Date.parse('2026-10-10T12:00:00Z');
const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString();
const EMBEDDED: PanelEnv = { standalone: false, target: '_top', currentPr: PR };
const STANDALONE: PanelEnv = { standalone: true, target: '_blank', currentPr: null };

function render(node: ReactElement, env: PanelEnv = EMBEDDED): string {
    return renderToStaticMarkup(<PanelContext.Provider value={env}>{node}</PanelContext.Provider>);
}

/** The markup's text, tags dropped and entities decoded. */
function text(html: string): string {
    return html
        .replace(/<[^>]+>/g, ' ')
        .replace(/&#x27;/g, "'")
        .replace(/&quot;/g, '"')
        .replace(/&amp;/g, '&')
        .replace(/\s+/g, ' ')
        .trim();
}

const noop = () => {};

// --- Fixtures -----------------------------------------------------------------

function item(overrides: Partial<ReviewItem>): ReviewItem {
    return {
        section: 'Needs my review',
        section_priority: 1,
        status: 'TODO',
        tags: 'widgets',
        title: 'Rate-limit the public API',
        owner: 'acme',
        repo: 'widgets',
        number: 42,
        author: 'mona',
        url: 'https://github.com/acme/widgets/pull/42',
        release_status: '',
        review_ease: '',
        created_at: minutesAgo(300),
        required_teams: null,
        comment_count: 0,
        merge_conflicts: false,
        ...overrides,
    };
}

function feature(id: string, name: string, overrides: Partial<AIFeature> = {}): AIFeature {
    return {
        id,
        name,
        description: `What ${name} does.`,
        enabled: true,
        automatic: false,
        mode: 'oneshot',
        modes: ['oneshot'],
        provider: 'gemini',
        applied: false,
        ...overrides,
    };
}

function aiOutput(featureId: string, overrides: Partial<AIFeatureOutput> = {}): AIFeatureOutput {
    return {
        feature: featureId,
        name: featureId,
        status: 'success',
        body: { body_type: 'markdown', body_content: '' },
        annotations: null,
        report: null,
        outstanding: null,
        covers_sha: 'abc',
        covers_digest: 'd',
        current_sha: 'abc',
        current_digest: 'd',
        stale: false,
        truncated: false,
        updated_at: minutesAgo(5),
        ...overrides,
    };
}

const FEATURES: AIFeature[] = [
    feature('comments-addressed', 'Comments addressed?'),
    feature('feature-flags', 'Behind a flag?'),
    feature('change-diagram', 'Change diagram'),
    feature('file-ordering', 'File ordering', { applied: true }),
    feature('review-ease', 'Review ease', { applied: true }),
    feature('test-gaps', 'Test gaps', { enabled: false }),
    feature('summary', 'Summary', { enabled: false }),
];

const FILES = ['internal/api/router.go', 'internal/ratelimit/limiter.go', 'docs/api.md'];

const AI_OUTPUTS: Record<string, AIFeatureOutput> = {
    'comments-addressed': aiOutput('comments-addressed', {
        body: {
            body_type: 'markdown',
            body_content: '**1 of 4 threads** is outstanding.\n\n| a | b |\n|---|---|\n| x | y |',
        },
        annotations: [
            {
                source: 'ai',
                feature: 'comments-addressed',
                filename: 'internal/ratelimit/limiter.go',
                line: 57,
                severity: 'warning',
                content: 'Outstanding thread',
            },
        ],
    }),
    'feature-flags': aiOutput('feature-flags', {
        stale: true,
        truncated: true,
        body: { body_type: 'markdown', body_content: '2 of 5 changes run unflagged.' },
    }),
    'change-diagram': aiOutput('change-diagram', {
        report: { mermaid: 'flowchart LR\n  A:::added --> B:::changed', diagram_type: 'flowchart' },
    }),
    'file-ordering': aiOutput('file-ordering', { report: { files: FILES } }),
    'review-ease': aiOutput('review-ease', { report: { rating: 'hard' } }),
};

const PLUGINS: PluginConfig[] = [
    {
        Name: 'security_check',
        Command: 'security_check --strict',
        IncludeDiff: true,
        IncludeHeaders: false,
        IncludeComments: false,
        IncludeBranch: false,
        OnlyOnDemand: false,
        Provider: '',
        Model: '',
    },
    {
        Name: 'style_guidelines',
        Command: 'style_guidelines',
        IncludeDiff: true,
        IncludeHeaders: false,
        IncludeComments: false,
        IncludeBranch: false,
        OnlyOnDemand: true,
        Provider: '',
        Model: '',
    },
];

const PLUGIN_OUTPUTS: Record<string, PluginOutput> = {
    security_check: {
        result: '',
        status: 'success',
        body: { body_type: 'markdown', body_content: '### Security review\n\nFound **2 issues**.' },
        annotations: [
            {
                filename: 'internal/ratelimit/store.go',
                line: 42,
                severity: 'warning',
                content: 'Unbounded keys',
            },
            {
                filename: 'internal/ratelimit/limiter.go',
                line: 88,
                severity: 'error',
                content: 'Token logged',
            },
        ],
    },
    style_guidelines: {
        result: '',
        status: 'deferred',
        body: { body_type: 'markdown', body_content: '' },
        annotations: null,
    },
};

const PAYLOAD = {
    okay: true,
    metadata: {
        number: 42,
        title: 'Rate-limit the public API per token',
        author: 'mona',
        base_ref: 'main',
        head_ref: 'mona/limits',
        state: 'open',
        draft: false,
        review_ease: 'hard',
        changed_files: 7,
        additions: 412,
        deletions: 58,
        url: 'https://github.com/acme/widgets/pull/42',
    },
    annotations: [],
} as unknown as PRPayload;

const HANDLERS: PRToolsHandlers = {
    onRetryAll: noop,
    onRetryPR: noop,
    onRetryFeatures: noop,
    onRetryPlugins: noop,
    onRetryOutputs: noop,
    onSync: noop,
    onRunFeature: noop,
    onRunPlugin: noop,
    onRerunAll: noop,
};

const DATA: PRToolsData = {
    payload: loaded(PAYLOAD),
    features: loaded(FEATURES),
    plugins: loaded(PLUGINS),
    aiOutputs: loaded(AI_OUTPUTS),
    pluginOutputs: loaded(PLUGIN_OUTPUTS),
};

beforeAll(async () => {
    // Links render with their #diff- anchors once the hashes are cached.
    await primeDiffAnchors([
        ...FILES,
        'internal/ratelimit/store.go',
        'internal/ratelimit/limiter.go',
    ]);
});

async function anchor(path: string): Promise<string> {
    return `diff-${new Bun.CryptoHasher('sha256').update(path).digest('hex')}`;
}

// --- Error card ---------------------------------------------------------------

describe('ErrorCard', () => {
    test('host-missing: the install steps and the extension id', () => {
        const html = render(
            <ErrorCard
                variant="panel"
                error={new RpcError('host-missing', 'Specified native messaging host not found.')}
                extensionId="acmghogknbbihjoejbkejhhikiapmiib"
                onRetry={noop}
            />
        );
        expect(html).toContain('role="alert"');
        expect(text(html)).toContain('Connect the extension to code-review-server');
        expect(html).toContain('chrome_extension/crs_native_host/install.sh');
        expect(html).toContain('--capture-env');
        expect(html).toContain('acmghogknbbihjoejbkejhhikiapmiib');
        expect(text(html)).toContain('Retry');
    });

    test('host-forbidden: install with this extension id', () => {
        const html = render(
            <ErrorCard
                variant="panel"
                error={new RpcError('host-forbidden', 'Access forbidden.')}
                extensionId="abc"
            />
        );
        expect(text(html)).toContain("The native host doesn't allow this extension");
        expect(html).toContain('install.sh --extension-id abc');
    });

    test('server: the host message and log path', () => {
        const html = render(
            <ErrorCard
                variant="panel"
                error={new RpcError('server', 'exit status 1', '/home/mona/.crs/native_host.log')}
                onRetry={noop}
            />
        );
        expect(text(html)).toContain("The server isn't running");
        expect(text(html)).toContain('exit status 1');
        expect(html).toContain('/home/mona/.crs/native_host.log');
        expect(html).toContain('native_host.env');
    });

    test('inline: what failed, the message and a Retry', () => {
        const html = render(
            <ErrorCard
                error={new RpcError('rpc', 'pull request not found')}
                what="the plugins"
                onRetry={noop}
            />
        );
        expect(html).toContain('class="error-inline"');
        expect(text(html)).toContain("Couldn't load the plugins pull request not found Retry");
    });

    test('inline without a retry has no button', () => {
        expect(render(<ErrorCard error={new RpcError('rpc', 'x')} />)).not.toContain('<button');
    });

    test.each([
        ['timeout', "The server didn't answer in time"],
        ['disconnected', 'Lost the connection to the server'],
        ['extension', 'Something went wrong'],
    ] as const)('%s title', (kind, title) => {
        expect(errorTitle(new RpcError(kind, ''))).toBe(title);
    });
});

// --- Review list ------------------------------------------------------------------

describe('ReviewRow', () => {
    const row = (overrides: Partial<ReviewItem>, env = EMBEDDED, current = false) =>
        render(
            <ul>
                <ReviewRow item={item(overrides)} current={current} now={NOW} onOpenTools={noop} />
            </ul>,
            env
        );

    test('links to the PR in the GitHub tab when embedded', () => {
        const html = row({});
        expect(html).toContain('href="https://github.com/acme/widgets/pull/42"');
        expect(html).toContain('target="_top"');
        expect(html).toContain('rel="noreferrer"');
        expect(text(html)).toContain('acme/widgets#42 · mona · opened 5 hours ago');
    });

    test('opens a new tab from the popup window', () => {
        expect(row({}, STANDALONE)).toContain('target="_blank"');
    });

    test('tags: draft, conflict, review ease, comments', () => {
        const html = row({
            tags: 'widgets,draft',
            merge_conflicts: true,
            review_ease: 'hard',
            comment_count: 7,
        });
        expect(text(html)).toContain('Rate-limit the public API Draft Conflict hard');
        expect(html).toContain('pill-danger');
        expect(html).toContain('title="7 comments"');
        expect(html).toContain('state-fg-draft');
    });

    test('merged', () => {
        const html = row({ status: 'DONE', tags: 'widgets,merged' });
        expect(text(html)).toContain('Merged');
        expect(html).toContain('state-fg-merged');
    });

    test('the current tab’s PR is highlighted', () => {
        const html = row({}, EMBEDDED, true);
        expect(html).toContain('row row-current');
        expect(html).toContain('aria-current="page"');
        expect(text(html)).toContain('This tab');
        expect(row({})).not.toContain('row-current');
    });

    test('the tools button names the PR', () => {
        expect(row({})).toContain('aria-label="AI features and plugins for acme/widgets#42"');
    });
});

describe('ReviewList', () => {
    const items = [
        item({ section: 'Mine', section_priority: 2, number: 61, title: 'Bump go-github' }),
        item({ number: 42 }),
        item({ number: 57, title: 'Fix pagination', author: 'hubot' }),
    ];
    const list = (props: Partial<Parameters<typeof ReviewList>[0]>) =>
        render(
            <ReviewList
                items={items}
                loading={false}
                error={null}
                filter=""
                onFilterChange={noop}
                onRefresh={noop}
                currentPr={PR}
                now={NOW}
                onOpenTools={noop}
                {...props}
            />
        );

    test('sections by priority, with counts', () => {
        const t = text(list({}));
        expect(t).toContain('3 PRs in 2 sections');
        expect(t.indexOf('Needs my review 2')).toBeLessThan(t.indexOf('Mine 1'));
        expect(t.indexOf('Rate-limit')).toBeLessThan(t.indexOf('Fix pagination'));
    });

    test('filtered', () => {
        const t = text(list({ filter: 'hubot' }));
        expect(t).toContain('1 of 3 PRs match');
        expect(t).toContain('Fix pagination');
        expect(t).not.toContain('Bump go-github');
    });

    test('no match', () => {
        expect(text(list({ filter: 'zzz' }))).toContain('No PRs match “zzz”');
    });

    test('empty list', () => {
        const t = text(list({ items: [] }));
        expect(t).toContain('No PRs in your review list');
        expect(t).toContain('Configure workflows');
    });

    test('loading shows a skeleton; an error is inline with Retry', () => {
        expect(list({ items: null, loading: true })).toContain('aria-busy="true"');
        const failed = text(list({ items: null, error: new RpcError('timeout', 'no answer') }));
        expect(failed).toContain("The server didn't answer in time no answer Retry");
    });
});

// --- Cards ----------------------------------------------------------------------------

describe('FileOrderingCard', () => {
    const card = (output: AIFeatureOutput | undefined) =>
        render(<FileOrderingCard pr={PR} feature={FEATURES[3]} output={output} now={NOW} />);

    test('the note and the ordered files, each linking to its diff', async () => {
        const html = card(AI_OUTPUTS['file-ordering']);
        expect(text(html)).toContain(text(FILE_ORDERING_NOTE));
        expect(FILE_ORDERING_NOTE).toBe(
            "Applied feature — the suggested order to read this PR's files. A future version of the extension will reorder GitHub's Files changed tab to match; for now the order is listed here."
        );
        expect(html).toContain('<ol class="file-order">');
        const t = text(html);
        expect(t.indexOf('router.go')).toBeLessThan(t.indexOf('limiter.go'));
        expect(t.indexOf('limiter.go')).toBeLessThan(t.indexOf('api.md'));
        for (const path of FILES) {
            expect(html).toContain(
                `href="https://github.com/acme/widgets/pull/42/files#${await anchor(path)}"`
            );
        }
        expect(text(html)).toContain('Applied');
        expect(html).toContain('target="_top"');
    });

    test('no order yet', () => {
        const html = card(aiOutput('file-ordering', { status: 'not-run', updated_at: '' }));
        expect(text(html)).toContain(text(FILE_ORDERING_NOTE));
        expect(text(html)).toContain('No order yet');
        expect(html).not.toContain('<ol');
    });

    test('while it runs', () => {
        expect(
            text(card(aiOutput('file-ordering', { status: 'pending', updated_at: '' })))
        ).toContain('Working out the order…');
    });
});

describe('Annotations', () => {
    test('by file and line, linking to the line in the diff', async () => {
        const html = render(
            <Annotations pr={PR} annotations={PLUGIN_OUTPUTS.security_check.annotations} />
        );
        const t = text(html);
        expect(t).toContain('2 annotations');
        expect(t.indexOf('limiter.go :88')).toBeLessThan(t.indexOf('store.go :42'));
        expect(html).toContain(
            `href="https://github.com/acme/widgets/pull/42/files#${await anchor('internal/ratelimit/limiter.go')}R88"`
        );
        expect(html).toContain('pill-danger');
        expect(html).toContain('pill-attention');
    });

    test('none: nothing', () => {
        expect(render(<Annotations pr={PR} annotations={null} />)).toBe('');
    });
});

describe('AIFeatureCard', () => {
    const card = (output: AIFeatureOutput | undefined, extra: Record<string, unknown> = {}) =>
        render(
            <AIFeatureCard
                pr={PR}
                feature={FEATURES[1]}
                output={output}
                now={NOW}
                onRun={noop}
                {...extra}
            />
        );

    test('stale and truncated: badges, and Run', () => {
        const html = card(AI_OUTPUTS['feature-flags']);
        const t = text(html);
        expect(t).toContain('Behind a flag? Success Stale Truncated');
        expect(t).toContain('Updated 5 min ago');
        expect(html).toContain('aria-label="Run Behind a flag?"');
        expect(html).toContain('btn-primary');
        // Collapsed: a preview of the result instead of the body.
        expect(html).toContain('aria-expanded="false"');
        expect(t).toContain('2 of 5 changes run unflagged.');
    });

    test('a current result: Re-run', () => {
        const html = card(
            aiOutput('feature-flags', { body: { body_type: 'markdown', body_content: 'ok' } })
        );
        expect(html).toContain('aria-label="Re-run Behind a flag?"');
        expect(html).not.toContain('btn-primary');
    });

    test('running: spinner, button disabled', () => {
        const html = card(aiOutput('feature-flags', { status: 'pending' }));
        expect(text(html)).toContain('Running');
        expect(html).toContain('class="spinner"');
        expect(html).toMatch(/<button[^>]*disabled=""[^>]*aria-label="Running Behind a flag\?"/);
    });

    test('never run: description, Run, nothing to expand', () => {
        const html = card(aiOutput('feature-flags', { status: 'not-run', updated_at: '' }));
        expect(text(html)).toContain('Not run');
        expect(text(html)).toContain('What Behind a flag? does.');
        expect(html).not.toContain('aria-expanded');
        expect(html).not.toContain('Updated');
    });

    test('outputs not loaded yet', () => {
        const html = card(undefined, { outputLoading: true });
        expect(text(html)).toContain('Loading');
        expect(html).toMatch(/<button[^>]*disabled=""/);
    });

    test('a failed request and a note', () => {
        expect(text(card(undefined, { error: new RpcError('timeout', 'slow') }))).toContain(
            "Couldn't start the run: slow"
        );
        expect(text(card(AI_OUTPUTS['feature-flags'], { note: 'Already up to date' }))).toContain(
            'Already up to date'
        );
    });

    test('expanded: the markdown body, sanitized, and the annotations', () => {
        const html = render(
            <AIFeatureCard
                pr={PR}
                feature={FEATURES[0]}
                output={aiOutput('comments-addressed', {
                    body: {
                        body_type: 'markdown',
                        body_content:
                            '**Bold** <script>alert(1)</script> [link](https://github.com/x)\n\n| a |\n|---|\n| b |',
                    },
                    annotations: AI_OUTPUTS['comments-addressed'].annotations,
                })}
                now={NOW}
                onRun={noop}
                defaultExpanded
            />
        );
        expect(html).toContain('aria-expanded="true"');
        expect(html).toContain('<strong>Bold</strong>');
        expect(html).not.toContain('<script');
        expect(html).toContain(
            '<a href="https://github.com/x" target="_top" rel="noreferrer">link</a>'
        );
        expect(html).toContain('<table>');
        expect(text(html)).toContain('1 annotation');
    });

    test('change-diagram: the diagram (drawn after mount), not the body', () => {
        const html = render(
            <AIFeatureCard
                pr={PR}
                feature={FEATURES[2]}
                output={AI_OUTPUTS['change-diagram']}
                now={NOW}
                onRun={noop}
                defaultExpanded
            />
        );
        expect(text(html)).toContain('Drawing the diagram…');
        expect(text(html)).toContain('Added Changed');
        expect(text(html)).not.toContain('Removed');
        expect(text(html)).toContain('Source');
        expect(text(html)).toContain('Copy source');
    });
});

describe('PluginCard', () => {
    const card = (name: string) =>
        render(
            <PluginCard
                pr={PR}
                plugin={PLUGINS.find(p => p.Name === name)!}
                output={PLUGIN_OUTPUTS[name]}
                onRun={noop}
            />
        );

    test('deferred: on demand, Run', () => {
        const html = card('style_guidelines');
        expect(text(html)).toContain('style_guidelines On demand');
        expect(html).toContain('aria-label="Run style_guidelines"');
        expect(html).not.toContain('aria-expanded');
    });

    test('a result: Re-run, annotation count, expandable', () => {
        const html = card('security_check');
        expect(text(html)).toContain('security_check Success 2 annotations');
        expect(html).toContain('aria-label="Re-run security_check"');
        expect(html).toContain('aria-expanded="false"');
        expect(html).toContain('title="security_check --strict"');
    });

    test('no entry: not run yet', () => {
        const html = render(
            <PluginCard pr={PR} plugin={PLUGINS[0]} output={undefined} onRun={noop} />
        );
        expect(text(html)).toContain('Not run yet');
        expect(html).toContain('aria-label="Run security_check"');
    });

    test('an HTML body goes in a sandboxed frame without scripts', () => {
        const html = render(
            <PluginCard
                pr={PR}
                plugin={PLUGINS[0]}
                output={{
                    result: '',
                    status: 'success',
                    body: { body_type: 'html', body_content: '<p>Hi</p><script>x()</script>' },
                    annotations: null,
                }}
                onRun={noop}
                defaultExpanded
            />
        );
        const frame = html.match(/<iframe[^>]*>/)?.[0] ?? '';
        expect(frame).toContain(
            'sandbox="allow-same-origin allow-popups allow-popups-to-escape-sandbox"'
        );
        expect(frame).not.toContain('allow-scripts');
        expect(frame).toContain('srcDoc=');
    });
});

// --- Header -----------------------------------------------------------------------------

describe('Header', () => {
    const header = (props: Partial<Parameters<typeof Header>[0]>) =>
        render(
            <Header
                pr={null}
                status={null}
                standalone={false}
                onBack={noop}
                onClose={noop}
                {...props}
            />
        );

    test('list: crumb, close button, no Back', () => {
        const html = header({});
        expect(text(html)).toContain('Code Review Server / Review list');
        expect(html).toContain('aria-label="Close"');
        expect(html).not.toContain('Back to the review list');
        expect(text(html)).toContain('Connecting');
    });

    test('PR view: Back and the PR crumb; no close button standalone', () => {
        const html = header({ pr: PR, standalone: true });
        expect(html).toContain('aria-label="Back to the review list"');
        expect(text(html)).toContain('acme/widgets#42');
        expect(html).not.toContain('aria-label="Close"');
    });

    test('connection states', () => {
        const ok = header({
            status: {
                connected: true,
                host: { server_path: '/bin/crs', log_path: '/log', version: 'v1' },
            },
        });
        expect(ok).toContain('connection-connected');
        expect(ok).toContain('title="Connected to /bin/crs (v1)"');
        const bad = header({
            status: { connected: false, lastError: { kind: 'host-missing', message: 'not found' } },
        });
        expect(bad).toContain('connection-error');
        expect(text(bad)).toContain('Disconnected');
    });
});

// --- The PR tools view -------------------------------------------------------------------

describe('PRToolsView', () => {
    const view = (data: Partial<PRToolsData> = {}, actions: ActionState = NO_ACTIONS) =>
        render(
            <PRToolsView
                pr={PR}
                data={{ ...DATA, ...data }}
                actions={actions}
                handlers={HANDLERS}
                now={NOW}
                expanded={{ ai: ['comments-addressed'], plugins: ['security_check'] }}
            />
        );

    test('everything loaded', () => {
        const html = view();
        const t = text(html);
        // Summary.
        expect(t).toContain('Open acme/widgets');
        expect(t).toContain('Rate-limit the public API per token #42');
        expect(t).toContain('mona wants to merge mona/limits into main');
        expect(t).toContain('+412 −58 7 files changed Hard to review');
        expect(t).toContain('Sync');
        // AI features: a card per report feature, then file ordering; review ease is the pill.
        expect(t).toContain('AI features 4');
        const order = ['Comments addressed?', 'Behind a flag?', 'Change diagram', 'File ordering'];
        const at = order.map(name => t.indexOf(name));
        expect(at.every(i => i >= 0)).toBe(true);
        expect([...at].sort((a, b) => a - b)).toEqual(at);
        expect(t).not.toContain('Review ease Success');
        expect(t).toContain(text(FILE_ORDERING_NOTE));
        expect(html).toContain('1 of 4 threads');
        expect(t).toContain(
            'Not enabled: Test gaps, Summary — turn them on with [[AIFeatures]] in the server config.'
        );
        // Plugins.
        expect(t).toContain('Plugins 2 Re-run all');
        expect(t).toContain('Found 2 issues');
        expect(t).toContain('style_guidelines On demand');
    });

    test('while GetPR loads, the lists still render; outputs show as loading', () => {
        const html = view({
            payload: loading(),
            aiOutputs: loading(),
            pluginOutputs: loading(),
        });
        const t = text(html);
        expect(t).toContain('acme/widgets#42');
        expect(t).toContain('Loading the pull request…');
        expect(t).toContain('Comments addressed? Loading');
        expect(t).toContain('security_check Loading');
    });

    test('a connection-level failure takes over the view', () => {
        const html = view({
            payload: {
                value: null,
                error: new RpcError('host-missing', 'not found'),
                loading: false,
            },
        });
        expect(html).toContain('error-panel');
        expect(html).not.toContain('AI features');
    });

    test('a section failure stays in its section', () => {
        const t = text(
            view({
                plugins: { value: null, error: new RpcError('rpc', 'boom'), loading: false },
            })
        );
        expect(t).toContain("Couldn't load the plugins boom Retry");
        expect(t).toContain('Comments addressed?');
    });

    test('no plugins, no AI features enabled', () => {
        const t = text(
            view({
                plugins: loaded([]),
                features: loaded(FEATURES.map(f => ({ ...f, enabled: false }))),
            })
        );
        expect(t).toContain('No plugins configured. Add [[Plugins]]');
        expect(t).toContain('No AI features are enabled');
    });

    test('action state: busy, errors and notes', () => {
        const html = view(DATA, {
            busy: { 'plugins:all': true },
            errors: { sync: new RpcError('timeout', 'GitHub is slow') },
            notes: { 'ai:comments-addressed': 'Already up to date' },
        });
        const t = text(html);
        expect(t).toContain("Couldn't sync: GitHub is slow");
        expect(t).toContain('Already up to date');
        expect(html).toMatch(/aria-busy="true"[^>]*>.*Re-run all/);
    });
});
