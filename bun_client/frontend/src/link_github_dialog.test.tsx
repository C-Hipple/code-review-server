import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import type { BridgeSession, GetGitHubLinkReply } from './api';
import { AccountChip, GitHubLinkState } from './components/AccountPanel';
import { LinkGitHubDialogView } from './components/LinkGitHubDialog';
import type { LinkPhase } from './github_link_utils';

const textOf = (html: string): string =>
    html
        .replace(/<[^>]*>/g, ' ')
        .replace(/&#x27;/g, "'")
        .replace(/&quot;/g, '"')
        .replace(/&amp;/g, '&')
        .replace(/\s+/g, ' ')
        .trim();

const noop = () => {};

const dialog = (phase: LinkPhase, over: Partial<Parameters<typeof LinkGitHubDialogView>[0]> = {}) =>
    renderToStaticMarkup(
        <LinkGitHubDialogView
            isOpen
            phase={phase}
            login="octocat"
            onStart={noop}
            onClose={noop}
            {...over}
        />
    );

const pending: LinkPhase = {
    kind: 'pending',
    userCode: 'WDJB-MJHT',
    verificationUri: 'https://github.com/login/device',
    expiresAt: '2026-10-11T17:05:00Z',
    interval: 5,
};

describe('LinkGitHubDialogView', () => {
    test('offers to link, saying why', () => {
        const text = textOf(
            dialog(
                { kind: 'offer' },
                {
                    reason: 'Your review on acme/widgets#42 was not submitted: link your GitHub account to submit reviews as yourself. Your draft is kept.',
                }
            )
        );
        expect(text).toContain('Link your GitHub account');
        expect(text).toContain('Your draft is kept.');
        expect(text).toContain('Cancel');
        expect(text).toContain('Link GitHub account');
    });

    test('shows the code, a way to copy it, and the page to enter it on', () => {
        const html = dialog(pending);
        const text = textOf(html);
        expect(text).toContain('WDJB-MJHT');
        expect(text).toContain('Copy');
        expect(text).toContain('Open github.com/login/device ↗');
        expect(html).toContain('href="https://github.com/login/device"');
        expect(html).toContain('target="_blank"');
        expect(html).toContain('rel="noopener noreferrer"');
        expect(text).toContain('Sign in to GitHub as octocat');
        expect(text).toContain('The code expires at');
        expect(text).toContain('Waiting for GitHub…');
    });

    test('says when it cannot reach the server, and that it keeps trying', () => {
        expect(textOf(dialog({ ...pending, trouble: 'Failed to fetch' }))).toContain(
            "Can't reach the server right now (Failed to fetch); still trying…"
        );
    });

    test('once linked, offers the write that was refused', () => {
        const text = textOf(
            dialog(
                { kind: 'done', status: 'linked', message: '', githubLogin: 'octocat' },
                { retryLabel: 'Submit again', onRetry: noop }
            )
        );
        expect(text).toContain('Linked to GitHub as octocat');
        expect(text).toContain('Submit again');
        expect(text).not.toContain('Try again');
    });

    test('linked from the account tab, there is nothing to retry', () => {
        const text = textOf(
            dialog({ kind: 'done', status: 'linked', message: '', githubLogin: 'octocat' })
        );
        expect(text).toContain('Done');
        expect(text).not.toContain('Submit again');
    });

    test('a declined link says so and offers to try again', () => {
        const text = textOf(
            dialog({
                kind: 'done',
                status: 'denied',
                message: 'The authorization was declined on GitHub, so nothing was linked',
                githubLogin: '',
            })
        );
        expect(text).toContain('The authorization was declined on GitHub');
        expect(text).toContain('Try again');
    });

    test('a server that cannot link says so, with nothing to retry', () => {
        const text = textOf(
            dialog({
                kind: 'done',
                status: 'unavailable',
                message:
                    'This server has no GitHubOAuthClientID configured, so GitHub accounts cannot be linked',
                githubLogin: '',
            })
        );
        expect(text).toContain("This server can't link GitHub accounts");
        expect(text).toContain('no GitHubOAuthClientID configured');
        expect(text).not.toContain('Try again');
    });

    test('renders nothing while closed', () => {
        expect(dialog(pending, { isOpen: false })).toBe('');
    });
});

const session = (over: Partial<BridgeSession> = {}): BridgeSession => ({
    remote: true,
    address: 'crs.test:7700',
    login: 'octocat',
    is_admin: false,
    github_linked: false,
    github_link_available: true,
    ...over,
});

const link = (over: Partial<GetGitHubLinkReply>): GetGitHubLinkReply => ({
    okay: true,
    status: 'unlinked',
    github_login: '',
    user_code: '',
    verification_uri: '',
    expires_at: '',
    message: '',
    ...over,
});

const linkState = (s: BridgeSession, l: GetGitHubLinkReply | null, confirmUnlink = false) =>
    textOf(
        renderToStaticMarkup(
            <GitHubLinkState
                session={s}
                link={l}
                loadError=""
                confirmUnlink={confirmUnlink}
                unlinking={false}
                onLink={noop}
                onUnlink={noop}
                onConfirmUnlink={noop}
                onKeep={noop}
            />
        )
    );

describe('GitHubLinkState', () => {
    test('linked: which account, and Unlink, which asks first', () => {
        const linked = link({ status: 'linked', github_login: 'octocat' });
        expect(linkState(session(), linked)).toContain('Linked as octocat');
        expect(linkState(session(), linked)).toContain('Unlink');
        expect(linkState(session(), linked, true)).toContain('Unlink it?');
        expect(linkState(session(), linked, true)).toContain('Keep it');
    });

    test('not linked: offers to link', () => {
        const text = linkState(session(), link({ status: 'unlinked' }));
        expect(text).toContain('Not linked');
        expect(text).toContain('Link GitHub account');
    });

    test('a link waiting on GitHub can be picked back up', () => {
        const text = linkState(
            session(),
            link({
                status: 'pending',
                user_code: 'WDJB-MJHT',
                verification_uri: 'https://github.com/login/device',
            })
        );
        expect(text).toContain('waiting for code WDJB-MJHT at github.com/login/device');
        expect(text).toContain('Continue linking');
    });

    test('a server that cannot link says why, with nothing to press', () => {
        const text = linkState(
            session({ github_link_available: false }),
            link({ okay: false, status: 'unavailable', message: 'no GitHubOAuthClientID' })
        );
        expect(text).toContain("This server can't link GitHub accounts");
        expect(text).toContain('no GitHubOAuthClientID');
        expect(text).not.toContain('Link GitHub account');
    });

    test('until the server answers, the session’s flags say enough', () => {
        expect(linkState(session({ github_linked: true }), null)).toContain('Linked as octocat');
        expect(linkState(session(), null)).toContain('Not linked');
    });
});

describe('AccountChip', () => {
    const chip = (s: BridgeSession, compact = false) =>
        renderToStaticMarkup(<AccountChip session={s} compact={compact} onOpen={noop} />);

    test('shows the login and says whether GitHub is linked', () => {
        expect(textOf(chip(session()))).toBe('octocat');
        expect(chip(session())).toContain('GitHub account not linked');
        expect(chip(session({ github_linked: true }))).toContain('GitHub account linked');
        expect(chip(session({ github_link_available: false }))).toContain(
            'GitHub accounts cannot be linked on this server'
        );
    });

    test('compact, only the dot', () => {
        expect(textOf(chip(session(), true))).toBe('');
        expect(chip(session(), true)).toContain('aria-label="Account: octocat');
    });

    test('the administrator token reads as the administrator', () => {
        expect(textOf(chip(session({ login: '', is_admin: true })))).toBe('administrator');
    });

    test('a bridge that has not signed in names nobody, and says why', () => {
        const refused: BridgeSession = {
            remote: true,
            address: 'crs.test:7700',
            connected: false,
            error: 'Not registered with crs at crs.test:7700: set CRS_REGISTRATION_KEY',
        };
        expect(textOf(chip(refused))).toBe('not connected');
        expect(chip(refused)).not.toContain('administrator');
        expect(chip(refused)).toContain(
            'aria-label="Account: not connected, Not registered with crs at crs.test:7700: set CRS_REGISTRATION_KEY"'
        );
        expect(chip(refused)).toContain('title="Not connected to crs.test:7700');
        // One from before the bridge said whether it had signed in, with no
        // login to show, is no administrator either.
        expect(textOf(chip({ remote: true, address: 'crs.test:7700', error: 'down' }))).toBe(
            'not connected'
        );
    });
});
