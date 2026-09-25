// Canned review data the fake `crs` backend serves. Shapes mirror the Go
// server's JSON (server/renderer.go ReviewItem, PRMetadata, CommentJSON,
// ReviewJSON) so the frontend sees exactly what it would in production.
//
// PR acme/widgets#42 is the "real" one: its metadata points repo_path at the
// fixture repo checked in next to this file, and its diff is the diff that
// turns the base revision into that checkout, so both LSP modes resolve
// symbols against files that actually exist on disk.

import { resolve } from 'node:path';

// The launcher points the real-LSP bridge at a git copy (CRS_E2E_REPO).
export const FIXTURE_REPO = process.env.CRS_E2E_REPO || resolve(import.meta.dirname, 'repo');

export interface ReviewItem {
    section: string;
    section_priority: number;
    status: string;
    tags: string;
    title: string;
    owner: string;
    repo: string;
    number: number;
    author: string;
    url: string;
    release_status: string;
    review_ease: string;
    created_at: string;
    required_teams: unknown[];
}

export interface CommentJSON {
    id: string;
    author: string;
    body: string;
    path: string;
    position: string;
    in_reply_to: number;
    created_at: string;
    outdated: boolean;
    diff_hunk: string;
    review_id: number;
    html_url: string;
    thread_id: string;
    resolved: boolean;
    resolved_by: string;
    reactions: unknown[];
}

export interface ReviewJSON {
    id: number;
    user: string;
    body: string;
    state: string;
    submitted_at: string;
    html_url: string;
    reactions: unknown[];
}

export interface PRFixture {
    item: ReviewItem;
    body: string;
    baseRef: string;
    headRef: string;
    repoPath: string;
    diff: string;
    comments: CommentJSON[];
    reviews: ReviewJSON[];
    plugins: Record<string, unknown>;
}

const created = (daysAgo: number) =>
    new Date(Date.now() - daysAgo * 24 * 60 * 60 * 1000).toISOString();

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
        created_at: created(1),
        required_teams: [],
        ...fields,
    } satisfies ReviewItem;
}

export function comment(fields: Partial<CommentJSON> & Pick<CommentJSON, 'id' | 'body'>) {
    return {
        author: 'bob',
        path: '',
        position: '',
        in_reply_to: 0,
        created_at: created(0.5),
        outdated: false,
        diff_hunk: '',
        review_id: 0,
        html_url: '',
        thread_id: '',
        resolved: false,
        resolved_by: '',
        reactions: [],
        ...fields,
    } satisfies CommentJSON;
}

// The PR's diff, file order as GitHub returns it. GitHub comment positions
// count from the first line after each file's first @@ header. Built from a
// line list so the single-space prefix of blank context lines is explicit.
export const WIDGETS_DIFF = [
    'diff --git a/README.md b/README.md',
    'index 5d1c2a1..9f0e7b3 100644',
    '--- a/README.md',
    '+++ b/README.md',
    '@@ -1,3 +1,5 @@',
    ' # widgets',
    ' ',
    ' A tiny example project.',
    '+',
    '+Run `bun src/main.ts` to print a greeting.',
    'diff --git a/src/greet.ts b/src/greet.ts',
    'index 1c4e9d2..7a2b8f0 100644',
    '--- a/src/greet.ts',
    '+++ b/src/greet.ts',
    '@@ -3,7 +3,8 @@',
    ' export interface Greeting {',
    '     name: string;',
    '+    punctuation: string;',
    ' }',
    ' ',
    ' export function formatGreeting(greeting: Greeting): string {',
    '-    return `Hello, ${greeting.name}!`;',
    '+    return `Hello, ${greeting.name}${greeting.punctuation}`;',
    ' }',
    'diff --git a/src/main.ts b/src/main.ts',
    'index 3b18e51..a1c9d2f 100644',
    '--- a/src/main.ts',
    '+++ b/src/main.ts',
    '@@ -1,3 +1,4 @@',
    " import { formatGreeting } from './greet';",
    ' ',
    "-console.log('hello');",
    "+const message = formatGreeting({ name: 'world', punctuation: '!' });",
    '+console.log(message);',
    '',
].join('\n');

const GADGETS_DIFF = [
    'diff --git a/overflow.go b/overflow.go',
    'index 0a1b2c3..3c2b1a0 100644',
    '--- a/overflow.go',
    '+++ b/overflow.go',
    '@@ -1,3 +1,3 @@',
    ' package gadgets',
    ' ',
    '-const MaxWidth = 80',
    '+const MaxWidth = 120',
    '',
].join('\n');

export function buildFixtures(): PRFixture[] {
    return [
        {
            item: item({ title: 'Add greeting helper', repo: 'widgets', number: 42 }),
            body: 'Adds a `formatGreeting` helper and uses it from the CLI.\n\nCloses #12.',
            baseRef: 'main',
            headRef: 'alice/greeting',
            repoPath: FIXTURE_REPO,
            diff: WIDGETS_DIFF,
            comments: [
                comment({
                    id: '5001',
                    author: 'bob',
                    body: 'Should punctuation have a default?',
                    path: 'src/greet.ts',
                    position: '3',
                    review_id: 700,
                }),
                comment({
                    id: '5002',
                    author: 'alice',
                    body: 'Callers always pass one today, so no.',
                    path: 'src/greet.ts',
                    position: '3',
                    in_reply_to: 5001,
                }),
            ],
            reviews: [
                {
                    id: 700,
                    user: 'bob',
                    body: 'A couple of questions.',
                    state: 'COMMENTED',
                    submitted_at: created(0.5),
                    html_url: 'https://github.com/acme/widgets/pull/42#pullrequestreview-700',
                    reactions: [],
                },
            ],
            plugins: {
                summarize: {
                    result: 'This PR adds a greeting helper.',
                    status: 'success',
                    body: {
                        body_type: 'markdown',
                        body_content: '## Summary\n\nThis PR adds a **greeting helper**.',
                    },
                    annotations: [],
                },
            },
        },
        {
            item: item({
                title: 'Refactor build scripts',
                repo: 'widgets',
                number: 43,
                status: 'WAITING',
                tags: 'widgets,draft',
                author: 'dave',
            }),
            body: 'Work in progress.',
            baseRef: 'main',
            headRef: 'dave/build',
            repoPath: FIXTURE_REPO,
            diff: WIDGETS_DIFF,
            comments: [],
            reviews: [],
            plugins: {},
        },
        {
            item: item({
                title: 'Fix gadget overflow',
                repo: 'gadgets',
                number: 7,
                section: 'My PRs',
                section_priority: 2,
                author: 'carol',
            }),
            body: 'Widens the gadget.',
            baseRef: 'main',
            headRef: 'carol/overflow',
            // Not cloned locally: the review view must disable LSP.
            repoPath: '',
            diff: GADGETS_DIFF,
            comments: [],
            reviews: [],
            plugins: {},
        },
        {
            item: item({
                title: 'Bump dependencies',
                repo: 'widgets',
                number: 40,
                section: 'Recently Merged',
                section_priority: 3,
                status: 'DONE',
                tags: 'widgets,merged',
                author: 'erin',
                created_at: created(9),
            }),
            body: 'Routine bump.',
            baseRef: 'main',
            headRef: 'erin/deps',
            repoPath: FIXTURE_REPO,
            diff: WIDGETS_DIFF,
            comments: [],
            reviews: [],
            plugins: {},
        },
    ];
}
