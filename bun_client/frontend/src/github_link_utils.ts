import type { GetGitHubLinkReply, GitHubLinkStatus } from './api';
import type { StatusVariant } from './design';

/**
 * Linking a GitHub account on a shared server, the client's half of GitHub's
 * device flow (docs/protocol.md): StartGitHubLink hands out a code, the user
 * enters it at github.com/login/device, and the client polls GetGitHubLink
 * at the interval the server gives until the status is no longer `pending`.
 */

/** Where the link dialog is. */
export type LinkPhase =
    /** Explaining why a link is needed, before starting one. */
    | { kind: 'offer' }
    /** Waiting for StartGitHubLink: the server is asking GitHub for a code. */
    | { kind: 'starting' }
    /** A code is waiting to be entered on GitHub, and the client polls. */
    | {
          kind: 'pending';
          userCode: string;
          verificationUri: string;
          expiresAt: string;
          interval: number;
          /** Why the last poll failed, while polling goes on. */
          trouble?: string;
      }
    /** The link ended: linked, or why not. */
    | { kind: 'done'; status: GitHubLinkStatus; message: string; githubLogin: string };

/** How an ended link reads to the user. */
export interface LinkOutcome {
    variant: StatusVariant;
    title: string;
    /** What to know or do next; the server's own words where it has them. */
    detail: string;
    /** Whether starting another link could end differently. */
    canRetry: boolean;
}

/** How a link that ended with `status` reads, given the server's `message`. */
export function linkOutcome(
    status: GitHubLinkStatus,
    message: string,
    githubLogin = ''
): LinkOutcome {
    switch (status) {
        case 'linked':
            return {
                variant: 'success',
                title: githubLogin ? `Linked to GitHub as ${githubLogin}` : 'Linked to GitHub',
                detail: 'Your reviews now go to GitHub as you.',
                canRetry: false,
            };
        case 'denied':
            return {
                variant: 'warning',
                title: 'The authorization was declined on GitHub',
                detail: 'Nothing was linked. Try again and choose Authorize on GitHub.',
                canRetry: true,
            };
        case 'expired':
            return {
                variant: 'warning',
                title: 'The code expired before it was entered',
                detail: 'Codes last about fifteen minutes. Try again for a new one.',
                canRetry: true,
            };
        case 'mismatch':
            return {
                variant: 'danger',
                title: 'That is not your GitHub account here',
                detail:
                    message ||
                    'GitHub authorized another account than the one you are registered as, so nothing was linked.',
                canRetry: true,
            };
        case 'unavailable':
            return {
                variant: 'neutral',
                title: "This server can't link GitHub accounts",
                detail: message || 'Ask its administrator to set up GitHub account linking.',
                canRetry: false,
            };
        case 'unlinked':
            return {
                variant: 'neutral',
                title: 'The link was cancelled',
                detail: 'No GitHub account is linked.',
                canRetry: true,
            };
        default:
            return {
                variant: 'danger',
                title: "GitHub couldn't link the account",
                detail: message || 'Something went wrong on the way.',
                canRetry: true,
            };
    }
}

/**
 * How often to poll for a link: the server's interval (GitHub's, five seconds
 * by default), and never more than once a second.
 */
export function pollIntervalMs(intervalSeconds: number): number {
    const seconds = Number.isFinite(intervalSeconds) && intervalSeconds > 0 ? intervalSeconds : 5;
    return Math.max(1, seconds) * 1000;
}

export interface LinkPollOptions {
    intervalSeconds: number;
    getLink: () => Promise<GetGitHubLinkReply>;
    /** Every answer: those still pending, and the one that ends the link. */
    onReply: (reply: GetGitHubLinkReply) => void;
    /** A poll that failed. Polling goes on: the server keeps the link going. */
    onError: (e: unknown) => void;
    setTimer?: (fn: () => void, ms: number) => unknown;
    clearTimer?: (handle: unknown) => void;
}

/**
 * Polls GetGitHubLink until the link is no longer pending, or until the
 * returned stop function is called — on closing the dialog, or unmounting
 * it. An answer that arrives after stop is dropped.
 */
export function pollGitHubLink({
    intervalSeconds,
    getLink,
    onReply,
    onError,
    setTimer = (fn, ms) => setTimeout(fn, ms),
    clearTimer = handle => clearTimeout(handle as ReturnType<typeof setTimeout>),
}: LinkPollOptions): () => void {
    const ms = pollIntervalMs(intervalSeconds);
    let stopped = false;
    let timer: unknown = null;

    const schedule = () => {
        if (!stopped) timer = setTimer(tick, ms);
    };
    const tick = async () => {
        timer = null;
        let reply: GetGitHubLinkReply;
        try {
            reply = await getLink();
        } catch (e) {
            if (stopped) return;
            onError(e);
            schedule();
            return;
        }
        if (stopped) return;
        onReply(reply);
        if (reply.status === 'pending') schedule();
    };

    schedule();
    return () => {
        stopped = true;
        if (timer !== null) clearTimer(timer);
        timer = null;
    };
}

/** An RFC 3339 time as the local time of day, `16:05`; '' when unreadable. */
export function formatLinkExpiry(expiresAt: string): string {
    const when = new Date(expiresAt);
    if (!expiresAt || Number.isNaN(when.getTime())) return '';
    return when.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}

/** The verification page without its scheme, for a link's text. */
export function displayUri(uri: string): string {
    return uri.replace(/^https?:\/\//, '');
}
