import { useEffect, useRef, useState } from 'react';
import { POLL_INTERVAL_MS } from './poll_utils';

/**
 * Calls `poll` every `intervalMs` while `active`, waiting for each call to
 * settle before scheduling the next so slow replies never stack up. Stops
 * when `active` turns false or the component unmounts.
 */
export function usePolling(
    active: boolean,
    poll: () => Promise<unknown>,
    intervalMs = POLL_INTERVAL_MS
): void {
    const latest = useRef(poll);
    useEffect(() => {
        latest.current = poll;
    });

    useEffect(() => {
        if (!active) return;
        let stopped = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        const schedule = () => {
            timer = setTimeout(async () => {
                try {
                    await latest.current();
                } catch {
                    // The poll reports its own errors; keep going.
                }
                if (!stopped) schedule();
            }, intervalMs);
        };
        schedule();
        return () => {
            stopped = true;
            clearTimeout(timer);
        };
    }, [active, intervalMs]);
}

/** The current time, refreshed every `intervalMs`, for relative times on screen. */
export function useNow(intervalMs = 30_000): number {
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        const id = setInterval(() => setNow(Date.now()), intervalMs);
        return () => clearInterval(id);
    }, [intervalMs]);
    return now;
}
