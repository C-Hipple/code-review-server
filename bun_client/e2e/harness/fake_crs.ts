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
// unauthenticated, answered the way the Go server answers them (the error
// strings and reply fields are copied from server/policy.go, users.go,
// write_identity.go and github_link.go):
//
//   - Authenticate / RegisterUser per docs/protocol.md, with the
//     github_linked / github_link_available flags;
//   - the method policy: `not authenticated:` before a session,
//     `permission denied:` for the administrator's methods and for SkipCache;
//   - the write resolver for SubmitReview and MergePR: a linked user writes
//     as themselves; otherwise the server's account only with the identity
//     gate open (a merge then also needs an administrator); otherwise
//     `refused: link-github:` where linking is available, `refused:` where
//     it is not;
//   - StartGitHubLink / GetGitHubLink / UnlinkGitHub, the device flow a test
//     ends with `E2E.EndGitHubLink` the way GitHub would.
//
// Drafts and feedback are each login's own, as on the server: the stdio
// session's are the server's own (login ''), and a user's go when the user
// does. So does a user's dashboard, which — as on the server, whose workflow
// cycle builds it — is empty after registering until `E2E.RunCycle`.
//
// Registered users outlive `E2E.Reset`, as they outlive a sync cycle on the
// real server; their GitHub links and link flows do not, and nor do their
// dashboards: every test starts before the first cycle. The remote-mode
// hooks are at the end of `handlers`.

import { createHash, randomBytes } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
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
    // Drafts and feedback by owner (lowercased login, '' for the server's
    // own) and PR: see ownKey.
    localComments: Map<string, CommentJSON[]>;
    feedback: Map<string, string>;
    // `refused` is the refusal a call was answered with: the method policy's,
    // or one its handler gave (SkipCache, the write resolver).
    calls: { method: string; params: unknown; refused?: string }[];
    failNext: Map<string, string>;
    // Methods whose next call is recorded but left unanswered, and the calls
    // held that way, each answered by `E2E.Release`.
    holdNext: Set<string>;
    held: Map<string, () => void>;
    // Listen mode: methods whose next call is carried out, then the
    // connection it came on is closed instead of answered (E2E.DropNext).
    dropNext: Set<string>;
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
    // Plugin re-runs in flight: reads of the output left before each lands,
    // keyed by PR and plugin.
    pluginRuns: Map<string, number>;
    // What the Server Configuration tab edits: UpdateConfig merges each field
    // it is sent into this, the way the server merges an update into the file.
    config: Record<string, unknown>;
    // Listen mode: whether the server lets remote users act under its GitHub
    // identity (AllowServerIdentityActions).
    identityActions: boolean;
    // Listen mode: whether the server can link GitHub accounts (it has a
    // GitHubOAuthClientID and a CRS_SECRET_KEY), the poll interval it hands
    // out, and the device flows in progress or recently ended, by lowercased
    // login.
    linkAvailable: boolean;
    linkInterval: number;
    linkFlows: Map<string, LinkFlow>;
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
        dropNext: new Set(),
        syncUpdated: false,
        nextCommentId: 9001,
        nextReviewId: 800,
        aiEnabled: true,
        reviewEaseEnabled: true,
        diagramEnabled: false,
        diagramSource: DIAGRAM_SOURCE,
        aiRuns: new Map(),
        aiDone: new Set(),
        pluginRuns: new Map(),
        config: freshConfig(),
        identityActions: false,
        linkAvailable: false,
        linkInterval: 1,
        linkFlows: new Map(),
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

/** A PR's key for what `session`'s login owns of it: its drafts, its feedback. */
const ownKey = (session: Session, pr: PRFixture) =>
    `${(session.login ?? '').toLowerCase()}\0${key(pr.item.owner, pr.item.repo, pr.item.number)}`;

/** The drafts `session`'s login has left on `pr`. */
function locals(pr: PRFixture, session: Session): CommentJSON[] {
    const k = ownKey(session, pr);
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

function payload(pr: PRFixture, session: Session) {
    const all = [...pr.comments, ...locals(pr, session)];
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
        feedback: state.feedback.get(ownKey(session, pr)) ?? '',
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

// A PR's plugin output, as GetPluginOutput serves it. A re-run deletes the
// plugin's result, as the server does, so the plugin is missing from the
// first read after it; it reads as pending for the next, then lands with a
// new result.
function pluginOutput(pr: PRFixture): Record<string, unknown> {
    const prefix = `${key(pr.item.owner, pr.item.repo, pr.item.number)}:`;
    for (const [k, readsLeft] of state.pluginRuns) {
        if (!k.startsWith(prefix)) continue;
        const name = k.slice(prefix.length);
        if (readsLeft === 0) {
            state.pluginRuns.delete(k);
            const content = `## Re-run\n\n${name} ran again.`;
            pr.plugins[name] = {
                result: content,
                status: 'success',
                body: { body_type: 'markdown', body_content: content },
                annotations: [],
            };
            continue;
        }
        state.pluginRuns.set(k, readsLeft - 1);
        if (readsLeft === 1) {
            pr.plugins[name] = {
                result: '',
                status: 'pending',
                body: { body_type: 'markdown', body_content: '' },
                annotations: [],
            };
        }
    }
    return pr.plugins;
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

/** A user's linked GitHub account, as the server stores it. */
interface GitHubLink {
    githubLogin: string;
    linkedAt: string;
    /**
     * `revoked`: revoked on github.com, which the server learns from the next
     * write's 401. `unreadable`: sealed under a CRS_SECRET_KEY the server no
     * longer has.
     */
    broken?: 'revoked' | 'unreadable';
}

interface RemoteUser {
    login: string;
    token: string;
    admin: boolean;
    workflows: unknown[];
    repos: string[];
    sectionPriority: Record<string, number>;
    sectionSorting: Record<string, string>;
    link?: GitHubLink;
    /** A workflow cycle has built their dashboard since they registered. */
    dashboard: boolean;
}

/** A device flow, waiting for its code or ended without a link. */
interface LinkFlow {
    status: 'pending' | 'expired' | 'denied' | 'mismatch' | 'error';
    message: string;
    userCode: string;
    verificationUri: string;
    expiresAt: Date;
    interval: number;
    endedAt: number;
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

// The method policy (server/policy.go) by bare method name. As there, any
// RPCHandler method not listed — and any method of another service — is an
// administrator's. E2E.* is this fake's own control surface, open to all.
const METHOD_POLICY: Record<string, 'public' | 'user' | 'admin'> = {
    Hello: 'public',
    Authenticate: 'public',
    RegisterUser: 'public',

    GetAllReviews: 'user',
    GetPR: 'user',
    GetAdjacentPR: 'user',
    SyncPR: 'user',
    AddComment: 'user',
    EditComment: 'user',
    DeleteComment: 'user',
    SetFeedback: 'user',
    RemovePRComments: 'user',
    GetMyWorkflows: 'user',
    UpdateMyWorkflows: 'user',
    SubmitReview: 'user',
    MergePR: 'user',
    StartGitHubLink: 'user',
    GetGitHubLink: 'user',
    UnlinkGitHub: 'user',
    ListPlugins: 'user',
    GetPluginOutput: 'user',
    ListAIFeatures: 'user',
    RunAIFeature: 'user',
    GetAIOutput: 'user',
    GetRateLimitStatus: 'user',
    GetRateLimitHistory: 'user',
    GetHunkContext: 'user',
    GetImage: 'user',

    GetConfig: 'admin',
    UpdateConfig: 'admin',
    RerunPlugins: 'admin',
    ListUsers: 'admin',
    DeleteUser: 'admin',
};

// The server's refusals, word for word. Clients match on their prefixes.
const notAuthenticated = (method: string) =>
    `not authenticated: ${method} needs an authenticated connection; call RPCHandler.Authenticate or RPCHandler.RegisterUser first`;
const permissionDenied = (method: string) =>
    `permission denied: ${method} requires an administrator`;
const SKIP_CACHE_NEEDS_ADMIN = 'permission denied: SkipCache requires an administrator';
const MERGE_AS_SERVER_NEEDS_ADMIN =
    "permission denied: RPCHandler.MergePR requires an administrator to merge as this server's GitHub account";
const DRAFTS_CHANGED =
    'drafts changed: some of the pending comments this review was to carry no longer exist (already submitted, or deleted with your account after inactivity), so nothing was submitted; reload the PR and check what is pending before submitting again';
const SERVER_IDENTITY_REFUSED =
    "refused: this server posts to GitHub as its own account, so it does not submit reviews or merge on users' behalf; an administrator can enable AllowServerIdentityActions";
const LINK_REQUIRED =
    'refused: link-github: link your GitHub account to submit reviews as yourself';
const LINK_EXPIRED =
    'refused: link-github: your GitHub authorization has expired or was revoked; link your account again';
const LINK_UNREADABLE =
    'refused: link-github: this server can no longer read your stored GitHub authorization; link your account again';

// What the link RPCs say when they cannot link at all.
const LOCAL_CANNOT_LINK =
    "This is the local connection: it writes to GitHub with the server's own token and links no account";
const ADMIN_CANNOT_LINK =
    "This is the administrator token's session, the server's own identity, which writes to GitHub with the server's token; GitHub accounts are linked by registered users";
const LINK_UNAVAILABLE =
    'This server has no GitHubOAuthClientID configured, so GitHub accounts cannot be linked';

const VERIFICATION_URI = 'https://github.com/login/device';
// How long a code lasts (GitHub's default) and how long a failed link's
// outcome is reported for (linkResultTTL).
const LINK_CODE_TTL_MS = 15 * 60 * 1000;
const LINK_RESULT_TTL_MS = 10 * 60 * 1000;

/** The error the server's method policy answers `method` with, if it turns it down. */
function refusal(method: string, session: Session): string | null {
    if (session.local || method.startsWith('E2E.')) return null;
    const name = method.startsWith('RPCHandler.') ? method.slice('RPCHandler.'.length) : '';
    const access = METHOD_POLICY[name] ?? 'admin';
    if (access === 'public') return null;
    if (session.login === null) return notAuthenticated(method);
    if (access === 'admin' && !session.admin) return permissionDenied(method);
    return null;
}

/** SkipCache on GetPR / GetAdjacentPR is an administrator's (checked in the handler). */
function checkSkipCache(args: Record<string, any>, session: Session) {
    if (args.SkipCache && !session.local && !session.admin) throw new Error(SKIP_CACHE_NEEDS_ADMIN);
}

// The account the server's own token belongs to, which local writes and writes
// through the open identity gate go out as.
const SERVER_ACCOUNT = 'e2e-reviewer';

const userOf = (session: Session) =>
    session.login ? users.get(session.login.toLowerCase()) : undefined;

/** A stored link the server can open, which writes then go out as. */
const usableLink = (user: RemoteUser | undefined) =>
    user?.link && user.link.broken !== 'unreadable' ? user.link : undefined;

/**
 * The write resolver (writeClientFor): the GitHub login SubmitReview or
 * MergePR goes out as, or throws the refusal. Decided before anything is
 * written, so a refused submit keeps its drafts.
 */
function writeIdentity(action: 'SubmitReview' | 'MergePR', session: Session): string {
    if (session.local) return SERVER_ACCOUNT;
    const user = userOf(session);
    if (user?.link?.broken === 'unreadable') throw new Error(LINK_UNREADABLE);
    if (user?.link?.broken === 'revoked') {
        // GitHub answers the user's token with a 401: the link is cleared.
        user.link = undefined;
        throw new Error(LINK_EXPIRED);
    }
    if (user?.link) return user.link.githubLogin;
    if (state.identityActions) {
        if (action === 'MergePR' && !session.admin) throw new Error(MERGE_AS_SERVER_NEEDS_ADMIN);
        return SERVER_ACCOUNT;
    }
    if (session.login && state.linkAvailable) throw new Error(LINK_REQUIRED);
    throw new Error(SERVER_IDENTITY_REFUSED);
}

/** The reply flags of Authenticate, RegisterUser and GetMyWorkflows. */
function linkFields(login: string) {
    if (!login) return { github_linked: false, github_link_available: false };
    return {
        github_linked: !!usableLink(users.get(login.toLowerCase())),
        github_link_available: state.linkAvailable,
    };
}

function authenticate(args: Record<string, any>, peer: Peer) {
    const reply = {
        okay: false,
        known: false,
        login: '',
        is_admin: false,
        message: '',
        ...linkFields(''),
    };
    if (peer.session.local) {
        return {
            ...reply,
            known: true,
            message:
                "This is the local connection: it acts as the server's own identity and needs no token",
        };
    }
    const token = String(args.Token ?? '').trim();
    if (!token) {
        return { ...reply, message: 'No token given; register with RegisterUser to get one' };
    }
    if (ADMIN_TOKEN && token === ADMIN_TOKEN) {
        peer.session = { login: '', admin: true, local: false };
        return {
            ...reply,
            okay: true,
            known: true,
            is_admin: true,
            message: "Authenticated as the server's administrator",
        };
    }
    const user = [...users.values()].find(u => u.token === token);
    if (!user) {
        return {
            ...reply,
            message:
                'No user holds this token: it was never registered here, or its user was deleted after a stretch of inactivity; register again with RegisterUser',
        };
    }
    peer.session = { login: user.login, admin: user.admin, local: false };
    return {
        okay: true,
        known: true,
        login: user.login,
        is_admin: user.admin,
        message: `Authenticated as ${user.login}`,
        ...linkFields(user.login),
    };
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

function registerUser(args: Record<string, any>, peer: Peer) {
    const refuse = (message: string, errors: unknown[] = []) => ({
        okay: false,
        created: false,
        login: '',
        token: '',
        is_admin: false,
        message,
        errors,
        ...linkFields(''),
    });
    if (peer.session.local) {
        return refuse(
            "This is the local connection: it acts as the server's own identity, and its workflows are the config file's"
        );
    }
    if (!REGISTRATION_KEY) {
        return refuse('Registration is disabled on this server: CRS_REGISTRATION_KEY is not set');
    }
    if (args.RegistrationKey !== REGISTRATION_KEY) return refuse('Wrong registration key');
    const login = String(args.Login ?? '').trim();
    if (!LOGIN_PATTERN.test(login)) {
        return refuse(
            `${JSON.stringify(login)} is not a GitHub login: letters, digits and single hyphens, at most 39 characters`
        );
    }

    const token = String(args.Token ?? '').trim();
    const known = users.get(login.toLowerCase());
    if (known && known.token !== token) {
        return refuse(
            `login already registered: ${known.login} is registered with another token; register with that token, or ask an administrator to delete the user`
        );
    }

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
    if (errors.length > 0) {
        return refuse(
            `Not registered: found ${plural(errors.length, 'problem', 'problems')}`,
            errors
        );
    }

    const adminTokenMatches = !!ADMIN_TOKEN && args.AdminToken === ADMIN_TOKEN;
    const reused = TOKEN_PATTERN.test(token) && ![...users.values()].some(u => u.token === token);
    const user: RemoteUser = {
        login: known?.login ?? login,
        token: known?.token ?? (reused ? token : `crs_${randomBytes(32).toString('hex')}`),
        // An administrator stays one whether or not this registration says so.
        admin: (known?.admin ?? false) || adminTokenMatches,
        workflows: Array.isArray(workflows) ? workflows : [],
        repos: Array.isArray(args.Repos) ? args.Repos : [],
        sectionPriority: args.SectionPriority ?? {},
        sectionSorting: args.SectionSorting ?? {},
        link: known?.link,
        // A new user's dashboard waits for the next cycle; an update keeps
        // the one there is.
        dashboard: known?.dashboard ?? false,
    };
    users.set(login.toLowerCase(), user);
    peer.session = { login: user.login, admin: user.admin, local: false };
    let message = `${known ? 'Updated the registration of' : 'Registered'} ${user.login} with ${plural(
        user.workflows.length,
        'workflow',
        'workflows'
    )}; the dashboard follows on the next workflow cycle`;
    if (args.AdminToken && !adminTokenMatches) {
        message += '; AdminToken did not match, so not as an administrator';
    }
    return {
        okay: true,
        created: !known,
        login: user.login,
        token: user.token,
        is_admin: user.admin,
        message,
        errors: [],
        ...linkFields(user.login),
    };
}

/** GetMyWorkflows: the connection's user's stored workflows and settings. */
function myWorkflows(session: Session) {
    const user = userOf(session);
    return {
        okay: !!user,
        known: !!user,
        login: user?.login ?? session.login ?? '',
        workflows: user?.workflows ?? [],
        repos: user?.repos ?? [],
        section_priority: user?.sectionPriority ?? {},
        section_sorting: user?.sectionSorting ?? {},
        workflow_types: [],
        filters: [],
        ...linkFields(user?.login ?? ''),
    };
}

// --- Linking a GitHub account (listen mode) ----------------------------------

/** Why this connection cannot link an account, or null when it can try. */
function linkRefusal(session: Session): string | null {
    if (session.local) return LOCAL_CANNOT_LINK;
    if (!session.login) return ADMIN_CANNOT_LINK;
    return null;
}

/**
 * The login's flow: one in progress, or one that ended without a link in the
 * last ten minutes. A pending flow whose code has expired ends as GitHub
 * would end it.
 */
function currentFlow(login: string): LinkFlow | undefined {
    const key = login.toLowerCase();
    const flow = state.linkFlows.get(key);
    if (!flow) return undefined;
    const now = Date.now();
    if (flow.status === 'pending' && now > flow.expiresAt.getTime()) {
        endFlow(
            flow,
            'expired',
            'The code expired before it was entered on GitHub; start the link again'
        );
    }
    if (flow.status !== 'pending' && now - flow.endedAt > LINK_RESULT_TTL_MS) {
        state.linkFlows.delete(key);
        return undefined;
    }
    return flow;
}

function endFlow(flow: LinkFlow, status: LinkFlow['status'], message: string) {
    flow.status = status;
    flow.message = message;
    flow.endedAt = Date.now();
}

/** A user code the way GitHub prints them: eight letters, `WDJB-MJHT`. */
function userCode() {
    const letters = 'BCDFGHJKLMNPQRSTVWXZ';
    const pick = () => letters[randomBytes(1)[0] % letters.length];
    return Array.from({ length: 9 }, (_, i) => (i === 4 ? '-' : pick())).join('');
}

const rfc3339 = (d: Date) => d.toISOString().replace(/\.\d{3}Z$/, 'Z');

/** Ends `login`'s pending flow as GitHub would (E2E.EndGitHubLink). */
function endGitHubLink(args: Record<string, any>) {
    const login = String(args.Login ?? '');
    const user = users.get(login.toLowerCase());
    const flow = currentFlow(login);
    if (!user || flow?.status !== 'pending') throw new Error(`${login} has no GitHub link pending`);
    switch (args.Outcome) {
        case 'linked':
            user.link = {
                githubLogin: args.GitHubLogin || user.login,
                linkedAt: rfc3339(new Date()),
            };
            state.linkFlows.delete(login.toLowerCase());
            break;
        case 'denied':
            endFlow(
                flow,
                'denied',
                'The authorization was declined on GitHub, so nothing was linked'
            );
            break;
        case 'expired':
            endFlow(
                flow,
                'expired',
                'The code expired before it was entered on GitHub; start the link again'
            );
            break;
        case 'mismatch': {
            const other = args.GitHubLogin || 'someone-else';
            endFlow(
                flow,
                'mismatch',
                `GitHub says that authorization is ${other}'s, but this connection is registered as ${user.login}, so nothing was stored; sign in to GitHub as ${user.login} and link again`
            );
            break;
        }
        case 'error':
            endFlow(
                flow,
                'error',
                `GitHub did not complete the link: ${args.Message || 'incorrect_client_credentials'}`
            );
            break;
        default:
            throw new Error(`unknown outcome ${JSON.stringify(args.Outcome)}`);
    }
    return { okay: true };
}

const REVIEW_STATE: Record<string, string> = {
    APPROVE: 'APPROVED',
    REQUEST_CHANGES: 'CHANGES_REQUESTED',
    COMMENT: 'COMMENTED',
};

// --- Images -------------------------------------------------------------------

// The images the fake has "downloaded", by URL: one PNG, two pixels wide.
const IMAGES: Record<string, { contentType: string; base64: string }> = {
    'https://github.com/user-attachments/assets/e2e-screenshot': {
        contentType: 'image/png',
        base64: 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAABCAYAAAD0In+KAAAAEUlEQVR4nGNQTX79/2ex2H8AFMsE9NJRZ9EAAAAASUVORK5CYII=',
    },
};

/**
 * GetImage. Locally the cached file is real (in the bridge's state
 * directory), so the bridge can serve it from disk; on a listening fake the
 * path is the "server's", which does not exist where the bridge runs, so a
 * remote bridge has to ask for the bytes (IncludeData).
 */
function getImage(args: Record<string, any>, session: Session) {
    const url = String(args.URL ?? '');
    const image = IMAGES[url];
    if (!image) {
        return {
            okay: false,
            url,
            path: '',
            content_type: '',
            size: 0,
            cached: false,
            error: 'not cached',
        };
    }
    const bytes = Buffer.from(image.base64, 'base64');
    const name = `${createHash('sha256').update(url).digest('hex')}.png`;
    let path = join('/nonexistent/crs-server-host/images', name);
    if (session.local) {
        const dir = join(process.env.CRS_E2E_STATE_DIR || tmpdir(), 'images');
        mkdirSync(dir, { recursive: true });
        path = join(dir, name);
        writeFileSync(path, bytes);
    }
    return {
        okay: true,
        url,
        path,
        content_type: image.contentType,
        size: bytes.length,
        cached: true,
        error: '',
        ...(args.IncludeData ? { data: image.base64 } : {}),
    };
}

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

    'RPCHandler.GetMyWorkflows': (_args, peer) => myWorkflows(peer.session),

    // The server's own dashboard (stdio, the admin token) is the fixtures'; a
    // user's is too once a cycle has built it, and empty until then.
    'RPCHandler.GetAllReviews': (_args, peer) => ({
        okay: true,
        content: '',
        // Counted like the server does, from the PR's GitHub comments: local
        // drafts don't count until they are submitted.
        items:
            peer.session.login && !userOf(peer.session)?.dashboard
                ? []
                : state.prs.map(p => ({
                      ...p.item,
                      review_ease: reviewEase(p),
                      comment_count: p.comments.length,
                  })),
    }),

    'RPCHandler.GetPR': (args, peer) => {
        checkSkipCache(args, peer.session);
        return payload(findPR(args), peer.session);
    },

    'RPCHandler.SyncPR': (args, peer) => ({
        ...payload(findPR(args), peer.session),
        updated: state.syncUpdated,
    }),

    'RPCHandler.GetAdjacentPR': (args, peer) => {
        checkSkipCache(args, peer.session);
        const items = state.prs.map(p => p.item);
        const idx = items.findIndex(
            i => i.owner === args.Owner && i.repo === args.Repo && i.number === args.Number
        );
        const step = args.Previous ? -1 : 1;
        const next = items[(idx + step + items.length) % items.length];
        return {
            ...payload(
                findPR({ Owner: next.owner, Repo: next.repo, Number: next.number }),
                peer.session
            ),
            adjacent_owner: next.owner,
            adjacent_repo: next.repo,
            adjacent_number: next.number,
        };
    },

    'RPCHandler.AddComment': (args, peer) => {
        const pr = findPR(args);
        const id = state.nextCommentId++;
        locals(pr, peer.session).push(
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
        return { ...payload(pr, peer.session), id };
    },

    'RPCHandler.EditComment': (args, peer) => {
        const pr = findPR(args);
        const c = locals(pr, peer.session).find(l => l.id === String(args.ID));
        if (!c) throw new Error(`local comment ${args.ID} not found`);
        c.body = args.Body;
        return payload(pr, peer.session);
    },

    'RPCHandler.DeleteComment': (args, peer) => {
        const pr = findPR(args);
        const list = locals(pr, peer.session);
        const idx = list.findIndex(l => l.id === String(args.ID));
        if (idx === -1) throw new Error(`local comment ${args.ID} not found`);
        list.splice(idx, 1);
        return payload(pr, peer.session);
    },

    'RPCHandler.SubmitReview': (args, peer) => {
        const pr = findPR(args);
        // Settled before anything is written: a refusal leaves the drafts.
        const author = writeIdentity('SubmitReview', peer.session);
        // The drafts the client previewed must all still be there.
        const pending = new Set(locals(pr, peer.session).map(c => c.id));
        for (const id of args.ExpectedDraftIDs ?? []) {
            if (!pending.has(String(id))) throw new Error(DRAFTS_CHANGED);
        }
        const review: ReviewJSON = {
            id: state.nextReviewId++,
            user: author,
            body: args.Body ?? '',
            state: REVIEW_STATE[args.Event] ?? 'COMMENTED',
            submitted_at: new Date().toISOString(),
            html_url: `${pr.item.url}#pullrequestreview-${state.nextReviewId}`,
            reactions: [],
        };
        pr.reviews.push(review);
        // Pending local comments go out with the review and come back as
        // GitHub comments authored by the reviewer.
        for (const c of locals(pr, peer.session).splice(0)) {
            pr.comments.push({ ...c, author, review_id: review.id });
        }
        return { ...payload(pr, peer.session), failed_replies: [] };
    },

    // GitHub's answer passed through. No UI merges yet; this is here so the
    // write resolver's merge rules can be driven through the bridge.
    'RPCHandler.MergePR': (args, peer) => {
        findPR(args);
        writeIdentity('MergePR', peer.session);
        return {
            merged: true,
            sha: '6dcb09b5b57875f334f61aebed695e2e4193db5e',
            message: 'Pull Request successfully merged',
        };
    },

    'RPCHandler.SetFeedback': (args, peer) => {
        state.feedback.set(ownKey(peer.session, findPR(args)), args.Body);
        return { okay: true };
    },

    'RPCHandler.RemovePRComments': (args, peer) => {
        locals(findPR(args), peer.session).splice(0);
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

    'RPCHandler.GetPluginOutput': args => ({ output: pluginOutput(findPR(args)) }),

    'RPCHandler.RerunPlugins': args => {
        const pr = findPR(args);
        const prefix = `${key(pr.item.owner, pr.item.repo, pr.item.number)}:`;
        for (const name of args.Plugins ?? []) {
            delete pr.plugins[name];
            state.pluginRuns.set(prefix + name, 2);
        }
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

    'RPCHandler.GetImage': (args, peer) => getImage(args, peer.session),

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

    // --- Linking a GitHub account (listen mode) ---------------------------------

    'RPCHandler.StartGitHubLink': (_args, peer) => {
        const reply = {
            okay: false,
            status: 'unavailable',
            user_code: '',
            verification_uri: '',
            expires_at: '',
            interval: 0,
            message: '',
        };
        const cannot = linkRefusal(peer.session);
        if (cannot) return { ...reply, message: cannot };
        if (!state.linkAvailable) return { ...reply, message: LINK_UNAVAILABLE };
        const login = peer.session.login!;
        let flow = currentFlow(login);
        if (flow?.status !== 'pending') {
            flow = {
                status: 'pending',
                message: '',
                userCode: userCode(),
                verificationUri: VERIFICATION_URI,
                expiresAt: new Date(Date.now() + LINK_CODE_TTL_MS),
                interval: state.linkInterval,
                endedAt: 0,
            };
            state.linkFlows.set(login.toLowerCase(), flow);
        }
        const expiresAt = rfc3339(flow.expiresAt);
        return {
            okay: true,
            status: 'pending',
            user_code: flow.userCode,
            verification_uri: flow.verificationUri,
            expires_at: expiresAt,
            interval: flow.interval,
            message: `Enter ${flow.userCode} at ${flow.verificationUri} to link your GitHub account (signed in to GitHub as ${login}); the code expires at ${expiresAt}`,
        };
    },

    'RPCHandler.GetGitHubLink': (_args, peer) => {
        const reply = {
            okay: false,
            status: 'unavailable',
            github_login: '',
            user_code: '',
            verification_uri: '',
            expires_at: '',
            message: '',
        };
        const cannot = linkRefusal(peer.session);
        if (cannot) return { ...reply, message: cannot };
        const login = peer.session.login!;
        const user = users.get(login.toLowerCase());
        const flow = currentFlow(login);
        const link = usableLink(user);
        if (link) reply.github_login = link.githubLogin;
        reply.okay = true;
        if (flow) {
            reply.status = flow.status;
            reply.message = flow.message;
            if (flow.status === 'pending') {
                reply.user_code = flow.userCode;
                reply.verification_uri = flow.verificationUri;
                reply.expires_at = rfc3339(flow.expiresAt);
                reply.message = `Waiting for ${flow.userCode} to be entered at ${flow.verificationUri}`;
            }
            if (link) {
                reply.message += `; your current link, to ${link.githubLogin}, stays until a new one completes`;
            }
            return reply;
        }
        if (link) {
            reply.status = 'linked';
            reply.message = `Linked to GitHub as ${link.githubLogin} since ${link.linkedAt}: your reviews and merges go out as you`;
        } else if (!state.linkAvailable) {
            reply.okay = false;
            reply.message = LINK_UNAVAILABLE;
        } else if (user?.link?.broken === 'unreadable') {
            reply.status = 'error';
            reply.message =
                'Your stored GitHub link can no longer be read by this server (its CRS_SECRET_KEY changed); link your account again';
        } else {
            reply.status = 'unlinked';
            reply.message = 'No GitHub account is linked; StartGitHubLink links yours';
        }
        return reply;
    },

    'RPCHandler.UnlinkGitHub': (_args, peer) => {
        const cannot = linkRefusal(peer.session);
        if (cannot) return { okay: false, message: cannot };
        const key = peer.session.login!.toLowerCase();
        const cancelled = state.linkFlows.get(key)?.status === 'pending';
        state.linkFlows.delete(key);
        const user = users.get(key);
        const had = !!user?.link;
        if (user) user.link = undefined;
        let message = 'No GitHub account was linked';
        if (had) {
            message =
                'Unlinked your GitHub account: your reviews and merges no longer go out as you; its token was revoked on GitHub';
        } else if (cancelled) {
            message = 'Cancelled the GitHub link in progress; no account is linked';
        }
        return { okay: true, message };
    },

    // --- Test control surface -------------------------------------------------

    'E2E.Reset': () => {
        state = freshState();
        // Users stay registered (the bridge registered once), but every test
        // starts with nobody linked, before the first cycle.
        for (const user of users.values()) {
            user.link = undefined;
            user.dashboard = false;
        }
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

    // Listen mode: carry out the next call to `method`, then hang up on the
    // connection it came on instead of answering — the server finished what
    // the client never heard back about.
    'E2E.DropNext': args => {
        state.dropNext.add(args.method);
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

    // A workflow cycle, as far as dashboards go: it builds every registered
    // user's from the fixtures.
    'E2E.RunCycle': () => {
        for (const user of users.values()) user.dashboard = true;
        return { okay: true, users: users.size };
    },

    // Forget a registered user, as the server's retention sweep does. With
    // `Disconnect` their connections are closed too, as by a server restart,
    // and the bridge reconnects; without, they stay open but lose their
    // session, so the next call is refused as not authenticated.
    'E2E.ForgetUser': args => {
        const login = String(args.Login ?? '').toLowerCase();
        const forgotten = users.delete(login);
        // Deleting a user cancels a link in progress; their link goes with
        // them, and so do their drafts, feedback and dashboard.
        state.linkFlows.delete(login);
        for (const owned of [state.localComments, state.feedback]) {
            for (const k of owned.keys()) if (k.startsWith(`${login}\0`)) owned.delete(k);
        }
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

    // Let the server link GitHub accounts, as GitHubOAuthClientID and
    // CRS_SECRET_KEY do; off by default. `interval` is the seconds between
    // polls StartGitHubLink asks for (1 unless set; GitHub's is 5).
    'E2E.SetGitHubLinkAvailable': args => {
        state.linkAvailable = !!args.available;
        if (typeof args.interval === 'number') state.linkInterval = args.interval;
        return { okay: true };
    },

    // End a user's pending link as GitHub would. `Outcome` is `linked` (as
    // `GitHubLogin`, the user's own login unless set), `denied`, `expired`,
    // `mismatch` (GitHub's login is `GitHubLogin`) or `error` (`Message`).
    'E2E.EndGitHubLink': args => endGitHubLink(args),

    // Store a link for a user without a device flow.
    'E2E.LinkGitHub': args => {
        const user = users.get(String(args.Login ?? '').toLowerCase());
        if (!user) throw new Error(`no user ${args.Login}`);
        user.link = { githubLogin: args.GitHubLogin || user.login, linkedAt: rfc3339(new Date()) };
        return { okay: true };
    },

    // Break a user's stored link: `revoked` on github.com (the next write
    // finds out and clears it), or `unreadable` (CRS_SECRET_KEY changed).
    'E2E.BreakGitHubLink': args => {
        const user = users.get(String(args.Login ?? '').toLowerCase());
        if (!user?.link) throw new Error(`${args.Login} has no GitHub link`);
        user.link.broken = args.How === 'unreadable' ? 'unreadable' : 'revoked';
        return { okay: true };
    },

    // A user's link as the fake holds it: the flow, and the stored link.
    'E2E.GitHubLink': args => {
        const login = String(args.Login ?? '');
        const flow = currentFlow(login);
        return {
            flow: flow
                ? { status: flow.status, user_code: flow.userCode, message: flow.message }
                : null,
            link: users.get(login.toLowerCase())?.link ?? null,
        };
    },

    // Replace a PR's description, e.g. to embed an image.
    'E2E.SetPRBody': args => {
        findPR(args).body = String(args.Body ?? '');
        return { okay: true };
    },
};

function respond(peer: Peer, id: Request['id'], result: unknown, error: string | null) {
    peer.send(`${JSON.stringify({ id, result: error ? null : result, error })}\n`);
}

// The prefixes of the server's refusals, which a call's record notes.
const REFUSAL_PREFIX = /^(not authenticated|permission denied|refused):/;

function handle(req: Request, peer: Peer) {
    const args = req.params?.[0] ?? {};
    const refused = refusal(req.method, peer.session);
    const call: State['calls'][number] = { method: req.method, params: args };
    if (refused) call.refused = refused;
    if (!req.method.startsWith('E2E.')) state.calls.push(call);
    if (refused) {
        respond(peer, req.id, null, refused);
        return;
    }
    if (state.holdNext.delete(req.method)) {
        state.held.set(req.method, () => answer(req, args, peer, call));
        return;
    }
    answer(req, args, peer, call);
}

function answer(req: Request, args: Record<string, any>, peer: Peer, call: State['calls'][number]) {
    if (!peer.session.local && state.dropNext.delete(req.method)) {
        try {
            handlers[req.method]?.(args, peer);
        } catch {
            // Nobody hears how it went either way.
        }
        peer.close();
        return;
    }
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
        const message = e instanceof Error ? e.message : String(e);
        // A refusal a handler gave (SkipCache, the write resolver).
        if (REFUSAL_PREFIX.test(message)) call.refused = message;
        respond(peer, req.id, null, message);
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
