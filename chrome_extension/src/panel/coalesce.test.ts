import { describe, expect, test } from 'bun:test';
import { coalesce } from './coalesce';

/** A run the test finishes by hand. */
function gate() {
    const releases: (() => void)[] = [];
    let calls = 0;
    const run = () => {
        calls++;
        return new Promise<void>(resolve => releases.push(resolve));
    };
    return { run, calls: () => calls, release: () => releases.shift()?.() };
}

describe('coalesce', () => {
    test('a burst of calls while one runs is one more run', async () => {
        const g = gate();
        const refresh = coalesce(g.run);
        const first = refresh();
        refresh();
        refresh();
        const last = refresh();
        expect(g.calls()).toBe(1);

        g.release();
        await Promise.resolve();
        await Promise.resolve();
        expect(g.calls()).toBe(2);
        g.release();
        await first;
        await last;
        expect(g.calls()).toBe(2);
    });

    test('a call after the runs ended starts a new one', async () => {
        const g = gate();
        const refresh = coalesce(g.run);
        const one = refresh();
        g.release();
        await one;
        const two = refresh();
        expect(g.calls()).toBe(2);
        g.release();
        await two;
    });

    test('a failing run does not stop the next', async () => {
        let calls = 0;
        const refresh = coalesce(async () => {
            calls++;
            throw new Error('boom');
        });
        await refresh();
        await refresh();
        expect(calls).toBe(2);
    });
});
