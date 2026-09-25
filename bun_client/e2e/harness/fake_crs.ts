// Stand-in for the Go `crs --server` binary. server.ts spawns whatever `crs`
// is first on PATH and talks newline-delimited JSON-RPC 1.0 to it over stdio,
// so putting a shim for this script on PATH lets the e2e suite drive the real
// Bun bridge and the real frontend without GitHub, a token, or a SQLite DB.
//
// State (local comments, submitted reviews, feedback) lives in memory and is
// reset by the `E2E.Reset` method, which tests reach through the bridge's
// generic `/api/rpc` endpoint. `E2E.Calls` returns every RPC received since
// the last reset so tests can assert on exactly what the UI sent.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import {
    buildFixtures,
    comment,
    type CommentJSON,
    type PRFixture,
    type ReviewJSON,
} from '../fixtures/prs';

interface Request {
    method: string;
    params: Record<string, any>[];
    id: number | string;
}

interface State {
    prs: PRFixture[];
    localComments: Map<string, CommentJSON[]>;
    feedback: Map<string, string>;
    calls: { method: string; params: unknown }[];
    failNext: Map<string, string>;
    syncUpdated: boolean;
    nextCommentId: number;
    nextReviewId: number;
}

function freshState(): State {
    return {
        prs: buildFixtures(),
        localComments: new Map(),
        feedback: new Map(),
        calls: [],
        failNext: new Map(),
        syncUpdated: false,
        nextCommentId: 9001,
        nextReviewId: 800,
    };
}

let state = freshState();

const key = (owner: string, repo: string, number: number) => `${owner}/${repo}#${number}`;

function findPR(args: { Owner: string; Repo: string; Number: number }): PRFixture {
    const pr = state.prs.find(
        p =>
            p.item.owner === args.Owner &&
            p.item.repo === args.Repo &&
            p.item.number === Number(args.Number)
    );
    if (!pr) {
        throw new Error(`PR ${args.Owner}/${args.Repo}#${args.Number} not found`);
    }
    return pr;
}

function locals(pr: PRFixture): CommentJSON[] {
    const k = key(pr.item.owner, pr.item.repo, pr.item.number);
    let list = state.localComments.get(k);
    if (!list) {
        list = [];
        state.localComments.set(k, list);
    }
    return list;
}

function payload(pr: PRFixture) {
    const all = [...pr.comments, ...locals(pr)];
    const reviewers = (s: string) =>
        Array.from(new Set(pr.reviews.filter(r => r.state === s).map(r => r.user)));
    return {
        okay: true,
        content: `* ${pr.item.title}`,
        metadata: {
            number: pr.item.number,
            title: pr.item.title,
            author: pr.item.author,
            base_ref: pr.baseRef,
            head_ref: pr.headRef,
            state: pr.item.status === 'DONE' ? 'closed' : 'open',
            milestone: '',
            labels: ['e2e'],
            assignees: [],
            reviewers: [],
            requested_teams: [],
            approved_by: reviewers('APPROVED'),
            changes_requested_by: reviewers('CHANGES_REQUESTED'),
            commented_by: reviewers('COMMENTED'),
            draft: pr.item.tags.includes('draft'),
            ci_status: 'success',
            ci_failures: [],
            body: pr.body,
            url: pr.item.url,
            repo_path: pr.repoPath,
            worktree_path: '',
            release_status: '',
            review_ease: '',
            changed_files: (pr.diff.match(/^diff --git /gm) || []).length,
            additions: (pr.diff.match(/^\+(?!\+\+)/gm) || []).length,
            deletions: (pr.diff.match(/^-(?!--)/gm) || []).length,
        },
        diff: pr.diff,
        comments: all.filter(c => !c.outdated),
        outdated_comments: all.filter(c => c.outdated),
        reviews: pr.reviews,
        commits: [],
        feedback: state.feedback.get(key(pr.item.owner, pr.item.repo, pr.item.number)) ?? '',
        annotations: [],
        images: [],
    };
}

// Lines of a file in the fixture checkout, for GetHunkContext.
function fileLines(pr: PRFixture, filename: string): string[] {
    if (!pr.repoPath) return [];
    try {
        return readFileSync(join(pr.repoPath, filename), 'utf-8').replace(/\n$/, '').split('\n');
    } catch {
        return [];
    }
}

const REVIEW_STATE: Record<string, string> = {
    APPROVE: 'APPROVED',
    REQUEST_CHANGES: 'CHANGES_REQUESTED',
    COMMENT: 'COMMENTED',
};

const handlers: Record<string, (args: any) => unknown> = {
    'RPCHandler.GetAllReviews': () => ({
        okay: true,
        content: '',
        items: state.prs.map(p => p.item),
    }),

    'RPCHandler.GetPR': args => payload(findPR(args)),

    'RPCHandler.SyncPR': args => ({ ...payload(findPR(args)), updated: state.syncUpdated }),

    'RPCHandler.GetAdjacentPR': args => {
        const items = state.prs.map(p => p.item);
        const idx = items.findIndex(
            i => i.owner === args.Owner && i.repo === args.Repo && i.number === args.Number
        );
        const step = args.Previous ? -1 : 1;
        const next = items[(idx + step + items.length) % items.length];
        return {
            ...payload(findPR({ Owner: next.owner, Repo: next.repo, Number: next.number })),
            adjacent_owner: next.owner,
            adjacent_repo: next.repo,
            adjacent_number: next.number,
        };
    },

    'RPCHandler.AddComment': args => {
        const pr = findPR(args);
        const id = state.nextCommentId++;
        locals(pr).push(
            comment({
                id: String(id),
                author: 'local',
                body: args.Body,
                path: args.Filename,
                position: String(args.Position),
                in_reply_to: args.ReplyToID ?? 0,
                created_at: new Date().toISOString(),
            })
        );
        return { ...payload(pr), id };
    },

    'RPCHandler.EditComment': args => {
        const pr = findPR(args);
        const c = locals(pr).find(l => l.id === String(args.ID));
        if (!c) throw new Error(`local comment ${args.ID} not found`);
        c.body = args.Body;
        return payload(pr);
    },

    'RPCHandler.DeleteComment': args => {
        const pr = findPR(args);
        const list = locals(pr);
        const idx = list.findIndex(l => l.id === String(args.ID));
        if (idx === -1) throw new Error(`local comment ${args.ID} not found`);
        list.splice(idx, 1);
        return payload(pr);
    },

    'RPCHandler.SubmitReview': args => {
        const pr = findPR(args);
        const review: ReviewJSON = {
            id: state.nextReviewId++,
            user: 'e2e-reviewer',
            body: args.Body ?? '',
            state: REVIEW_STATE[args.Event] ?? 'COMMENTED',
            submitted_at: new Date().toISOString(),
            html_url: `${pr.item.url}#pullrequestreview-${state.nextReviewId}`,
            reactions: [],
        };
        pr.reviews.push(review);
        // Pending local comments go out with the review and come back as
        // GitHub comments authored by the reviewer.
        for (const c of locals(pr).splice(0)) {
            pr.comments.push({ ...c, author: 'e2e-reviewer', review_id: review.id });
        }
        return { ...payload(pr), failed_replies: [] };
    },

    'RPCHandler.SetFeedback': args => {
        const pr = findPR(args);
        state.feedback.set(key(pr.item.owner, pr.item.repo, pr.item.number), args.Body);
        return { okay: true };
    },

    'RPCHandler.RemovePRComments': args => {
        locals(findPR(args)).splice(0);
        return { okay: true };
    },

    'RPCHandler.GetHunkContext': args => {
        const pr = findPR(args);
        const lines = fileLines(pr, args.Filename);
        let start: number;
        let end: number;
        if (args.Direction === 'before') {
            end = args.AnchorLine - 1;
            start = Math.max(1, end - args.Count + 1);
        } else {
            start = args.AnchorLine + 1;
            end = Math.min(lines.length, start + args.Count - 1);
        }
        const slice = end >= start ? lines.slice(start - 1, end) : [];
        const added = slice.length;
        const origStart = args.Direction === 'before' ? args.OrigStart - added : args.OrigStart;
        const newStart = args.Direction === 'before' ? args.NewStart - added : args.NewStart;
        const header = args.HunkHeader ? ` ${args.HunkHeader}` : '';
        return {
            lines: slice,
            start_line: start,
            end_line: end,
            range_header: `@@ -${origStart},${args.OrigLength + added} +${newStart},${
                args.NewLength + added
            } @@${header}`,
        };
    },

    'RPCHandler.ListPlugins': () => ({ plugins: ['summarize'] }),

    'RPCHandler.GetPluginOutput': args => ({ output: findPR(args).plugins }),

    'RPCHandler.RerunPlugins': args => {
        findPR(args);
        return { okay: true };
    },

    'RPCHandler.GetConfig': () => ({
        okay: true,
        message: '',
        path: '/tmp/crs-e2e/codereviewserver.toml',
        using_defaults: false,
        config: {
            Repos: ['acme/widgets', 'acme/gadgets'],
            SleepDuration: 10,
            JiraDomain: '',
            GithubUsername: 'e2e-reviewer',
            RepoLocation: '~/src',
            AutoWorktree: false,
            DesktopNotifications: false,
            SectionPriority: {},
            SectionSorting: {},
            Workflows: [],
            Plugins: [],
            ExperimentalLLMFileOrdering: false,
            ExperimentalLLMReviewEase: false,
        },
        workflow_types: [],
        filters: [],
    }),

    'RPCHandler.GetRateLimitHistory': () => ({ hours_back: 3, since: '', points: [] }),

    'RPCHandler.GetImage': () => ({ okay: false, error: 'no images in e2e fixtures' }),

    // --- Test control surface -------------------------------------------------

    'E2E.Reset': () => {
        state = freshState();
        return { okay: true };
    },

    'E2E.Calls': () => ({ calls: state.calls }),

    // Make the next call to `method` fail with `message`.
    'E2E.FailNext': args => {
        state.failNext.set(args.method, args.message ?? 'injected failure');
        return { okay: true };
    },

    'E2E.SetSyncUpdated': args => {
        state.syncUpdated = !!args.updated;
        return { okay: true };
    },
};

function respond(id: Request['id'], result: unknown, error: string | null) {
    process.stdout.write(`${JSON.stringify({ id, result: error ? null : result, error })}\n`);
}

function handle(req: Request) {
    const args = req.params?.[0] ?? {};
    if (!req.method.startsWith('E2E.')) {
        state.calls.push({ method: req.method, params: args });
    }
    const injected = state.failNext.get(req.method);
    if (injected !== undefined) {
        state.failNext.delete(req.method);
        respond(req.id, null, injected);
        return;
    }
    const handler = handlers[req.method];
    if (!handler) {
        respond(req.id, null, `rpc: can't find method ${req.method}`);
        return;
    }
    try {
        respond(req.id, handler(args), null);
    } catch (e) {
        respond(req.id, null, e instanceof Error ? e.message : String(e));
    }
}

let buffer = '';
process.stdin.setEncoding('utf-8');
process.stdin.on('data', (chunk: string) => {
    buffer += chunk;
    let nl = buffer.indexOf('\n');
    while (nl !== -1) {
        const line = buffer.slice(0, nl).trim();
        buffer = buffer.slice(nl + 1);
        if (line) handle(JSON.parse(line));
        nl = buffer.indexOf('\n');
    }
});
process.stdin.on('end', () => process.exit(0));
