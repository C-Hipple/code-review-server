import { useEffect, useSyncExternalStore } from 'react';
import type { BridgeSession } from '../api';
import { bridgeSession } from '../session';

/**
 * Who the bridge is connected as (see session.ts): null until the first read
 * lands, `{ remote: false }` for a bridge running its own crs. The first
 * component to ask starts the read; `bridgeSession.refresh()` reads it again
 * for everyone.
 */
export function useBridgeSession(): BridgeSession | null {
    const session = useSyncExternalStore(
        bridgeSession.subscribe,
        bridgeSession.get,
        bridgeSession.get
    );
    useEffect(() => bridgeSession.ensure(), []);
    return session;
}
