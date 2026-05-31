import { useState, useEffect } from 'react';
import { breakpoints, media } from '../design/styles/tokens';

/**
 * Subscribe to a CSS media query and re-render when it changes.
 * SSR-safe: returns `false` when `window` is unavailable.
 */
export function useMediaQuery(query: string): boolean {
    const getMatch = () =>
        typeof window !== 'undefined' && typeof window.matchMedia === 'function'
            ? window.matchMedia(query).matches
            : false;

    const [matches, setMatches] = useState<boolean>(getMatch);

    useEffect(() => {
        if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
            return;
        }
        const mql = window.matchMedia(query);
        const handler = (e: MediaQueryListEvent) => setMatches(e.matches);

        // Sync immediately in case the query changed between render and effect.
        setMatches(mql.matches);
        mql.addEventListener('change', handler);
        return () => mql.removeEventListener('change', handler);
    }, [query]);

    return matches;
}

/**
 * True when the viewport is at or below the `md` breakpoint (phones / small
 * tablets). The primary switch for rendering the mobile-friendly layout.
 */
export function useIsMobile(): boolean {
    return useMediaQuery(media.md);
}

/** True at or below the `sm` breakpoint (phones). */
export function useIsSmallScreen(): boolean {
    return useMediaQuery(media.sm);
}

export { breakpoints };
