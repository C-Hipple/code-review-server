// Demo data for the README screenshots (`e2e/screenshots.ts`): the fake `crs`
// serves these in place of `prs.ts` when CRS_E2E_FIXTURES=demo. Same shapes as
// the e2e fixtures, but a review list that looks like a working day and a PR
// whose diff is a real change from this repository's history.

import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { comment, type PRFixture, type ReviewItem } from './prs';

const MERGED_STATE_DIFF = readFileSync(
    resolve(import.meta.dirname, 'demo/merged_state.diff'),
    'utf8'
);

const hoursAgo = (h: number) => new Date(Date.now() - h * 60 * 60 * 1000).toISOString();

function item(fields: Partial<ReviewItem> & Pick<ReviewItem, 'title' | 'repo' | 'number'>) {
    const owner = fields.owner ?? 'acme';
    return {
        section: 'Needs Review',
        section_priority: 1,
        status: 'TODO',
        tags: fields.repo,
        owner,
        author: 'alice',
        url: `https://github.com/${owner}/${fields.repo}/pull/${fields.number}`,
        release_status: '',
        review_ease: '',
        created_at: hoursAgo(5),
        required_teams: [],
        merge_conflicts: false,
        ...fields,
    } satisfies ReviewItem;
}

/**
 * The GitHub comment position of the first line of `path`'s diff that is
 * exactly `line`: lines counted from the one after the file's first @@ header.
 */
function position(diff: string, path: string, line: string): string {
    const lines = diff.split('\n');
    const start = lines.indexOf(`diff --git a/${path} b/${path}`);
    const header = lines.findIndex((l, i) => i > start && l.startsWith('@@'));
    for (let i = header + 1; i < lines.length && !lines[i].startsWith('diff --git '); i++) {
        if (lines[i] === line) return String(i - header);
    }
    throw new Error(`demo fixture: ${line} is not in ${path}'s diff`);
}

/** A small stand-in diff for the PRs nobody opens in the screenshots. */
function smallDiff(path: string, from: string, to: string) {
    return [
        `diff --git a/${path} b/${path}`,
        'index 1a2b3c4..4c3b2a1 100644',
        `--- a/${path}`,
        `+++ b/${path}`,
        '@@ -1,3 +1,3 @@',
        ' // context',
        `-${from}`,
        `+${to}`,
        ' // context',
        '',
    ].join('\n');
}

function filler(
    fields: Partial<ReviewItem> & Pick<ReviewItem, 'title' | 'repo' | 'number'>,
    body: string,
    diff: string
): PRFixture {
    return {
        item: item(fields),
        body,
        baseRef: 'main',
        headRef: `${fields.author ?? 'alice'}/pr-${fields.number}`,
        repoPath: '',
        diff,
        comments: [],
        reviews: [],
        plugins: {},
    };
}

// The commit MERGED_STATE_DIFF comes from, whose files hunk expansion reads.
const MERGED_STATE_COMMIT = '0d8f9b3';
const REPO_ROOT = resolve(import.meta.dirname, '../../..');

/** PATH at MERGED_STATE_COMMIT, or '' where the history doesn't reach it. */
function fileAtCommit(path: string): string {
    try {
        return execFileSync('git', ['show', `${MERGED_STATE_COMMIT}:${path}`], {
            cwd: REPO_ROOT,
            encoding: 'utf8',
            stdio: ['ignore', 'pipe', 'ignore'],
        });
    } catch {
        return '';
    }
}

/**
 * The review list as the server's OrgRenderer writes it (GetAllReviews'
 * `content`, which the Emacs client shows): a heading per section with its
 * done count, then each PR's headline, tags and detail lines.
 */
export function orgContent(prs: PRFixture[]): string {
    const sections = [...new Set(prs.map(p => p.item.section))];
    const out: string[] = [];
    for (const section of sections) {
        const items = prs.filter(p => p.item.section === section);
        const done = items.filter(p => p.item.status === 'DONE').length;
        out.push(
            `* ${done === items.length ? 'DONE' : 'TODO'} ${section} [${done}/${items.length}]`
        );
        for (const { item, headRef, reviews } of items) {
            const tags = [
                ...item.tags.split(',').filter(Boolean),
                ...(item.merge_conflicts ? ['conflict'] : []),
                ...(item.review_ease ? [item.review_ease] : []),
            ];
            const by = (state: string) =>
                reviews
                    .filter(r => r.state === state)
                    .map(r => r.user)
                    .join(', ');
            out.push(
                `** ${item.status} ${item.title}\t\t:${tags.join(':')}:`,
                String(item.number),
                `Repo: ${item.owner}/${item.repo}`,
                item.url,
                `Title: ${item.title}`,
                `Author: ${item.author}`,
                `Branch: ${item.author}:${headRef}`,
                'Requested Reviewers: you',
                'Requested Teams:'
            );
            if (item.status === 'TODO') {
                out.push(
                    `Approved By: ${by('APPROVED')}`,
                    `Changes Requested By: ${by('CHANGES_REQUESTED')}`,
                    `Commented By: ${by('COMMENTED')}`
                );
            }
        }
        out.push('');
    }
    return out.join('\n') + '\n';
}

export function buildFixtures(): PRFixture[] {
    const diff = MERGED_STATE_DIFF;
    const renderer = 'server/renderer.go';
    const stateLine = `+\tif pr.GetState() == "closed" && (pr.GetMerged() || pr.MergedAt != nil) {`;
    const callLine = '+\t\t\t\tState:              prState(pr),';
    return [
        {
            item: item({
                title: "Report a merged PR's state as merged, not closed",
                repo: 'code-review-server',
                number: 315,
                author: 'alice',
                tags: 'code-review-server,server',
                review_ease: 'easy',
                created_at: hoursAgo(3),
            }),
            body: [
                "GitHub's state is only open or closed, so the review header showed a merged",
                'PR as CLOSED in red. `GetPRDetails` now sends `"merged"` for a closed PR that',
                "was merged (`merged`, or `merged_at` for a listing's PR object), which the",
                "web client's header already draws in purple.",
            ].join('\n'),
            baseRef: 'main',
            headRef: 'alice/merged-state',
            labels: ['bug', 'server'],
            files: {
                'server/renderer.go': fileAtCommit('server/renderer.go'),
                'server/renderer_test.go': fileAtCommit('server/renderer_test.go'),
            },
            chatReply: [
                '`prState` only rewrites a **closed** PR, so open PRs and drafts keep',
                "GitHub's state untouched. Two things worth checking:",
                '',
                '1. **Listings.** `ListPullRequests` returns objects without `merged`, which',
                '   is why the `MergedAt != nil` arm exists — the table test covers it as',
                '   "merged from a listing".',
                '2. **Callers comparing against `"closed"`.** `server/renderer.go` is the only',
                '   writer of `PRMetadata.State`, but the Emacs header and the TUI read it.',
                '   Neither switches on `"closed"`, so `"merged"` falls through to the',
                '   default styling there rather than breaking anything.',
                '',
                "Looks safe to merge; the only behaviour change is the header's colour.",
            ].join('\n'),
            diagram: [
                'flowchart TD',
                '    get["GetPRDetails"]:::changed --> state["prState(pr)"]:::added',
                '    state -->|closed and merged / merged_at| merged(["#quot;merged#quot;"]):::added',
                '    state -->|otherwise| gh(["GitHub state: open / closed"])',
                '    get --> meta["PRMetadata.State"]:::changed',
                '    meta --> header["Review header colour"]',
                '    test["TestPRState"]:::added -.-> state',
                '    classDef added fill:#dcfce7,stroke:#16a34a,color:#14532d',
                '    classDef changed fill:#fef3c7,stroke:#d97706,color:#78350f',
            ].join('\n'),
            // This checkout, so the review finds the repo locally.
            repoPath: resolve(import.meta.dirname, '../../..'),
            diff,
            comments: [
                comment({
                    id: '9101',
                    author: 'bob',
                    body: "Could a closed PR have `merged: false` but a `merged_at`? I'd expect GitHub to keep them in step.",
                    path: renderer,
                    position: position(diff, renderer, stateLine),
                    review_id: 801,
                    created_at: hoursAgo(2),
                    thread_id: 'PRRT_demo1',
                }),
                comment({
                    id: '9102',
                    author: 'alice',
                    body: "They are for a single-PR fetch, but a listing's PR objects leave `merged` out entirely, so `merged_at` is the only sign there.",
                    path: renderer,
                    position: position(diff, renderer, stateLine),
                    in_reply_to: 9101,
                    created_at: hoursAgo(1.5),
                    thread_id: 'PRRT_demo1',
                }),
                comment({
                    id: '9103',
                    author: 'carol',
                    body: 'Nice — the Emacs header can pick this up too.',
                    path: renderer,
                    position: position(diff, renderer, callLine),
                    review_id: 802,
                    created_at: hoursAgo(1),
                    thread_id: 'PRRT_demo2',
                    resolved: true,
                    resolved_by: 'alice',
                }),
            ],
            reviews: [
                {
                    id: 801,
                    user: 'bob',
                    body: 'One question about listings.',
                    state: 'COMMENTED',
                    submitted_at: hoursAgo(2),
                    html_url:
                        'https://github.com/acme/code-review-server/pull/315#pullrequestreview-801',
                    reactions: [],
                },
                {
                    id: 802,
                    user: 'carol',
                    body: 'LGTM',
                    state: 'APPROVED',
                    submitted_at: hoursAgo(1),
                    html_url:
                        'https://github.com/acme/code-review-server/pull/315#pullrequestreview-802',
                    reactions: [],
                },
            ],
            plugins: {
                summarize: {
                    result: 'Merged PRs now report their state as merged.',
                    status: 'success',
                    body: {
                        body_type: 'markdown',
                        body_content: [
                            '## Summary',
                            '',
                            'Adds `prState`, which reports a closed PR that was merged as **merged**, and uses it for `PRMetadata.State`. Covered by a table test over single-PR and listing objects.',
                        ].join('\n'),
                    },
                    annotations: [
                        {
                            filename: 'server/renderer.go',
                            line: 837,
                            severity: 'info',
                            content:
                                "The heart of the change: a listing's PR objects carry merged_at but not merged.",
                        },
                        {
                            filename: 'server/renderer.go',
                            line: 1275,
                            severity: 'info',
                            content: 'The one caller: every client reads State from here.',
                        },
                    ],
                },
                security_check: {
                    result: 'No security issues found.',
                    status: 'success',
                    body: {
                        body_type: 'markdown',
                        body_content: [
                            '## Security check',
                            '',
                            '**No issues found.** The change only reads fields of the PR object GitHub returned; nothing new is taken from user input or sent anywhere.',
                        ].join('\n'),
                    },
                    annotations: [],
                },
            },
        },
        filler(
            {
                title: 'CI status: leave out check runs a later run replaced',
                repo: 'code-review-server',
                number: 329,
                author: 'dave',
                tags: 'code-review-server,ci',
                review_ease: 'medium',
                created_at: hoursAgo(7),
            },
            'Re-runs no longer show the failed attempt.',
            smallDiff('git_tools/check_runs.go', 'runs := all', 'runs := latestRuns(all)')
        ),
        filler(
            {
                title: 'Retry webhook deliveries that time out',
                repo: 'hooks-gateway',
                number: 88,
                author: 'erin',
                tags: 'hooks-gateway',
                review_ease: 'hard',
                merge_conflicts: true,
                created_at: hoursAgo(26),
            },
            'Backs off and retries up to three times.',
            smallDiff('deliver.go', 'return err', 'return retry(err)')
        ),
        filler(
            {
                title: 'Add dark mode to the settings page',
                repo: 'dashboard',
                number: 1204,
                author: 'frank',
                tags: 'dashboard,ui',
                review_ease: 'easy',
                created_at: hoursAgo(30),
            },
            'Follows the system theme.',
            smallDiff('src/theme.ts', "mode = 'light'", 'mode = systemMode()')
        ),
        filler(
            {
                title: 'Draft: move the job queue to Postgres',
                repo: 'hooks-gateway',
                number: 91,
                author: 'grace',
                status: 'WAITING',
                tags: 'hooks-gateway,draft',
                section: 'Waiting on Author',
                section_priority: 2,
                review_ease: 'hard',
                created_at: hoursAgo(50),
            },
            'Work in progress.',
            smallDiff('queue/store.go', 'store := newRedis()', 'store := newPostgres()')
        ),
        filler(
            {
                title: 'Cache image downloads by URL',
                repo: 'code-review-server',
                number: 312,
                author: 'sam',
                status: 'WAITING',
                tags: 'code-review-server',
                section: 'My PRs',
                section_priority: 3,
                review_ease: 'medium',
                created_at: hoursAgo(20),
            },
            'Content-addressed under $CRS_HOME/images.',
            smallDiff('images/store.go', 'path := name', 'path := hash(url)')
        ),
        filler(
            {
                title: 'Show the release a merged PR shipped in',
                repo: 'dashboard',
                number: 1199,
                author: 'sam',
                status: 'TODO',
                tags: 'dashboard',
                section: 'My PRs',
                section_priority: 3,
                review_ease: 'easy',
                created_at: hoursAgo(44),
            },
            'Reads the release from the tag list.',
            smallDiff('src/release.ts', 'release = null', 'release = findRelease(sha)')
        ),
        filler(
            {
                title: 'Bump go-github to v66',
                repo: 'code-review-server',
                number: 309,
                author: 'renovate',
                status: 'DONE',
                tags: 'code-review-server,merged',
                section: 'Recently Merged',
                section_priority: 4,
                created_at: hoursAgo(72),
            },
            'Routine bump.',
            smallDiff(
                'go.mod',
                'github.com/google/go-github/v65 v65.0.0',
                'github.com/google/go-github/v66 v66.0.0'
            )
        ),
    ];
}
