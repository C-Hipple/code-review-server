// What the server pushes to the page. The bridge relays the server's JSON-RPC
// notifications over one Server-Sent Events stream, `GET /api/events` (see
// server_events.ts beside server.ts), each event named after the
// notification's method with its params as the data. `serverEvents` shares
// that one stream among every listener on the page: a browser allows only a
// handful of open connections per host, and each EventSource holds one.

import { API_BASE } from './api';

/** Sent every `interval_seconds` while the server is up. */
export const HEARTBEAT_EVENT = 'Server.Heartbeat';

export interface Heartbeat {
    /** Heartbeats sent since the server started. */
    seq: number;
    /** The server's clock when it was sent (RFC 3339). */
    time: string;
    /** When the server started (RFC 3339); a new value is a new server. */
    started_at: string;
    /** How long until the next one. */
    interval_seconds: number;
}

export function parseHeartbeat(data: unknown): Heartbeat | null {
    if (typeof data !== 'object' || data === null) return null;
    const { seq, time, started_at, interval_seconds } = data as Record<string, unknown>;
    if (
        typeof seq !== 'number' ||
        typeof time !== 'string' ||
        typeof started_at !== 'string' ||
        typeof interval_seconds !== 'number'
    ) {
        return null;
    }
    return { seq, time, started_at, interval_seconds };
}

/**
 * Sent whenever a PR's stored plugin output changes: a plugin marked pending,
 * a result landing, a re-run clearing one. It names what changed, not the
 * output; a page showing the PR reads GetPluginOutput again.
 */
export const PLUGIN_OUTPUT_CHANGED_EVENT = 'Server.PluginOutputChanged';

export interface PluginOutputChanged {
    owner: string;
    repo: string;
    number: number;
    /** The plugin whose result changed; empty when a re-run cleared them all. */
    plugin: string;
    /** Its status now (`pending`, `success`, ...); empty when it was cleared for a re-run. */
    status: string;
}

/**
 * Sent when an AI feature's output for a PR changes: as a run starts
 * (`pending`) and as its result is stored. Like a plugin change it names what
 * changed; a page showing the feature reads GetAIOutput again.
 */
export const AI_OUTPUT_CHANGED_EVENT = 'Server.AIOutputChanged';

export interface AIOutputChanged {
    owner: string;
    repo: string;
    number: number;
    feature: string;
    /** `pending` as a run starts, then the stored result's status. */
    status: string;
}

/** A pull request, as the server's change events name one. */
export interface PRRef {
    owner: string;
    repo: string;
    number: number;
}

function parsePRRef(data: unknown): (PRRef & Record<string, unknown>) | null {
    if (typeof data !== 'object' || data === null) return null;
    const fields = data as Record<string, unknown>;
    const { owner, repo, number } = fields;
    if (typeof owner !== 'string' || typeof repo !== 'string' || typeof number !== 'number') {
        return null;
    }
    return { ...fields, owner, repo, number };
}

const text = (value: unknown) => (typeof value === 'string' ? value : '');

export function parsePluginOutputChanged(data: unknown): PluginOutputChanged | null {
    const pr = parsePRRef(data);
    if (!pr) return null;
    const { owner, repo, number } = pr;
    return { owner, repo, number, plugin: text(pr.plugin), status: text(pr.status) };
}

export function parseAIOutputChanged(data: unknown): AIOutputChanged | null {
    const pr = parsePRRef(data);
    if (!pr || typeof pr.feature !== 'string') return null;
    const { owner, repo, number } = pr;
    return { owner, repo, number, feature: pr.feature, status: text(pr.status) };
}

/** Whether `change` is about `pr`. GitHub names ignore case. */
export function isChangeFor(change: PRRef, pr: PRRef): boolean {
    return (
        change.number === pr.number &&
        change.owner.toLowerCase() === pr.owner.toLowerCase() &&
        change.repo.toLowerCase() === pr.repo.toLowerCase()
    );
}

/** Heartbeats that can go missing before the server is taken for gone. */
export const MISSED_HEARTBEATS = 3;

/** How long after `heartbeat` the server is taken for gone if nothing follows it. */
export function heartbeatDeadlineMs(heartbeat: Heartbeat): number {
    return Math.max(heartbeat.interval_seconds, 1) * 1000 * MISSED_HEARTBEATS;
}

/**
 * `connecting` until the first heartbeat, `live` while they keep coming, and
 * `lost` once they stop or the stream drops. A server that never sends one (a
 * crs from before heartbeats) stays `connecting`, which shows nothing.
 */
export type ServerConnection = 'connecting' | 'live' | 'lost';

export interface HeartbeatState {
    connection: ServerConnection;
    /** The latest heartbeat heard. */
    heartbeat: Heartbeat | null;
}

export const INITIAL_HEARTBEAT_STATE: HeartbeatState = {
    connection: 'connecting',
    heartbeat: null,
};

export type HeartbeatAction =
    | { type: 'beat'; heartbeat: Heartbeat }
    /** A heartbeat is overdue, or the stream dropped. */
    | { type: 'silent' };

export function heartbeatReducer(state: HeartbeatState, action: HeartbeatAction): HeartbeatState {
    switch (action.type) {
        case 'beat':
            return { connection: 'live', heartbeat: action.heartbeat };
        case 'silent':
            return state.connection === 'live' ? { ...state, connection: 'lost' } : state;
    }
}

/**
 * Sent when a GitHub webhook delivery changed what the server holds for a PR:
 * a page showing it fetches it again (GetPR, GetAllReviews) to pick the change
 * up, and says what happened.
 */
export const PR_UPDATED_EVENT = 'Server.PRUpdated';

/** `changes` when the PR's head or base moved: its diff may be new. */
export const CHANGE_HEAD = 'head';
/** `changes` when only the review list's rows for the PR changed. */
export const CHANGE_SECTIONS = 'sections';

export interface PRUpdate {
    owner: string;
    /** The short repository name, as GetPR takes it. */
    repo: string;
    number: number;
    /** The delivery's event and action ("pull_request_review.submitted"), or "sections". */
    event: string;
    /** What was rewritten: "head", "pr", "reviews", "comments", "ci_status", "sections", ... */
    changes: string[];
    head_sha?: string;
    /** Who did it; absent for CI. */
    actor?: string;
    /** The change is the configured user's own. */
    own: boolean;
    /** What happened in a few words ("alice approved"); absent when not worth announcing. */
    summary?: string;
}

export function parsePRUpdate(data: unknown): PRUpdate | null {
    if (typeof data !== 'object' || data === null) return null;
    const { owner, repo, number, event, changes, head_sha, actor, own, summary } = data as Record<
        string,
        unknown
    >;
    if (
        typeof owner !== 'string' ||
        typeof repo !== 'string' ||
        typeof number !== 'number' ||
        !Array.isArray(changes)
    ) {
        return null;
    }
    return {
        owner,
        repo,
        number,
        event: typeof event === 'string' ? event : '',
        changes: changes.filter((c): c is string => typeof c === 'string'),
        head_sha: typeof head_sha === 'string' ? head_sha : undefined,
        actor: typeof actor === 'string' ? actor : undefined,
        own: own === true,
        summary: typeof summary === 'string' && summary !== '' ? summary : undefined,
    };
}

/**
 * Whether `update` is about the PR owner/repo#number. GitHub and the config
 * can spell an owner or repository in different cases.
 */
export function isUpdateFor(update: PRUpdate, owner: string, repo: string, number: number) {
    return (
        update.number === number &&
        update.owner.toLowerCase() === owner.toLowerCase() &&
        update.repo.toLowerCase() === repo.toLowerCase()
    );
}

/** Whether `update` changed the PR itself, not just the review list's rows for it. */
export function changesPR(update: PRUpdate): boolean {
    return update.changes.some(change => change !== CHANGE_SECTIONS);
}

/** Lines a toast shows before it sums the rest up. */
const SHOWN_UPDATES = 3;

/**
 * What a toast says about a batch of updates: one line per announced change,
 * the newest few, each naming its PR when `withPR`. Null when none is worth
 * announcing — the rest only refresh the page. Changes the user made
 * themselves aren't announced: they know.
 */
export function prUpdateMessage(updates: PRUpdate[], withPR = false): string | null {
    const lines: string[] = [];
    for (const update of updates) {
        if (!update.summary || update.own) continue;
        const line = withPR ? `${update.repo}#${update.number}: ${update.summary}` : update.summary;
        // A burst can say the same thing twice (a check re-run that passes again).
        if (lines[lines.length - 1] !== line) lines.push(line);
    }
    if (lines.length === 0) return null;
    const shown = lines.slice(-SHOWN_UPDATES);
    const hidden = lines.length - shown.length;
    return hidden > 0 ? `${shown.join('\n')}\n…and ${hidden} more` : shown.join('\n');
}

/** How long a batch of updates collects before the page acts on it. */
export const PR_UPDATE_BATCH_MS = 400;

export interface Timers {
    set: (fn: () => void, ms: number) => unknown;
    clear: (handle: unknown) => void;
}

const realTimers: Timers = {
    set: (fn, ms) => setTimeout(fn, ms),
    clear: handle => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/**
 * Calls `flush` with each batch of the PR updates `accept` lets through. A
 * delivery seldom comes alone — a push sets off one per check, a review one
 * per comment, then the section change — so the first update opens a batch,
 * and whatever arrives in the next `delayMs` joins it rather than refreshing
 * the page again. The batch never waits longer than that, however busy it
 * gets. Returns the unsubscribe.
 */
export function onPRUpdates(
    events: ServerEvents,
    accept: (update: PRUpdate) => boolean,
    flush: (updates: PRUpdate[]) => void,
    delayMs: number = PR_UPDATE_BATCH_MS,
    timers: Timers = realTimers
): () => void {
    let batch: PRUpdate[] = [];
    let timer: unknown = null;
    const off = events.on(PR_UPDATED_EVENT, data => {
        const update = parsePRUpdate(data);
        if (!update || !accept(update)) return;
        batch.push(update);
        if (timer !== null) return;
        timer = timers.set(() => {
            const updates = batch;
            batch = [];
            timer = null;
            flush(updates);
        }, delayMs);
    });
    return () => {
        off();
        if (timer !== null) timers.clear(timer);
        timer = null;
        batch = [];
    };
}

type MessageListener = (event: MessageEvent<string>) => void;

/** The parts of EventSource ServerEvents uses, so tests can stand in for it. */
export interface EventStream {
    addEventListener(type: string, listener: MessageListener): void;
    removeEventListener(type: string, listener: MessageListener): void;
    onopen: ((event: Event) => void) | null;
    onerror: ((event: Event) => void) | null;
    close(): void;
}

type Listener = (data: unknown) => void;

/**
 * One event stream shared by every listener: opened with the first, closed
 * with the last. EventSource reconnects by itself when the stream drops.
 */
export class ServerEvents {
    private source: EventStream | null = null;
    private readonly listeners = new Map<string, Set<Listener>>();
    private readonly dispatchers = new Map<string, MessageListener>();
    private readonly openListeners = new Set<() => void>();
    private readonly errorListeners = new Set<() => void>();

    constructor(private readonly open: () => EventStream | null) {}

    /** Calls `listener` with each `event`'s data, parsed. Returns the unsubscribe. */
    on(event: string, listener: Listener): () => void {
        let listeners = this.listeners.get(event);
        if (!listeners) {
            listeners = new Set();
            this.listeners.set(event, listeners);
            this.source?.addEventListener(event, this.dispatcher(event));
        }
        listeners.add(listener);
        this.connect();

        return () => {
            if (!listeners.delete(listener) || listeners.size > 0) return;
            this.listeners.delete(event);
            this.source?.removeEventListener(event, this.dispatcher(event));
            if (this.listeners.size === 0) this.disconnect();
        };
    }

    /**
     * Calls `listener` each time the stream connects: at first, and again
     * whenever it comes back after dropping. Events sent while it was down are
     * never delivered, so this is when to catch up. Returns the unsubscribe.
     */
    onOpen(listener: () => void): () => void {
        this.openListeners.add(listener);
        return () => this.openListeners.delete(listener);
    }

    /** Calls `listener` whenever the stream drops. Returns the unsubscribe. */
    onError(listener: () => void): () => void {
        this.errorListeners.add(listener);
        return () => this.errorListeners.delete(listener);
    }

    private connect() {
        if (this.source) return;
        const source = this.open();
        if (!source) return;
        this.source = source;
        source.onopen = () => {
            for (const listener of [...this.openListeners]) listener();
        };
        source.onerror = () => {
            for (const listener of [...this.errorListeners]) listener();
        };
        for (const event of this.listeners.keys()) {
            source.addEventListener(event, this.dispatcher(event));
        }
    }

    private disconnect() {
        this.source?.close();
        this.source = null;
    }

    private dispatcher(event: string): MessageListener {
        let dispatch = this.dispatchers.get(event);
        if (!dispatch) {
            dispatch = message => {
                let data: unknown;
                try {
                    data = JSON.parse(message.data);
                } catch {
                    console.error(`Ignoring a ${event} event whose data is not JSON`);
                    return;
                }
                for (const listener of [...(this.listeners.get(event) ?? [])]) listener(data);
            };
            this.dispatchers.set(event, dispatch);
        }
        return dispatch;
    }
}

export const serverEvents = new ServerEvents(() =>
    typeof EventSource === 'undefined' ? null : new EventSource(`${API_BASE}/api/events`)
);
