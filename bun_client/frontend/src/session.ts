import { getBridgeSession, rpcErrorMessage, type BridgeSession } from './api';

/**
 * The bridge's session (GET /api/session), shared by everything that shows
 * who the user is on a shared server — the account menu, the submit preview,
 * the GitHub link dialog — and refreshed by whatever changes it: a link made
 * or removed, or a write refused because the link stopped working.
 *
 * Reads are applied in the order they were started, so a slow read begun
 * before a link completed can never overwrite the one made after it.
 */
export class SessionStore {
    private current: BridgeSession | null = null;
    private started = 0;
    private applied = 0;
    private listeners = new Set<() => void>();

    constructor(private load: () => Promise<BridgeSession>) {}

    /** The session as last read; null until the first read lands. */
    get = (): BridgeSession | null => this.current;

    subscribe = (listener: () => void): (() => void) => {
        this.listeners.add(listener);
        return () => {
            this.listeners.delete(listener);
        };
    };

    /**
     * Reads the session again. A failed read keeps what was known, with the
     * reason; with nothing known yet it is taken for a local bridge, whose
     * features need no session.
     */
    refresh = async (): Promise<BridgeSession> => {
        const seq = ++this.started;
        let session: BridgeSession;
        try {
            session = await this.load();
        } catch (e) {
            session = { ...(this.current ?? { remote: false }), error: rpcErrorMessage(e) };
        }
        if (seq > this.applied) {
            this.applied = seq;
            this.current = session;
            for (const listener of this.listeners) listener();
        }
        return this.current ?? session;
    };

    /** Starts the first read, unless one has started already. */
    ensure = () => {
        if (this.started === 0) void this.refresh();
    };
}

/** The app's one session store, read from the bridge. */
export const bridgeSession = new SessionStore(getBridgeSession);
