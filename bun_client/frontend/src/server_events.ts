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
