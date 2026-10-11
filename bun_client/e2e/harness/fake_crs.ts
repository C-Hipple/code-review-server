// Stand-in for the Go `crs --server` binary. server.ts spawns whatever `crs`
// is first on PATH and talks newline-delimited JSON-RPC 1.0 to it over stdio,
// so putting a shim for this script on PATH lets the e2e suite drive the real
// Bun bridge and the real frontend without GitHub, a token, or a SQLite DB.
//
// State (local comments, submitted reviews, feedback) lives in memory and is
// reset by the `E2E.Reset` method, which tests reach through the bridge's
// generic `/api/rpc` endpoint. `E2E.Calls` returns every RPC received since
// the last reset so tests can assert on exactly what the UI sent.
//
// With `--listen host:port` it stands in for a hosted `crs --listen` instead:
// the same methods over TCP, each connection a session that starts
// unauthenticated, with Authenticate / RegisterUser per the contract in
// docs/protocol.md and the server's method policy — `not authenticated:`
// before a session, `permission denied:` for the admin-only methods, and
// `refused:` for SubmitReview / MergePR while the identity gate is shut (see
// the remote-mode hooks at the end of `handlers`). Registered users outlive
// `E2E.Reset`, as they outlive a sync cycle on the real server.

import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Socket } from 'bun';
import {
    buildFixtures,
    comment,
    commentsAddressedReport,
    type CommentJSON,
    type PRFixture,
    type ReviewJSON,
} from '../fixtures/prs';

interface Request {
    method: string;
    params: Record<string, any>[];
    id: number | string;
}

// The change diagram the fake serves for every PR, as the server stores it:
// the model's flowchart, with the change classes the server defines appended.
const DIAGRAM_SOURCE = `flowchart TD
    main["src/main.ts: main"]:::changed --> greet["src/greet.ts: greet"]:::added
    greet --> punct["src/greet.ts: punctuation"]:::added
    classDef added fill:#dcfce7,stroke:#16a34a,color:#14532d
    classDef changed fill:#fef3c7,stroke:#d97706,color:#78350f
    classDef removed fill:#fee2e2,stroke:#dc2626,color:#7f1d1d,stroke-dasharray: 5 5`;

interface State {
    prs: PRFixture[];
    localComments: Map<string, CommentJSON[]>;
    feedback: Map<string, string>;
    // `refused` is the policy error a listen-mode call was answered with.
    calls: { method: string; params: unknown; refused?: string }[];
    failNext: Map<string, string>;
    // Methods whose next call is recorded but left unanswered, and the calls
    // held that way, each answered by `E2E.Release`.
    holdNext: Set<string>;
    held: Map<string, () => void>;
    syncUpdated: boolean;
    nextCommentId: number;
    nextReviewId: number;
    // AI features: whether comments-addressed is enabled, whether review-ease
    // is (the fixtures' ratings are served only while it is), whether
    // change-diagram is and the Mermaid source it serves, the runs in flight
    // (polls left before each lands) and the results there are — both keyed
    // by PR and feature.
    aiEnabled: boolean;
    reviewEaseEnabled: boolean;
    diagramEnabled: boolean;
    diagramSource: string;
    aiRuns: Map<string, number>;
    aiDone: Set<string>;
    // What the Server Configuration tab edits: UpdateConfig merges each field
    // it is sent into this, the way the server merges an update into the file.
    config: Record<string, unknown>;
    // Listen mode: whether the server lets remote users act under its GitHub
    // identity (AllowServerIdentityActions).
    identityActions: boolean;
}

const CONFIG_PATH = '/tmp/crs-e2e/codereviewserver.toml';

function freshConfig(): Record<string, unknown> {
    return {
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
        Plugins: [
            {
                Name: 'summarize',
                Command: 'summarize_diff',
                IncludeDiff: true,
                IncludeHeaders: true,
                IncludeComments: false,
                IncludeBranch: false,
                OnlyOnDemand: false,
                Provider: '',
                Model: '',
            },
        ],
        AI: { DefaultProvider: '', DefaultCommand: '', DefaultModel: '' },
        AIFeatures: [],
    };
}

function freshState(): State {
    return {
        prs: buildFixtures(),
        localComments: new Map(),
        feedback: new Map(),
        calls: [],
        failNext: new Map(),
        holdNext: new Set(),
        held: new Map(),
        syncUpdated: false,
        nextCommentId: 9001,
        nextReviewId: 800,
        aiEnabled: true,
        reviewEaseEnabled: true,
        diagramEnabled: false,
        diagramSource: DIAGRAM_SOURCE,
        aiRuns: new Map(),
        aiDone: new Set(),
        config: freshConfig(),
        identityActions: false,
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

// The rating the server serves for a PR: none while review-ease is off.
function reviewEase(pr: PRFixture): string {
    return state.reviewEaseEnabled ? pr.item.review_ease : '';
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
            review_ease: reviewEase(pr),
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

const AI_FEATURE = {
    id: 'comments-addressed',
    name: 'Comments addressed?',
    description: 'Reports which review comments are still outstanding.',
    automatic: false,
    mode: 'oneshot',
    modes: ['oneshot', 'agent'],
    provider: 'gemini',
};

// Features whose results the server applies to what it already serves — the
// diff's file order, the review list's ease pill — rather than a report. They
// are listed as enabled, the way a server whose config switches them on lists
// them, so the tests that expect no AI buttons also prove the client never
// offers a report for one. Only review-ease can be switched off
// (E2E.SetReviewEase).
const APPLIED_AI_FEATURES = [
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
];

// change-diagram: off unless a test turns it on (E2E.SetDiagram), so the
// other specs see only comments-addressed.
const DIAGRAM_FEATURE = {
    id: 'change-diagram',
    name: 'Change diagram',
    description: 'Draws a Mermaid diagram of what the PR changes.',
    automatic: false,
    mode: 'oneshot',
    modes: ['oneshot'],
    provider: 'gemini',
};

// Every AI feature as GetConfig describes it for the config editor. The
// fixtures' review-ease ratings stand for a config that still switches it on
// with the legacy root-level key.
function configAIFeatureTypes() {
    const features: Array<{
        id: string;
        name: string;
        description: string;
        modes: string[];
        applied?: boolean;
    }> = [AI_FEATURE, DIAGRAM_FEATURE, ...APPLIED_AI_FEATURES];
    return features.map(f => ({
        id: f.id,
        name: f.name,
        description: f.description,
        modes: f.modes,
        applied: !!f.applied,
        ...(f.id === 'review-ease'
            ? {
                  legacy_key: 'ExperimentalLLMReviewEase',
                  legacy: {
                      ID: 'review-ease',
                      Enabled: true,
                      Mode: '',
                      Automatic: true,
                      Provider: 'gemini',
                      Command: '',
                      Model: '',
                  },
              }
            : {}),
    }));
}

function configPayload(message = '') {
    return {
        okay: true,
        message,
        path: CONFIG_PATH,
        using_defaults: false,
        config: state.config,
        workflow_types: [],
        filters: [],
        ai_features: configAIFeatureTypes(),
    };
}

// The report features the fake knows, each with whether it is enabled now.
function reportFeatures() {
    return [
        { ...AI_FEATURE, enabled: state.aiEnabled },
        { ...DIAGRAM_FEATURE, enabled: state.diagramEnabled },
    ];
}

// A feature's canned result, once its run has landed.
function aiResult(pr: PRFixture, feature: string) {
    if (feature === DIAGRAM_FEATURE.id) {
        return {
            body: '```mermaid\n' + state.diagramSource + '\n```\n',
            report: { mermaid: state.diagramSource, diagram_type: 'flowchart' },
            outstanding: null,
        };
    }
    return commentsAddressedReport(pr);
}

// One feature's output for a PR, in the shape GetAIOutput serves. A run in
// flight reads "pending" for one poll, then the canned report lands.
function aiOutput(pr: PRFixture, feature = AI_FEATURE) {
    const k = `${key(pr.item.owner, pr.item.repo, pr.item.number)}:${feature.id}`;
    const base = {
        feature: feature.id,
        name: feature.name,
        annotations: [],
        covers_sha: '',
        covers_digest: '',
        current_sha: 'e2e-sha',
        current_digest: 'e2e-digest',
        stale: false,
        truncated: false,
    };
    const pollsLeft = state.aiRuns.get(k);
    if (pollsLeft !== undefined) {
        if (pollsLeft > 0) {
            state.aiRuns.set(k, pollsLeft - 1);
            return {
                ...base,
                status: 'pending',
                body: { body_type: 'markdown', body_content: '' },
                report: null,
                outstanding: null,
                updated_at: '',
            };
        }
        state.aiRuns.delete(k);
        state.aiDone.add(k);
    }
    if (!state.aiDone.has(k)) {
        return {
            ...base,
            status: 'not-run',
            body: { body_type: 'markdown', body_content: '' },
            report: null,
            outstanding: null,
            updated_at: '',
        };
    }
    const { report, outstanding, body } = aiResult(pr, feature.id);
    return {
        ...base,
        status: 'success',
        body: { body_type: 'markdown', body_content: body },
        report,
        outstanding,
        covers_sha: 'e2e-sha',
        covers_digest: 'e2e-digest',
        updated_at: new Date().toISOString(),
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

// --- Sessions (listen mode) ---------------------------------------------------

/** Who a connection is. Stdio's is the local user, an administrator. */
interface Session {
    /** Null until Authenticate or RegisterUser succeeds; '' is the server's own identity. */
    login: string | null;
    admin: boolean;
    /** The stdio session, which the method policy never applies to. */
    local: boolean;
}

/** One client: its session, and where its replies go. */
interface Peer {
    session: Session;
    send(line: string): void;
    /** Hang up on it, as a server restart would. */
    close(): void;
}

interface RemoteUser {
    login: string;
    token: string;
    admin: boolean;
}

// Registered users by lowercased login (logins are case-insensitive), and every
// Authenticate / RegisterUser with its reply, oldest first. E2E.Reset clears
// neither: a bridge registers once, when it connects.
const users = new Map<string, RemoteUser>();
const authLog: { method: string; params: unknown; reply: unknown }[] = [];
const peers = new Set<Peer>();

// Secrets come from the environment, as on the real server.
const REGISTRATION_KEY = process.env.CRS_REGISTRATION_KEY ?? '';
const ADMIN_TOKEN = process.env.CRS_ADMIN_TOKEN ?? '';
const TOKEN_PATTERN = /^crs_[0-9a-f]{64}$/;
const LOGIN_PATTERN = /^[A-Za-z0-9](?:-?[A-Za-z0-9]){0,38}$/;
const MAX_WORKFLOWS = 50;

const PUBLIC_METHODS = new Set([
    'RPCHandler.Hello',
    'RPCHandler.Authenticate',
    'RPCHandler.RegisterUser',
]);
const ADMIN_METHODS = new Set([
    'RPCHandler.GetConfig',
    'RPCHandler.UpdateConfig',
    'RPCHandler.RerunPlugins',
    'RPCHandler.MergePR',
    'RPCHandler.ListUsers',
    'RPCHandler.DeleteUser',
]);
// Actions that go out under the server's GitHub identity.
const IDENTITY_METHODS = new Set(['RPCHandler.SubmitReview', 'RPCHandler.MergePR']);

/** The error the server's method policy answers `method` with, if it turns it down. */
function refusal(method: string, session: Session): string | null {
    if (session.local || method.startsWith('E2E.') || PUBLIC_METHODS.has(method)) return null;
    if (session.login === null) {
        return `not authenticated: ${method} needs a session; call RPCHandler.Authenticate or RPCHandler.RegisterUser first`;
    }
    if (ADMIN_METHODS.has(method) && !session.admin) {
        return `permission denied: ${method} requires an administrator`;
    }
    if (IDENTITY_METHODS.has(method) && !state.identityActions) {
        return `refused: ${method} would act as the server's GitHub account, which this server does not allow (AllowServerIdentityActions is off)`;
    }
    return null;
}

function authenticate(args: Record<string, any>, peer: Peer) {
    const token = String(args.Token ?? '');
    if (ADMIN_TOKEN && token === ADMIN_TOKEN) {
        peer.session = { login: '', admin: true, local: false };
        return { okay: true, known: true, login: '', is_admin: true, message: '' };
    }
    const user = [...users.values()].find(u => u.token === token);
    if (!user) {
        return { okay: false, known: false, login: '', is_admin: false, message: 'unknown token' };
    }
    peer.session = { login: user.login, admin: user.admin, local: false };
    return { okay: true, known: true, login: user.login, is_admin: user.admin, message: '' };
}

function registerUser(args: Record<string, any>, peer: Peer) {
    const refuse = (message: string, errors: unknown[] = []) => ({
        okay: false,
        created: false,
        login: '',
        token: '',
        is_admin: false,
        message,
        errors,
    });
    if (!REGISTRATION_KEY) return refuse('registration is disabled on this server');
    if (args.RegistrationKey !== REGISTRATION_KEY) return refuse('wrong registration key');
    const login = String(args.Login ?? '').trim();
    if (!LOGIN_PATTERN.test(login)) return refuse(`"${login}" is not a valid GitHub login`);

    const workflows: unknown = args.Workflows ?? [];
    const errors: { workflow: number; field: string; message: string }[] = [];
    if (!Array.isArray(workflows)) {
        errors.push({ workflow: -1, field: 'Workflows', message: 'must be a list' });
    } else {
        if (workflows.length > MAX_WORKFLOWS) {
            errors.push({ workflow: -1, field: 'Workflows', message: `at most ${MAX_WORKFLOWS}` });
        }
        const seen = new Set<string>();
        workflows.forEach((w, i) => {
            const name = String(w?.Name ?? '');
            if (!name) errors.push({ workflow: i, field: 'Name', message: 'is required' });
            else if (seen.has(name)) {
                errors.push({ workflow: i, field: 'Name', message: `duplicate name "${name}"` });
            }
            seen.add(name);
        });
    }
    if (errors.length > 0) return refuse('the workflows are not valid', errors);

    const token = String(args.Token ?? '');
    const known = users.get(login.toLowerCase());
    if (known && known.token !== token) return refuse('login already registered');
    const user: RemoteUser = known ?? {
        login,
        token: TOKEN_PATTERN.test(token) ? token : `crs_${randomBytes(32).toString('hex')}`,
        admin: false,
    };
    if (ADMIN_TOKEN && args.AdminToken === ADMIN_TOKEN) user.admin = true;
    users.set(login.toLowerCase(), user);
    peer.session = { login: user.login, admin: user.admin, local: false };
    return {
        okay: true,
        created: !known,
        login: user.login,
        token: user.token,
        is_admin: user.admin,
        message: '',
        errors: [],
    };
}

const REVIEW_STATE: Record<string, string> = {
    APPROVE: 'APPROVED',
    REQUEST_CHANGES: 'CHANGES_REQUESTED',
    COMMENT: 'COMMENTED',
};

const handlers: Record<string, (args: any, peer: Peer) => unknown> = {
    'RPCHandler.Hello': () => ({ okay: true }),

    'RPCHandler.Authenticate': (args, peer) => {
        const reply = authenticate(args, peer);
        authLog.push({ method: 'RPCHandler.Authenticate', params: args, reply });
        return reply;
    },

    'RPCHandler.RegisterUser': (args, peer) => {
        const reply = registerUser(args, peer);
        authLog.push({ method: 'RPCHandler.RegisterUser', params: args, reply });
        return reply;
    },

    'RPCHandler.GetAllReviews': () => ({
        okay: true,
        content: '',
        // Counted like the server does, from the PR's GitHub comments: local
        // drafts don't count until they are submitted.
        items: state.prs.map(p => ({
            ...p.item,
            review_ease: reviewEase(p),
            comment_count: p.comments.length,
        })),
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

    'RPCHandler.GetConfig': () => configPayload(),

    // Accepts whatever it is sent: validation is the real server's, and the
    // client's own is what the e2e suite exercises.
    'RPCHandler.UpdateConfig': args => {
        for (const [field, value] of Object.entries(args)) {
            if (value !== null && value !== undefined) state.config[field] = value;
        }
        return { ...configPayload(`Configuration saved to ${CONFIG_PATH}`), errors: [] };
    },

    'RPCHandler.GetRateLimitHistory': () => ({ hours_back: 3, since: '', points: [] }),

    'RPCHandler.GetImage': () => ({ okay: false, error: 'no images in e2e fixtures' }),

    'RPCHandler.ListAIFeatures': () => ({
        features: [
            ...reportFeatures(),
            ...APPLIED_AI_FEATURES.map(f =>
                f.id === 'review-ease' ? { ...f, enabled: state.reviewEaseEnabled } : f
            ),
        ],
    }),

    'RPCHandler.RunAIFeature': args => {
        const pr = findPR(args);
        const feature = reportFeatures().find(f => f.id === args.Feature);
        if (!feature) {
            return {
                okay: false,
                outcome: 'unknown-feature',
                message: `There is no AI feature named "${args.Feature}"`,
                output: null,
            };
        }
        if (!feature.enabled) {
            return {
                okay: false,
                outcome: 'disabled',
                message: 'not enabled',
                output: aiOutput(pr, feature),
            };
        }
        const k = `${key(pr.item.owner, pr.item.repo, pr.item.number)}:${feature.id}`;
        if (state.aiRuns.has(k)) {
            return {
                okay: true,
                outcome: 'already-running',
                message: '',
                output: aiOutput(pr, feature),
            };
        }
        if (!args.Force && state.aiDone.has(k)) {
            return {
                okay: true,
                outcome: 'up-to-date',
                message: '',
                output: aiOutput(pr, feature),
            };
        }
        state.aiDone.delete(k);
        state.aiRuns.set(k, 1);
        return {
            okay: true,
            outcome: 'started',
            message: `Running ${feature.name} for PR ${pr.item.number}`,
            output: aiOutput(pr, feature),
        };
    },

    // One feature when asked for (enabled or not), else every enabled one.
    'RPCHandler.GetAIOutput': args => {
        const pr = findPR(args);
        const features = args.Feature
            ? reportFeatures().filter(f => f.id === args.Feature)
            : reportFeatures().filter(f => f.enabled);
        return { output: Object.fromEntries(features.map(f => [f.id, aiOutput(pr, f)])) };
    },

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

    // Leave the next call to `method` unanswered until `E2E.Release`, so a test
    // can look at the UI while it waits. The call is handled at release, as if
    // the backend had been busy with it all along.
    'E2E.HoldNext': args => {
        state.holdNext.add(args.method);
        return { okay: true };
    },

    'E2E.Release': args => {
        const answer = state.held.get(args.method);
        if (!answer) throw new Error(`no ${args.method} call is being held`);
        state.held.delete(args.method);
        answer();
        return { okay: true };
    },

    'E2E.SetSyncUpdated': args => {
        state.syncUpdated = !!args.updated;
        return { okay: true };
    },

    'E2E.SetAIEnabled': args => {
        state.aiEnabled = !!args.enabled;
        return { okay: true };
    },

    // Turn review-ease on or off: while it is off no PR carries a rating.
    'E2E.SetReviewEase': args => {
        state.reviewEaseEnabled = !!args.enabled;
        return { okay: true };
    },

    // Turn change-diagram on or off, optionally serving other Mermaid source.
    'E2E.SetDiagram': args => {
        state.diagramEnabled = !!args.enabled;
        if (typeof args.mermaid === 'string') state.diagramSource = args.mermaid;
        return { okay: true };
    },

    // --- Remote mode (--listen) ------------------------------------------------

    // Every Authenticate / RegisterUser since the fake started, with its reply.
    'E2E.AuthLog': () => ({ calls: authLog }),

    // Forget a registered user, as the server's retention sweep does. With
    // `Disconnect` their connections are closed too, as by a server restart,
    // and the bridge reconnects; without, they stay open but lose their
    // session, so the next call is refused as not authenticated.
    'E2E.ForgetUser': args => {
        const login = String(args.Login ?? '').toLowerCase();
        const forgotten = users.delete(login);
        let sessions = 0;
        for (const peer of peers) {
            if (peer.session.login?.toLowerCase() !== login) continue;
            sessions++;
            if (args.Disconnect) {
                // Once this reply has gone out on what may be the same connection.
                setTimeout(() => peer.close(), 20);
            } else {
                peer.session = { login: null, admin: false, local: false };
            }
        }
        return { okay: forgotten, sessions };
    },

    // Open or shut the identity gate (AllowServerIdentityActions); shut by default.
    'E2E.SetIdentityActions': args => {
        state.identityActions = !!args.allowed;
        return { okay: true };
    },
};

function respond(peer: Peer, id: Request['id'], result: unknown, error: string | null) {
    peer.send(`${JSON.stringify({ id, result: error ? null : result, error })}\n`);
}

function handle(req: Request, peer: Peer) {
    const args = req.params?.[0] ?? {};
    const refused = refusal(req.method, peer.session);
    if (!req.method.startsWith('E2E.')) {
        state.calls.push({ method: req.method, params: args, ...(refused ? { refused } : {}) });
    }
    if (refused) {
        respond(peer, req.id, null, refused);
        return;
    }
    if (state.holdNext.delete(req.method)) {
        state.held.set(req.method, () => answer(req, args, peer));
        return;
    }
    answer(req, args, peer);
}

function answer(req: Request, args: Record<string, any>, peer: Peer) {
    const injected = state.failNext.get(req.method);
    if (injected !== undefined) {
        state.failNext.delete(req.method);
        respond(peer, req.id, null, injected);
        return;
    }
    const handler = handlers[req.method];
    if (!handler) {
        respond(peer, req.id, null, `rpc: can't find method ${req.method}`);
        return;
    }
    try {
        respond(peer, req.id, handler(args, peer), null);
    } catch (e) {
        respond(peer, req.id, null, e instanceof Error ? e.message : String(e));
    }
}

/** Calls `onLine` with each complete line of the chunks pushed into it. */
function lineReader(onLine: (line: string) => void) {
    let buffer = '';
    return (chunk: string) => {
        buffer += chunk;
        let nl = buffer.indexOf('\n');
        while (nl !== -1) {
            const line = buffer.slice(0, nl).trim();
            buffer = buffer.slice(nl + 1);
            if (line) onLine(line);
            nl = buffer.indexOf('\n');
        }
    };
}

function serveStdio() {
    const peer: Peer = {
        session: { login: '', admin: true, local: true },
        send: line => process.stdout.write(line),
        close: () => process.exit(0),
    };
    process.stdin.setEncoding('utf-8');
    process.stdin.on(
        'data',
        lineReader(line => handle(JSON.parse(line), peer))
    );
    process.stdin.on('end', () => process.exit(0));
}

interface Connection {
    peer: Peer;
    push: (chunk: string) => void;
    decoder: TextDecoder;
    // Bytes the socket didn't take yet, written on `drain`.
    unsent: Buffer | null;
}

function write(socket: Socket<Connection>, bytes: Buffer) {
    const conn = socket.data;
    if (conn.unsent) {
        conn.unsent = Buffer.concat([conn.unsent, bytes]);
        return;
    }
    const written = Math.max(socket.write(bytes), 0);
    if (written < bytes.length) conn.unsent = bytes.subarray(written);
}

function listen(address: string) {
    const at = address.lastIndexOf(':');
    Bun.listen<Connection>({
        hostname: address.slice(0, at),
        port: parseInt(address.slice(at + 1), 10),
        socket: {
            open(socket) {
                const peer: Peer = {
                    session: { login: null, admin: false, local: false },
                    send: line => write(socket, Buffer.from(line)),
                    close: () => socket.end(),
                };
                socket.data = {
                    peer,
                    push: lineReader(line => handle(JSON.parse(line), peer)),
                    decoder: new TextDecoder(),
                    unsent: null,
                };
                peers.add(peer);
            },
            data(socket, chunk) {
                socket.data.push(socket.data.decoder.decode(chunk, { stream: true }));
            },
            drain(socket) {
                const pending = socket.data.unsent;
                socket.data.unsent = null;
                if (pending) write(socket, pending);
            },
            close(socket) {
                peers.delete(socket.data.peer);
            },
        },
    });
    console.error(`[fake crs] listening on ${address}`);
}

const listenAt = process.argv.indexOf('--listen');
if (listenAt === -1) {
    serveStdio();
} else {
    listen(process.argv[listenAt + 1] ?? '');
}
