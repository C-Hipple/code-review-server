// Server → browser events.
//
// Once the bridge subscribes (RPCHandler.Subscribe), the Go server writes
// JSON-RPC notifications to its stdout between replies: a Server.Heartbeat
// every interval, and whatever events it sends. The browser can't read that
// stdout, so the bridge relays each notification to every page holding
// `GET /api/events` open, as a Server-Sent Event named after the
// notification's method, its data the notification's params[0].

export const HEARTBEAT_METHOD = 'Server.Heartbeat';

/** How soon a page's EventSource retries after the stream drops. */
const RETRY_MS = 2000;

export interface JsonRpcNotification {
    method: string;
    params?: unknown[];
    id?: null;
}

/** A message from the server that is a notification rather than a reply. */
export function isNotification(msg: unknown): msg is JsonRpcNotification {
    return (
        typeof msg === 'object' &&
        msg !== null &&
        typeof (msg as { method?: unknown }).method === 'string'
    );
}

/**
 * One Server-Sent Event. The data is JSON, which never holds a raw newline, so
 * it always fits on one `data:` line.
 */
export function formatSseEvent(event: string, data: unknown): string {
    return `event: ${event}\ndata: ${JSON.stringify(data ?? null)}\n\n`;
}

type Controller = ReadableStreamDefaultController<Uint8Array>;

export class ServerEventHub {
    private readonly pages = new Set<Controller>();
    private readonly encoder = new TextEncoder();
    private latestHeartbeat: { data: unknown; receivedAt: number; intervalMs: number } | null =
        null;

    constructor(private readonly now: () => number = Date.now) {}

    /** Pages with an event stream open. */
    get size(): number {
        return this.pages.size;
    }

    /** Relays a notification from the server to every open page. */
    publish(notification: JsonRpcNotification) {
        const data = notification.params?.[0] ?? null;
        if (notification.method === HEARTBEAT_METHOD) {
            const seconds = (data as { interval_seconds?: unknown } | null)?.interval_seconds;
            this.latestHeartbeat = {
                data,
                receivedAt: this.now(),
                intervalMs: typeof seconds === 'number' && seconds > 0 ? seconds * 1000 : 0,
            };
        }
        const chunk = this.encoder.encode(formatSseEvent(notification.method, data));
        for (const page of this.pages) {
            try {
                page.enqueue(chunk);
            } catch {
                // The page went away without its stream being cancelled.
                this.pages.delete(page);
            }
        }
    }

    /**
     * A page's event stream. It opens with the latest heartbeat, when the next
     * one isn't overdue yet, so a page that just loaded knows the server is up
     * without waiting out an interval.
     */
    stream(): ReadableStream<Uint8Array> {
        let controller: Controller;
        return new ReadableStream<Uint8Array>({
            start: c => {
                controller = c;
                this.pages.add(c);
                let opening = `retry: ${RETRY_MS}\n\n`;
                const beat = this.latestHeartbeat;
                if (beat && this.now() - beat.receivedAt < beat.intervalMs) {
                    opening += formatSseEvent(HEARTBEAT_METHOD, beat.data);
                }
                c.enqueue(this.encoder.encode(opening));
            },
            cancel: () => {
                this.pages.delete(controller);
            },
        });
    }
}
