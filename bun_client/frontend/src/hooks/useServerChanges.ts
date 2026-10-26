import { useEffect, useRef } from 'react';
import { serverEvents, type ServerEvents } from '../server_events';

export interface ServerChangesOptions {
    /** Resubscribes when it changes: the PR (and feature) being shown. */
    key: string;
    /** Listen only while this is set. */
    enabled?: boolean;
    events?: ServerEvents;
}

/**
 * Calls `refresh` whenever the server pushes an `event` that `matches`
 * accepts, and whenever the event stream (re)connects: anything sent while it
 * was down never arrives, so that is when to catch up. This is how a view
 * keeps output the server produces in the background current without polling.
 *
 * One refresh runs at a time. Changes come in bursts (a run announces itself
 * starting, then landing), so any that arrive while a refresh is in flight ask
 * for one more once it's done rather than one each.
 *
 * `matches` and `refresh` are read when needed, so new identities on each
 * render don't resubscribe; a new `key` does.
 */
export function useServerChanges(
    event: string,
    matches: (data: unknown) => boolean,
    refresh: () => Promise<unknown>,
    { key, enabled = true, events = serverEvents }: ServerChangesOptions
): void {
    const matchesRef = useRef(matches);
    const refreshRef = useRef(refresh);
    useEffect(() => {
        matchesRef.current = matches;
        refreshRef.current = refresh;
    });

    useEffect(() => {
        if (!enabled) return;
        let active = true;
        let running = false;
        let again = false;
        const run = async () => {
            if (running) {
                again = true;
                return;
            }
            running = true;
            try {
                do {
                    again = false;
                    try {
                        await refreshRef.current();
                    } catch (e) {
                        console.error(`Failed to refresh after ${event}:`, e);
                    }
                } while (again && active);
            } finally {
                running = false;
            }
        };
        const offEvent = events.on(event, data => {
            if (matchesRef.current(data)) void run();
        });
        const offOpen = events.onOpen(() => void run());
        return () => {
            active = false;
            offEvent();
            offOpen();
        };
    }, [event, key, enabled, events]);
}
