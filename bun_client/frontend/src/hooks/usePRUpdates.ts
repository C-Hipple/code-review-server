import { useEffect, useRef } from 'react';
import {
    onPRUpdates,
    onReconnect,
    serverEvents,
    type PRUpdate,
    type ServerEvents,
} from '../server_events';

/**
 * Calls `onUpdates` with each batch of the server's PR updates (a webhook
 * delivery changing a PR) that `accept` lets through, and with none when the
 * event stream comes back after dropping: whatever was sent meanwhile is lost,
 * so the page reads again, quietly. Both are read fresh on every update, so
 * they can close over the latest state without the stream being re-subscribed
 * each render.
 */
export function usePRUpdates(
    accept: (update: PRUpdate) => boolean,
    onUpdates: (updates: PRUpdate[]) => void,
    events: ServerEvents = serverEvents
) {
    const acceptRef = useRef(accept);
    const onUpdatesRef = useRef(onUpdates);
    acceptRef.current = accept;
    onUpdatesRef.current = onUpdates;

    useEffect(() => {
        const offUpdates = onPRUpdates(
            events,
            update => acceptRef.current(update),
            updates => onUpdatesRef.current(updates)
        );
        const offReconnect = onReconnect(events, () => onUpdatesRef.current([]));
        return () => {
            offUpdates();
            offReconnect();
        };
    }, [events]);
}
