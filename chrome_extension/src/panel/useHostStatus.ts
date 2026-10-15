import { useCallback, useEffect, useState } from 'react';
import { getStatus } from '../rpc';
import type { HostStatus } from '../types';

const REFRESH_MS = 5000;

/**
 * How the native connection stands, for the header's dot. Asking the
 * service worker never connects, so this is cheap to refresh; it is read on
 * mount, every few seconds, and whenever a view calls `refresh` after a call.
 */
export function useHostStatus(): [HostStatus | null, () => void] {
    const [status, setStatus] = useState<HostStatus | null>(null);

    const refresh = useCallback(() => {
        getStatus().then(setStatus, () => setStatus(null));
    }, []);

    useEffect(() => {
        let live = true;
        const read = () =>
            getStatus().then(
                s => live && setStatus(s),
                () => live && setStatus(null)
            );
        void read();
        const id = setInterval(read, REFRESH_MS);
        return () => {
            live = false;
            clearInterval(id);
        };
    }, []);

    return [status, refresh];
}
