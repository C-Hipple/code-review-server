import { useEffect, useRef } from 'react';
import type { PullRef } from '../github_url';
import { changeFor, parseServerEvent, type PRChange } from '../server_events';
import { EVENTS_PORT } from '../types';

/** The parts of chrome.runtime.Port the hook uses, so tests can stand in. */
export interface EventsPort {
    onMessage: { addListener(callback: (message: unknown) => void): void };
    onDisconnect: { addListener(callback: () => void): void };
    disconnect(): void;
}

/** How soon a dropped events port is opened again. */
export const RECONNECT_MS = 1000;

function connectEvents(): EventsPort {
    return chrome.runtime.connect({ name: EVENTS_PORT });
}

/**
 * Calls `onChange` for each change the server announces to `pr`'s plugin
 * output or AI results, relayed by the service worker over an events port —
 * so the view reads its output again when it changes instead of polling for
 * it. A `resync` means events may have been missed (a new server, or the
 * port dropped and was opened again), and everything is to be read again.
 *
 * `onChange` is read when an event arrives, so a new identity each render
 * doesn't reopen the port.
 */
export function usePRChanges(
    pr: PullRef,
    onChange: (change: PRChange) => void,
    connect: () => EventsPort = connectEvents
): void {
    const latest = useRef(onChange);
    useEffect(() => {
        latest.current = onChange;
    });

    const { owner, repo, number } = pr;
    useEffect(() => {
        let stopped = false;
        let port: EventsPort | null = null;
        let timer: ReturnType<typeof setTimeout> | undefined;

        const open = (reopened: boolean) => {
            try {
                port = connect();
            } catch {
                // The extension was reloaded under this page: nothing to hear.
                return;
            }
            port.onMessage.addListener(message => {
                const event = parseServerEvent(message);
                const change = event && changeFor(event, { owner, repo, number });
                if (change && !stopped) latest.current(change);
            });
            // The service worker went away (an extension update, say). Open
            // the port again, which wakes it, and catch up on what was missed.
            port.onDisconnect.addListener(() => {
                port = null;
                if (stopped) return;
                timer = setTimeout(() => open(true), RECONNECT_MS);
            });
            if (reopened) latest.current({ kind: 'resync' });
        };

        open(false);
        return () => {
            stopped = true;
            clearTimeout(timer);
            port?.disconnect();
        };
    }, [owner, repo, number, connect]);
}
