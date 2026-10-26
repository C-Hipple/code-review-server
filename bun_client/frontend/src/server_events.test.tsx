import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import ServerStatus from './components/ServerStatus';
import {
    changesPR,
    HEARTBEAT_EVENT,
    heartbeatDeadlineMs,
    heartbeatReducer,
    INITIAL_HEARTBEAT_STATE,
    isChangeFor,
    onPRUpdates,
    onReconnect,
    parseAIOutputChanged,
    parseHeartbeat,
    parsePluginOutputChanged,
    parsePRUpdate,
    PLUGIN_OUTPUT_CHANGED_EVENT,
    PR_UPDATED_EVENT,
    prUpdateMessage,
    ServerEvents,
    type EventStream,
    type Heartbeat,
    type PRUpdate,
    type Timers,
} from './server_events';

const BEAT: Heartbeat = {
    seq: 7,
    time: '2026-10-25T12:00:10Z',
    started_at: '2026-10-25T11:00:00Z',
    interval_seconds: 10,
};

/** Stands in for EventSource: records listeners, delivers events on demand. */
class FakeStream implements EventStream {
    listeners = new Map<string, Set<(event: MessageEvent<string>) => void>>();
    onopen: ((event: Event) => void) | null = null;
    onerror: ((event: Event) => void) | null = null;
    closed = false;

    addEventListener(type: string, listener: (event: MessageEvent<string>) => void) {
        if (!this.listeners.has(type)) this.listeners.set(type, new Set());
        this.listeners.get(type)!.add(listener);
    }

    removeEventListener(type: string, listener: (event: MessageEvent<string>) => void) {
        this.listeners.get(type)?.delete(listener);
    }

    close() {
        this.closed = true;
    }

    emit(type: string, data: string) {
        for (const listener of this.listeners.get(type) ?? []) {
            listener({ data } as MessageEvent<string>);
        }
    }

    fail() {
        this.onerror?.(new Event('error'));
    }

    connect() {
        this.onopen?.(new Event('open'));
    }
}

function hub() {
    const opened: FakeStream[] = [];
    const events = new ServerEvents(() => {
        const stream = new FakeStream();
        opened.push(stream);
        return stream;
    });
    return { events, opened };
}

describe('parseHeartbeat', () => {
    test('reads a heartbeat', () => {
        expect(parseHeartbeat({ ...BEAT })).toEqual(BEAT);
    });

    test('rejects anything else', () => {
        expect(parseHeartbeat(null)).toBeNull();
        expect(parseHeartbeat('beat')).toBeNull();
        expect(parseHeartbeat({ ...BEAT, seq: '7' })).toBeNull();
        expect(parseHeartbeat({ seq: 1 })).toBeNull();
    });
});

describe('change events', () => {
    const pr = { owner: 'acme', repo: 'widgets', number: 42 };

    test('reads a plugin output change', () => {
        expect(parsePluginOutputChanged({ ...pr, plugin: 'summarize', status: 'success' })).toEqual(
            { ...pr, plugin: 'summarize', status: 'success' }
        );
        // A re-run of every plugin clears them all at once: no plugin, no status.
        expect(parsePluginOutputChanged({ ...pr, plugin: '', status: '' })).toEqual({
            ...pr,
            plugin: '',
            status: '',
        });
    });

    test('reads an AI output change', () => {
        expect(
            parseAIOutputChanged({ ...pr, feature: 'comments-addressed', status: 'pending' })
        ).toEqual({ ...pr, feature: 'comments-addressed', status: 'pending' });
        expect(parseAIOutputChanged({ ...pr, status: 'pending' })).toBeNull();
    });

    test('rejects anything that names no PR', () => {
        expect(parsePluginOutputChanged(null)).toBeNull();
        expect(parsePluginOutputChanged({ ...pr, number: '42' })).toBeNull();
        expect(parseAIOutputChanged({ owner: 'acme', number: 42, feature: 'x' })).toBeNull();
    });

    test('matches its PR whatever the case of its names', () => {
        expect(isChangeFor({ owner: 'Acme', repo: 'Widgets', number: 42 }, pr)).toBe(true);
        expect(isChangeFor({ ...pr, number: 43 }, pr)).toBe(false);
        expect(isChangeFor({ ...pr, repo: 'gadgets' }, pr)).toBe(false);
        expect(isChangeFor({ ...pr, owner: 'other' }, pr)).toBe(false);
    });
});

describe('heartbeatReducer', () => {
    test('goes live on a heartbeat, lost when one is missed, and back', () => {
        const live = heartbeatReducer(INITIAL_HEARTBEAT_STATE, { type: 'beat', heartbeat: BEAT });
        expect(live).toEqual({ connection: 'live', heartbeat: BEAT });

        const lost = heartbeatReducer(live, { type: 'silent' });
        expect(lost).toEqual({ connection: 'lost', heartbeat: BEAT });

        const next = { ...BEAT, seq: 8 };
        expect(heartbeatReducer(lost, { type: 'beat', heartbeat: next })).toEqual({
            connection: 'live',
            heartbeat: next,
        });
    });

    test('a server that has never sent a heartbeat is not lost', () => {
        expect(heartbeatReducer(INITIAL_HEARTBEAT_STATE, { type: 'silent' })).toBe(
            INITIAL_HEARTBEAT_STATE
        );
    });
});

describe('heartbeatDeadlineMs', () => {
    test('allows a few missed heartbeats', () => {
        expect(heartbeatDeadlineMs(BEAT)).toBe(30_000);
        expect(heartbeatDeadlineMs({ ...BEAT, interval_seconds: 0 })).toBe(3_000);
    });
});

describe('ServerEvents', () => {
    test('shares one stream among every listener, closing it with the last', () => {
        const { events, opened } = hub();
        const seen: unknown[] = [];
        const offA = events.on(HEARTBEAT_EVENT, data => seen.push(['a', data]));
        const offB = events.on(HEARTBEAT_EVENT, data => seen.push(['b', data]));
        const offC = events.on('Server.Other', data => seen.push(['c', data]));
        expect(opened).toHaveLength(1);

        opened[0].emit(HEARTBEAT_EVENT, '{"seq":1}');
        opened[0].emit('Server.Other', '"hi"');
        expect(seen).toEqual([
            ['a', { seq: 1 }],
            ['b', { seq: 1 }],
            ['c', 'hi'],
        ]);

        offA();
        offB();
        expect(opened[0].closed).toBe(false);
        offC();
        expect(opened[0].closed).toBe(true);

        // A listener after that opens a new stream.
        events.on(HEARTBEAT_EVENT, () => {});
        expect(opened).toHaveLength(2);
        expect(opened[1].listeners.get(HEARTBEAT_EVENT)?.size).toBe(1);
    });

    test('stops delivering to a listener once it unsubscribes', () => {
        const { events, opened } = hub();
        const seen: unknown[] = [];
        const off = events.on(HEARTBEAT_EVENT, data => seen.push(data));
        events.on(HEARTBEAT_EVENT, () => {});
        off();
        opened[0].emit(HEARTBEAT_EVENT, '1');
        expect(seen).toEqual([]);
    });

    test('ignores an event whose data is not JSON', () => {
        const { events, opened } = hub();
        const seen: unknown[] = [];
        events.on(HEARTBEAT_EVENT, data => seen.push(data));
        const error = console.error;
        console.error = () => {};
        try {
            opened[0].emit(HEARTBEAT_EVENT, 'not json');
        } finally {
            console.error = error;
        }
        opened[0].emit(HEARTBEAT_EVENT, '2');
        expect(seen).toEqual([2]);
    });

    test('tells error listeners when the stream drops', () => {
        const { events, opened } = hub();
        let errors = 0;
        const off = events.onError(() => errors++);
        events.on(HEARTBEAT_EVENT, () => {});
        opened[0].fail();
        expect(errors).toBe(1);
        off();
        opened[0].fail();
        expect(errors).toBe(1);
    });

    test('tells open listeners each time the stream connects', () => {
        const { events, opened } = hub();
        let opens = 0;
        const off = events.onOpen(() => opens++);
        events.on(PLUGIN_OUTPUT_CHANGED_EVENT, () => {});
        opened[0].connect();
        // EventSource reconnects by itself after a drop, and says so again.
        opened[0].fail();
        opened[0].connect();
        expect(opens).toBe(2);
        off();
        opened[0].connect();
        expect(opens).toBe(2);
    });

    test('does nothing where there is no EventSource', () => {
        const events = new ServerEvents(() => null);
        const off = events.on(HEARTBEAT_EVENT, () => {});
        off();
    });
});

describe('ServerStatus', () => {
    test('shows nothing before the first heartbeat', () => {
        const html = renderToStaticMarkup(
            <ServerStatus connection="connecting" heartbeat={null} />
        );
        expect(html).toContain('aria-live="polite"');
        expect(html).not.toContain('aria-label');
    });

    test('is a quiet dot while heartbeats arrive', () => {
        const html = renderToStaticMarkup(<ServerStatus connection="live" heartbeat={BEAT} />);
        expect(html).toContain('aria-label="Server connected"');
        expect(html).toContain('heartbeat every 10 s');
        expect(html).not.toContain('not responding');
    });

    test('warns once they stop', () => {
        const html = renderToStaticMarkup(<ServerStatus connection="lost" heartbeat={BEAT} />);
        expect(html).toContain('aria-label="Server not responding"');
        expect(html).toContain('for over 30 s');
        expect(html).toContain('>Server not responding</span>');

        const compact = renderToStaticMarkup(
            <ServerStatus connection="lost" heartbeat={BEAT} compact />
        );
        expect(compact).toContain('aria-label="Server not responding"');
        expect(compact).not.toContain('>Server not responding</span>');
    });
});

const UPDATE: PRUpdate = {
    owner: 'C-Hipple',
    repo: 'code-review-server',
    number: 303,
    event: 'pull_request_review.submitted',
    changes: ['reviews', 'metadata'],
    head_sha: 'abc123',
    actor: 'alice',
    own: false,
    summary: 'alice approved',
};

describe('parsePRUpdate', () => {
    test('reads a PR update', () => {
        expect(parsePRUpdate({ ...UPDATE, time: '2026-10-25T12:00:00Z' })).toEqual(UPDATE);
    });

    test('fills in what a quiet update leaves out', () => {
        expect(
            parsePRUpdate({
                owner: 'a',
                repo: 'b',
                number: 1,
                event: 'sections',
                changes: ['sections'],
            })
        ).toEqual({
            owner: 'a',
            repo: 'b',
            number: 1,
            event: 'sections',
            changes: ['sections'],
            head_sha: undefined,
            actor: undefined,
            own: false,
            summary: undefined,
        });
    });

    test('rejects anything else', () => {
        expect(parsePRUpdate(null)).toBeNull();
        expect(parsePRUpdate({ ...UPDATE, number: '303' })).toBeNull();
        expect(parsePRUpdate({ ...UPDATE, changes: 'reviews' })).toBeNull();
        expect(parsePRUpdate({ owner: 'a' })).toBeNull();
    });
});

describe('onReconnect', () => {
    test('calls back when the stream comes back after dropping, not on its first connect', () => {
        const { events, opened } = hub();
        const offEvents = events.on(PR_UPDATED_EVENT, () => {});
        let reconnects = 0;
        const off = onReconnect(events, () => reconnects++);

        opened[0].connect();
        expect(reconnects).toBe(0);
        opened[0].fail();
        opened[0].connect();
        expect(reconnects).toBe(1);
        // An open with no drop before it brings nothing new.
        opened[0].connect();
        expect(reconnects).toBe(1);

        off();
        opened[0].fail();
        opened[0].connect();
        expect(reconnects).toBe(1);
        offEvents();
    });
});

describe('changesPR', () => {
    test('is false for a change to the list rows alone', () => {
        expect(changesPR(UPDATE)).toBe(true);
        expect(changesPR({ ...UPDATE, changes: ['sections'] })).toBe(false);
        expect(changesPR({ ...UPDATE, changes: ['sections', 'head'] })).toBe(true);
    });
});

describe('prUpdateMessage', () => {
    test('says what happened, naming the PR when asked', () => {
        expect(prUpdateMessage([UPDATE])).toBe('alice approved');
        expect(prUpdateMessage([UPDATE], true)).toBe('code-review-server#303: alice approved');
    });

    test("leaves out quiet updates and the user's own", () => {
        expect(prUpdateMessage([{ ...UPDATE, summary: undefined }])).toBeNull();
        expect(prUpdateMessage([{ ...UPDATE, own: true }])).toBeNull();
        expect(
            prUpdateMessage([
                { ...UPDATE, own: true },
                { ...UPDATE, summary: 'Checks passed' },
            ])
        ).toBe('Checks passed');
    });

    test('says a repeat once and sums up a long burst', () => {
        const said = (summary: string) => ({ ...UPDATE, summary });
        expect(prUpdateMessage([said('Checks passed'), said('Checks passed')])).toBe(
            'Checks passed'
        );
        expect(prUpdateMessage(['a', 'b', 'c', 'd', 'e'].map(said))).toBe('c\nd\ne\n…and 2 more');
    });
});

/** Timers a test fires by hand. */
function manualTimers() {
    const pending = new Map<number, { fn: () => void; ms: number }>();
    let next = 0;
    const timers: Timers = {
        set: (fn, ms) => {
            pending.set(++next, { fn, ms });
            return next;
        },
        clear: handle => pending.delete(handle as number),
    };
    const fire = () => {
        const due = [...pending.values()];
        pending.clear();
        for (const { fn } of due) fn();
    };
    return { timers, pending, fire };
}

describe('onPRUpdates', () => {
    test('batches a burst into one flush, bounded by the first update', () => {
        const { events, opened } = hub();
        const { timers, pending, fire } = manualTimers();
        const flushed: PRUpdate[][] = [];
        onPRUpdates(
            events,
            () => true,
            updates => flushed.push(updates),
            400,
            timers
        );

        opened[0].emit(PR_UPDATED_EVENT, JSON.stringify(UPDATE));
        opened[0].emit(PR_UPDATED_EVENT, JSON.stringify({ ...UPDATE, summary: 'Checks passed' }));
        // The second joined the first's batch rather than putting it off.
        expect(pending.size).toBe(1);
        expect([...pending.values()][0].ms).toBe(400);
        expect(flushed).toEqual([]);

        fire();
        expect(flushed.map(batch => batch.map(u => u.summary))).toEqual([
            ['alice approved', 'Checks passed'],
        ]);

        opened[0].emit(PR_UPDATED_EVENT, JSON.stringify(UPDATE));
        fire();
        expect(flushed).toHaveLength(2);
    });

    test('ignores what it does not accept, and anything that is not an update', () => {
        const { events, opened } = hub();
        const { timers, pending } = manualTimers();
        onPRUpdates(
            events,
            update => update.number === 1,
            () => {},
            400,
            timers
        );

        opened[0].emit(PR_UPDATED_EVENT, JSON.stringify(UPDATE));
        opened[0].emit(PR_UPDATED_EVENT, '{"owner":"a"}');
        expect(pending.size).toBe(0);
    });

    test('drops a pending batch on unsubscribe', () => {
        const { events, opened } = hub();
        const { timers, pending } = manualTimers();
        let flushed = 0;
        const off = onPRUpdates(
            events,
            () => true,
            () => flushed++,
            400,
            timers
        );

        opened[0].emit(PR_UPDATED_EVENT, JSON.stringify(UPDATE));
        off();
        expect(pending.size).toBe(0);
        expect(flushed).toBe(0);
        expect(opened[0].closed).toBe(true);
    });
});
