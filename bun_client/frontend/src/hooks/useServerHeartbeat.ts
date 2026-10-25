import { useEffect, useReducer } from 'react';
import {
    HEARTBEAT_EVENT,
    heartbeatDeadlineMs,
    heartbeatReducer,
    INITIAL_HEARTBEAT_STATE,
    parseHeartbeat,
    serverEvents,
    type HeartbeatState,
    type ServerEvents,
} from '../server_events';

/**
 * Whether the server is still there, from the heartbeats it pushes. Live while
 * each arrives before the last one's deadline; lost when one doesn't, or the
 * event stream drops, until the next arrives.
 */
export function useServerHeartbeat(events: ServerEvents = serverEvents): HeartbeatState {
    const [state, dispatch] = useReducer(heartbeatReducer, INITIAL_HEARTBEAT_STATE);

    useEffect(() => {
        let overdue: ReturnType<typeof setTimeout> | undefined;
        const offBeat = events.on(HEARTBEAT_EVENT, data => {
            const heartbeat = parseHeartbeat(data);
            if (!heartbeat) return;
            clearTimeout(overdue);
            overdue = setTimeout(
                () => dispatch({ type: 'silent' }),
                heartbeatDeadlineMs(heartbeat)
            );
            dispatch({ type: 'beat', heartbeat });
        });
        // The bridge went away or is restarting. The EventSource retries by
        // itself, and the next heartbeat on the new stream brings it back.
        const offError = events.onError(() => {
            clearTimeout(overdue);
            dispatch({ type: 'silent' });
        });
        return () => {
            clearTimeout(overdue);
            offBeat();
            offError();
        };
    }, [events]);

    return state;
}
