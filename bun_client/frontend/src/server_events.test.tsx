import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import ServerStatus from './components/ServerStatus';
import {
    HEARTBEAT_EVENT,
    heartbeatDeadlineMs,
    heartbeatReducer,
    INITIAL_HEARTBEAT_STATE,
    isChangeFor,
    parseAIOutputChanged,
    parseHeartbeat,
    parsePluginOutputChanged,
    PLUGIN_OUTPUT_CHANGED_EVENT,
    ServerEvents,
    type EventStream,
    type Heartbeat,
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
