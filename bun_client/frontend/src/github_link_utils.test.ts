import { describe, expect, test } from 'bun:test';
import type { GetGitHubLinkReply } from './api';
import {
    displayUri,
    formatLinkExpiry,
    linkOutcome,
    pollGitHubLink,
    pollIntervalMs,
} from './github_link_utils';

const reply = (over: Partial<GetGitHubLinkReply> = {}): GetGitHubLinkReply => ({
    okay: true,
    status: 'pending',
    github_login: '',
    user_code: 'WDJB-MJHT',
    verification_uri: 'https://github.com/login/device',
    expires_at: '2026-10-11T17:00:00Z',
    message: '',
    ...over,
});

/** Timers the test fires by hand. */
function manualTimers() {
    const timers = new Map<number, { fn: () => void; ms: number }>();
    let next = 1;
    return {
        timers,
        setTimer: (fn: () => void, ms: number) => {
            timers.set(next, { fn, ms });
            return next++;
        },
        clearTimer: (handle: unknown) => {
            timers.delete(handle as number);
        },
        /** Fires the one armed timer and lets its poll settle. */
        async fire() {
            const [[handle, { fn }]] = [...timers.entries()];
            timers.delete(handle);
            fn();
            await new Promise(resolve => setTimeout(resolve, 0));
        },
    };
}

describe('pollGitHubLink', () => {
    test('polls at the interval until the link is no longer pending', async () => {
        const answers = [reply(), reply({ status: 'linked', github_login: 'octocat' })];
        const seen: string[] = [];
        const clock = manualTimers();
        pollGitHubLink({
            intervalSeconds: 5,
            getLink: async () => answers.shift()!,
            onReply: r => seen.push(r.status),
            onError: () => seen.push('error'),
            ...clock,
        });

        expect([...clock.timers.values()].map(t => t.ms)).toEqual([5000]);
        await clock.fire();
        expect(seen).toEqual(['pending']);
        expect(clock.timers.size).toBe(1);
        await clock.fire();
        expect(seen).toEqual(['pending', 'linked']);
        // Ended: nothing more is armed.
        expect(clock.timers.size).toBe(0);
    });

    test('keeps polling through a failed poll', async () => {
        const seen: string[] = [];
        const clock = manualTimers();
        let calls = 0;
        pollGitHubLink({
            intervalSeconds: 1,
            getLink: async () => {
                if (calls++ === 0) throw new Error('lost the connection');
                return reply({ status: 'denied' });
            },
            onReply: r => seen.push(r.status),
            onError: e => seen.push((e as Error).message),
            ...clock,
        });
        await clock.fire();
        expect(seen).toEqual(['lost the connection']);
        await clock.fire();
        expect(seen).toEqual(['lost the connection', 'denied']);
        expect(clock.timers.size).toBe(0);
    });

    test('stopping clears the timer and drops an answer still on its way', async () => {
        const seen: string[] = [];
        const clock = manualTimers();
        let answer!: (r: GetGitHubLinkReply) => void;
        const stop = pollGitHubLink({
            intervalSeconds: 1,
            getLink: () => new Promise(resolve => (answer = resolve)),
            onReply: r => seen.push(r.status),
            onError: () => seen.push('error'),
            ...clock,
        });
        await clock.fire();
        stop();
        answer(reply({ status: 'linked' }));
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(seen).toEqual([]);
        expect(clock.timers.size).toBe(0);

        // Stopped before the first poll: nothing is left armed.
        const idle = manualTimers();
        pollGitHubLink({
            intervalSeconds: 1,
            getLink: async () => reply(),
            onReply: () => {},
            onError: () => {},
            ...idle,
        })();
        expect(idle.timers.size).toBe(0);
    });
});

describe('pollIntervalMs', () => {
    test('takes the server’s interval, never faster than a second', () => {
        expect(pollIntervalMs(5)).toBe(5000);
        expect(pollIntervalMs(1)).toBe(1000);
        expect(pollIntervalMs(0.2)).toBe(1000);
        expect(pollIntervalMs(0)).toBe(5000);
        expect(pollIntervalMs(Number.NaN)).toBe(5000);
    });
});

describe('linkOutcome', () => {
    test('a link says whose account it is', () => {
        expect(linkOutcome('linked', '', 'octocat')).toMatchObject({
            variant: 'success',
            title: 'Linked to GitHub as octocat',
            canRetry: false,
        });
    });

    test('the ends a new link could change offer to try again', () => {
        for (const status of ['denied', 'expired', 'mismatch', 'error', 'unlinked'] as const) {
            expect(linkOutcome(status, 'why').canRetry).toBe(true);
        }
        expect(linkOutcome('unavailable', 'no client ID').canRetry).toBe(false);
    });

    test('the server explains a mismatch, an error and unavailability in its own words', () => {
        const mismatch =
            "GitHub says that authorization is hubot's, but this connection is registered as octocat, so nothing was stored; sign in to GitHub as octocat and link again";
        expect(linkOutcome('mismatch', mismatch).detail).toBe(mismatch);
        expect(linkOutcome('error', 'GitHub did not complete the link: boom').detail).toBe(
            'GitHub did not complete the link: boom'
        );
        expect(linkOutcome('unavailable', 'no GitHubOAuthClientID').detail).toBe(
            'no GitHubOAuthClientID'
        );
    });
});

describe('formatting', () => {
    test('an expiry reads as a time of day, and nothing when unreadable', () => {
        expect(formatLinkExpiry('2026-10-11T17:05:00Z')).toMatch(/\d{1,2}:\d{2}/);
        expect(formatLinkExpiry('')).toBe('');
        expect(formatLinkExpiry('soon')).toBe('');
    });

    test('the verification page reads without its scheme', () => {
        expect(displayUri('https://github.com/login/device')).toBe('github.com/login/device');
    });
});
