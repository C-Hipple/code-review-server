import { describe, expect, test } from 'bun:test';
import type { BridgeSession } from './api';
import { SessionStore } from './session';

/** A load the test settles by hand, in whatever order it likes. */
function loads() {
    const pending: { resolve: (s: BridgeSession) => void; reject: (e: unknown) => void }[] = [];
    const load = () =>
        new Promise<BridgeSession>((resolve, reject) => pending.push({ resolve, reject }));
    return { load, pending };
}

const remote = (over: Partial<BridgeSession> = {}): BridgeSession => ({
    remote: true,
    address: 'crs.test:7700',
    login: 'octocat',
    is_admin: false,
    github_linked: false,
    github_link_available: true,
    ...over,
});

describe('SessionStore', () => {
    test('reads once on first use, and tells subscribers', async () => {
        const { load, pending } = loads();
        const store = new SessionStore(load);
        let told = 0;
        store.subscribe(() => told++);

        expect(store.get()).toBeNull();
        store.ensure();
        store.ensure();
        expect(pending).toHaveLength(1);

        pending[0].resolve(remote());
        await Promise.resolve();
        await Promise.resolve();
        expect(store.get()).toEqual(remote());
        expect(told).toBe(1);
    });

    test('a read started later wins, whichever lands first', async () => {
        const { load, pending } = loads();
        const store = new SessionStore(load);
        const before = store.refresh();
        const after = store.refresh();

        // The read made after linking lands first...
        pending[1].resolve(remote({ github_linked: true }));
        await after;
        // ...and the one begun before it cannot undo it.
        pending[0].resolve(remote({ github_linked: false }));
        expect(await before).toEqual(remote({ github_linked: true }));
        expect(store.get()?.github_linked).toBe(true);
    });

    test('a failed read keeps what was known, with the reason', async () => {
        const { load, pending } = loads();
        const store = new SessionStore(load);
        const first = store.refresh();
        pending[0].resolve(remote({ github_linked: true }));
        await first;

        const second = store.refresh();
        pending[1].reject(new Error('Failed to fetch'));
        expect(await second).toEqual(remote({ github_linked: true, error: 'Failed to fetch' }));
    });

    test('with nothing known, a failed read is taken for a local bridge', async () => {
        const store = new SessionStore(() => Promise.reject(new Error('404')));
        expect(await store.refresh()).toEqual({ remote: false, error: '404' });
    });

    test('unsubscribing stops the news', async () => {
        const store = new SessionStore(() => Promise.resolve(remote()));
        let told = 0;
        const unsubscribe = store.subscribe(() => told++);
        unsubscribe();
        await store.refresh();
        expect(told).toBe(0);
    });
});
