// What the fake server serves, typed with the extension's own wire types so a
// change to those shows up here. Imported by the fake (under Bun) and by the
// specs (under Playwright) — keep it free of runtime dependencies.

import type {
    AIFeature,
    AIFeatureOutput,
    PluginConfig,
    PluginOutput,
    PRMetadata,
    PRPayload,
    ReviewItem,
} from '../../src/types';

export interface PR {
    owner: string;
    repo: string;
    number: number;
}

export const prKey = (pr: PR) => `${pr.owner}/${pr.repo}#${pr.number}`;

/** The PR most specs open: in the review list, with every output populated. */
export const PR42: PR = { owner: 'acme', repo: 'widgets', number: 42 };
/** Another listed PR, for navigation; nothing has run for it. */
export const PR57: PR = { owner: 'acme', repo: 'widgets', number: 57 };
/** In no review list; GetPR serves it with a multi-megabyte diff. */
export const BIG_PR: PR = { owner: 'acme', repo: 'monorepo', number: 77 };

const UPDATED = '2026-01-02T03:04:05Z';

function item(fields: Partial<ReviewItem> & PR & Pick<ReviewItem, 'title' | 'section'>) {
    return {
        section_priority: 1,
        status: 'TODO',
        tags: fields.repo,
        author: 'mona',
        url: `https://github.com/${fields.owner}/${fields.repo}/pull/${fields.number}`,
        release_status: '',
        review_ease: '',
        created_at: '2026-01-01T00:00:00Z',
        required_teams: null,
        comment_count: 0,
        merge_conflicts: false,
        ...fields,
    } satisfies ReviewItem;
}

/**
 * The review list, in the server's order. The merged PR comes first on
 * purpose: the panel orders sections by section_priority, then by first
 * appearance, and keeps the server's order within a section.
 */
export const REVIEW_ITEMS: ReviewItem[] = [
    item({
        owner: 'acme',
        repo: 'widgets',
        number: 39,
        section: 'Recently merged',
        section_priority: 5,
        title: 'Cache team membership lookups for an hour',
        status: 'DONE',
        tags: 'widgets,merged',
        comment_count: 5,
    }),
    item({
        ...PR42,
        section: 'Needs my review',
        title: 'Rate-limit the public API per token',
        review_ease: 'hard',
        comment_count: 7,
        merge_conflicts: true,
    }),
    item({
        owner: 'acme',
        repo: 'hooks',
        number: 211,
        section: 'My open PRs',
        section_priority: 2,
        title: 'Add retries with jitter to the webhook sender',
        author: 'chris',
        review_ease: 'medium',
        comment_count: 3,
    }),
    item({
        ...PR57,
        section: 'Needs my review',
        title: 'Fix off-by-one in the pagination cursor',
        author: 'hubot',
        review_ease: 'easy',
        comment_count: 2,
    }),
    item({
        owner: 'acme',
        repo: 'web',
        number: 880,
        section: 'Needs my review',
        title: 'WIP: dark mode for the settings page',
        author: 'lisa',
        status: 'WAITING',
        tags: 'web,draft',
    }),
];

export const SECTIONS_IN_ORDER = ['Needs my review', 'My open PRs', 'Recently merged'];

export const BIG_PR_TITLE = 'Vendor the 日本語 fonts ✓ — naïve café 😀';

function metadata(pr: PR, fields: Partial<PRMetadata> = {}): PRMetadata {
    const listed = REVIEW_ITEMS.find(i => prKey(i) === prKey(pr));
    return {
        number: pr.number,
        title: listed?.title ?? `Pull request ${pr.number}`,
        author: listed?.author ?? 'octocat',
        base_ref: 'main',
        head_ref: 'feature',
        state: 'open',
        milestone: '',
        labels: [],
        assignees: null,
        reviewers: null,
        requested_teams: null,
        approved_by: null,
        changes_requested_by: null,
        commented_by: null,
        draft: false,
        ci_status: 'success',
        ci_failures: null,
        body: '',
        url: `https://github.com/${pr.owner}/${pr.repo}/pull/${pr.number}`,
        repo_path: '',
        worktree_path: '',
        release_status: '',
        review_ease: listed?.review_ease ?? '',
        changed_files: 2,
        additions: 23,
        deletions: 4,
        ...fields,
    };
}

/** GetPR's reply for any PR: the real server reads any PR, listed or not. */
export function prPayload(pr: PR): PRPayload {
    let meta = metadata(pr);
    let diff = 'diff --git a/README.md b/README.md\n+hello\n';
    if (prKey(pr) === prKey(PR42)) {
        meta = metadata(pr, {
            head_ref: 'mona/token-rate-limits',
            changed_files: 7,
            additions: 412,
            deletions: 58,
        });
    } else if (prKey(pr) === prKey(BIG_PR)) {
        diff = bigDiff();
        meta = metadata(pr, { title: BIG_PR_TITLE, changed_files: 1, additions: 30_000 });
    }
    return {
        okay: true,
        metadata: meta,
        annotations: [],
        content: '',
        diff,
        feedback: '',
        comments: [],
        outdated_comments: [],
        reviews: [],
        commits: [],
        images: [],
    };
}

/**
 * About 3 MB of diff, far over the 1 MB Chrome takes in one native message,
 * so the host must chunk the response. It mixes 1- to 4-byte UTF-8 with the
 * characters JSON escapes (quotes, backslashes, tabs, U+2028) so chunk
 * boundaries land inside multi-byte characters and escape sequences.
 */
export function bigDiff(): string {
    const pieces = [
        'plain ascii',
        'naïve café',
        'check ✓ — dash',
        '日本語のテキスト',
        'emoji 😀🚀',
        'quote " and backslash \\ and tab \t',
        'line separator',
        '<tag> & ampersand',
    ];
    const lines = ['diff --git a/fonts/README.md b/fonts/README.md', '@@ -0,0 +1,30000 @@'];
    for (let i = 0; i < 30_000; i++) {
        lines.push(`+${i}: ${pieces[i % pieces.length]} ${pieces[(i * 7) % pieces.length]}`);
    }
    return lines.join('\n') + '\n';
}

// --- AI features --------------------------------------------------------------

export const FEATURES: AIFeature[] = [
    {
        id: 'comments-addressed',
        name: 'Comments addressed?',
        description: 'Reports which review comments are still outstanding.',
        enabled: true,
        automatic: true,
        mode: 'oneshot',
        modes: ['oneshot', 'agent'],
        provider: 'gemini',
        applied: false,
    },
    {
        id: 'feature-flags',
        name: 'Behind a flag?',
        description: 'Reports which logic changes would take effect with every feature flag off.',
        enabled: true,
        automatic: false,
        mode: 'agent',
        modes: ['oneshot', 'agent'],
        provider: 'openrouter',
        applied: false,
    },
    {
        id: 'change-diagram',
        name: 'Change diagram',
        description: 'Draws a Mermaid diagram of what the PR changes.',
        enabled: true,
        automatic: false,
        mode: 'oneshot',
        modes: ['oneshot'],
        provider: 'gemini',
        applied: false,
    },
    {
        id: 'file-ordering',
        name: 'File ordering',
        description: 'Orders the files of the diff so the PR reads top to bottom.',
        enabled: true,
        automatic: true,
        mode: 'oneshot',
        modes: ['oneshot'],
        provider: 'gemini',
        applied: true,
    },
    {
        id: 'review-ease',
        name: 'Review ease',
        description: 'Rates how easy the PR is to review.',
        enabled: true,
        automatic: true,
        mode: 'oneshot',
        modes: ['oneshot'],
        provider: 'gemini',
        applied: true,
    },
    {
        id: 'test-gaps',
        name: 'Test gaps',
        description: 'Lists changed behaviour no test exercises.',
        enabled: false,
        automatic: false,
        mode: 'oneshot',
        modes: ['oneshot'],
        provider: 'gemini',
        applied: false,
    },
];

export const DIAGRAM = `flowchart LR
    Client([API client]) --> Router["api.Router"]:::changed
    Router --> MW["ratelimit.Middleware"]:::added
    MW --> Limiter["ratelimit.Limiter"]:::added
    Limiter --> Store[("ratelimit.Store")]:::added
    Legacy["legacy IP throttle"]:::removed -.-> Router
    classDef added fill:#dcfce7,stroke:#16a34a,color:#14532d
    classDef changed fill:#fef3c7,stroke:#d97706,color:#78350f
    classDef removed fill:#fee2e2,stroke:#dc2626,color:#7f1d1d,stroke-dasharray: 5 5`;

export const FILE_ORDER = [
    'internal/api/router.go',
    'internal/ratelimit/middleware.go',
    'internal/ratelimit/limiter.go',
    'internal/ratelimit/limiter_test.go',
    'docs/api.md',
];

export function aiOutput(
    feature: AIFeature,
    fields: Partial<AIFeatureOutput> = {}
): AIFeatureOutput {
    return {
        feature: feature.id,
        name: feature.name,
        status: 'not-run',
        body: { body_type: 'markdown', body_content: '' },
        annotations: null,
        report: null,
        outstanding: null,
        covers_sha: '',
        covers_digest: '',
        current_sha: 'e2e-sha',
        current_digest: 'e2e-digest',
        stale: false,
        truncated: false,
        updated_at: '',
        ...fields,
    };
}

function done(fields: Partial<AIFeatureOutput>): Partial<AIFeatureOutput> {
    return {
        status: 'success',
        covers_sha: 'e2e-sha',
        covers_digest: 'e2e-digest',
        updated_at: UPDATED,
        ...fields,
    };
}

const feature = (id: string) => FEATURES.find(f => f.id === id)!;

/** PR42's stored AI results; every other PR has none. */
export function pr42AIOutputs(): Record<string, AIFeatureOutput> {
    return {
        'comments-addressed': aiOutput(
            feature('comments-addressed'),
            done({
                body: {
                    body_type: 'markdown',
                    body_content:
                        '**1 of 4 review threads is still outstanding.**\n\n' +
                        '| Status | Where |\n|---|---|\n| Outstanding | `limiter.go:57` |\n',
                },
            })
        ),
        'feature-flags': aiOutput(feature('feature-flags')),
        'change-diagram': aiOutput(
            feature('change-diagram'),
            done({
                body: { body_type: 'markdown', body_content: '```mermaid\n' + DIAGRAM + '\n```\n' },
                report: { mermaid: DIAGRAM, diagram_type: 'flowchart' },
            })
        ),
        'file-ordering': aiOutput(feature('file-ordering'), done({ report: { files: FILE_ORDER } })),
        'review-ease': aiOutput(feature('review-ease'), done({ report: { rating: 'hard' } })),
    };
}

/** What a finished run of `featureId` reports; `run` counts the runs started. */
export function aiRunResult(featureId: string, run: number): Partial<AIFeatureOutput> {
    return done({
        body: {
            body_type: 'markdown',
            body_content: `**Run ${run}:** ${featureId} found 2 of 5 logic changes outside a flag.\n`,
        },
        updated_at: new Date().toISOString(),
    });
}

// --- Plugins --------------------------------------------------------------------

function plugin(fields: Partial<PluginConfig> & Pick<PluginConfig, 'Name'>): PluginConfig {
    return {
        Command: fields.Name,
        IncludeDiff: true,
        IncludeHeaders: true,
        IncludeComments: false,
        IncludeBranch: false,
        OnlyOnDemand: false,
        Provider: '',
        Model: '',
        ...fields,
    };
}

export const PLUGINS: PluginConfig[] = [
    plugin({ Name: 'security_check', Provider: 'gemini' }),
    plugin({ Name: 'style_guidelines', OnlyOnDemand: true, Provider: 'openrouter' }),
];

const SECURITY_MD =
    '### Security review\n\nFound **2 issues** worth a look before merging.\n\n' +
    '1. The raw bearer token is logged when a request is throttled.\n' +
    '2. Bucket keys include a client-controlled header.\n';

/** PR42's stored plugin results; every other PR has none. */
export function pr42PluginOutputs(): Record<string, PluginOutput> {
    return {
        security_check: {
            result: SECURITY_MD,
            status: 'success',
            body: { body_type: 'markdown', body_content: SECURITY_MD },
            annotations: [
                {
                    filename: 'internal/ratelimit/limiter.go',
                    line: 88,
                    severity: 'error',
                    content: 'The raw bearer token is logged when a request is rejected.',
                },
                {
                    filename: 'internal/ratelimit/store.go',
                    line: 42,
                    severity: 'warning',
                    content: 'Bucket keys include X-Forwarded-For, which the client controls.',
                },
                {
                    filename: 'internal/api/router.go',
                    line: 31,
                    severity: 'info',
                    content: 'Rate limiting runs before auth.',
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
}

/**
 * What a finished run of plugin `name` reports. style_guidelines answers in
 * HTML with a script in it, which the panel's sandboxed frame must not run.
 */
export function pluginRunResult(name: string, run: number): PluginOutput {
    if (name === 'style_guidelines') {
        const html =
            `<h3>Style guide findings (run ${run})</h3>` +
            '<p>Exported <code>Limiter</code> lacks a doc comment.</p>' +
            "<script>document.body.textContent = 'SCRIPT RAN'</script>";
        return {
            result: html,
            status: 'success',
            body: { body_type: 'html', body_content: html },
            annotations: null,
        };
    }
    const md = `Run ${run} of ${name}: no new findings.\n`;
    return {
        result: md,
        status: 'success',
        body: { body_type: 'markdown', body_content: md },
        annotations: null,
    };
}
