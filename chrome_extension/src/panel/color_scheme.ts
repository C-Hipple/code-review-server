// The system light/dark setting, which the panel follows (not GitHub's theme).

import { useSyncExternalStore } from 'react';

const QUERY = '(prefers-color-scheme: dark)';

function media(): MediaQueryList | null {
    return typeof window !== 'undefined' && typeof window.matchMedia === 'function'
        ? window.matchMedia(QUERY)
        : null;
}

function subscribe(onChange: () => void): () => void {
    const m = media();
    m?.addEventListener('change', onChange);
    return () => m?.removeEventListener('change', onChange);
}

export function prefersDark(): boolean {
    return media()?.matches ?? false;
}

/** Whether the system prefers dark, kept current as it changes. */
export function usePrefersDark(): boolean {
    return useSyncExternalStore(subscribe, prefersDark, () => false);
}
