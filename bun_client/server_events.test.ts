import { describe, expect, test } from 'bun:test';
import {
    formatSseEvent,
    HEARTBEAT_METHOD,
    isNotification,
    type JsonRpcNotification,
    ServerEventHub,
} from './server_events';

function heartbeat(seq: number, intervalSeconds = 10): JsonRpcNotification {
    return {
        method: HEARTBEAT_METHOD,
        params: [
            {
                seq,
                time: '2026-10-25T12:00:00Z',
                started_at: '2026-10-25T11:00:00Z',
                interval_seconds: intervalSeconds,
            },
        ],
        id: null,
    };
}

/**
 * A page reading its stream. `drain` returns what has arrived so far; a read
 * still waiting when it returns is kept for the next call, so nothing it
 * receives later is lost.
 */
function page(stream: ReadableStream<Uint8Array>) {
    const reader = stream.getReader();
    const decoder = new TextDecoder();
    let pending: Promise<ReadableStreamReadResult<Uint8Array>> | null = null;
    return {
        reader,
        async drain(): Promise<string> {
            let text = '';
            while (true) {
                pending ??= reader.read();
                const next = await Promise.race([
                    pending,
                    new Promise<null>(resolve => setTimeout(() => resolve(null), 20)),
                ]);
                if (next === null) return text;
                pending = null;
                if (next.done) return text;
                text += decoder.decode(next.value, { stream: true });
            }
        },
    };
}

describe('isNotification', () => {
    test('tells a notification from a reply', () => {
        expect(isNotification(heartbeat(1))).toBe(true);
        expect(isNotification({ id: 3, result: {}, error: null })).toBe(false);
        expect(isNotification({ id: null, result: null, error: 'boom' })).toBe(false);
        expect(isNotification(null)).toBe(false);
        expect(isNotification('Server.Heartbeat')).toBe(false);
    });
});

describe('formatSseEvent', () => {
    test('names the event and puts the JSON on one data line', () => {
        expect(formatSseEvent('Server.Test', { body: 'two\nlines' })).toBe(
            'event: Server.Test\ndata: {"body":"two\\nlines"}\n\n'
        );
        expect(formatSseEvent('Server.Test', undefined)).toBe('event: Server.Test\ndata: null\n\n');
    });
});

describe('ServerEventHub', () => {
    test('relays every notification to every open page', async () => {
        const hub = new ServerEventHub();
        const a = page(hub.stream());
        const b = page(hub.stream());
        expect(await a.drain()).toBe('retry: 2000\n\n');
        await b.drain();

        hub.publish({ method: 'Server.Test', params: [{ x: 1 }], id: null });
        const expected = 'event: Server.Test\ndata: {"x":1}\n\n';
        expect(await a.drain()).toBe(expected);
        expect(await b.drain()).toBe(expected);
    });

    test('opens a stream with the latest heartbeat while the next is not overdue', async () => {
        let now = 1_000_000;
        const hub = new ServerEventHub(() => now);
        hub.publish(heartbeat(4, 10));

        now += 9_000;
        const fresh = await page(hub.stream()).drain();
        expect(fresh).toBe(
            `retry: 2000\n\n${formatSseEvent(HEARTBEAT_METHOD, heartbeat(4).params?.[0])}`
        );

        // Past the interval the server has missed a beat: a page that opens
        // now waits for the next one instead of hearing an old one as current.
        now += 2_000;
        expect(await page(hub.stream()).drain()).toBe('retry: 2000\n\n');
    });

    test('forgets a page whose stream is cancelled', async () => {
        const hub = new ServerEventHub();
        const { reader } = page(hub.stream());
        hub.stream();
        expect(hub.size).toBe(2);
        await reader.cancel();
        expect(hub.size).toBe(1);
        hub.publish(heartbeat(1));
        expect(hub.size).toBe(1);
    });
});
