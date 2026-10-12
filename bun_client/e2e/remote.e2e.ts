// Remote mode: the bridge on REMOTE_PORT connects to a hosted crs over TCP
// (CRS_REMOTE) instead of spawning one, and has to authenticate first — here a
// fake crs started with --listen, which registered users outlive E2E.Reset.
// The bridge starts with no token, so it registered as it came up.

import { readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { REMOTE_LOGIN, REMOTE_USER_REGISTRATION } from './fixtures/remote_user';
import { REGISTRATION_KEY, REMOTE_PORT, remoteHomeFor, urlFor } from './harness/config';
import {
    type AuthCall,
    expect,
    modal,
    openReview,
    PR42,
    SCREENSHOT_URL,
    test,
} from './harness/test';

test.use({ baseURL: urlFor(REMOTE_PORT) });

const PR42_ARGS = { Owner: PR42.owner, Repo: PR42.repo, Number: PR42.number };
const DRAFT = { Filename: 'src/main.ts', Position: 4, Body: 'Name this better.' };

const TOKEN_FILE = join(remoteHomeFor(REMOTE_PORT), '.crs/remote_token');
const savedToken = () => readFileSync(TOKEN_FILE, 'utf-8').trim();

/**
 * The methods of the auth calls after the first `from`, or null while the
 * bridge can't answer because it is reconnecting.
 */
async function authMethodsSince(backend: { authLog(): Promise<AuthCall[]> }, from: number) {
    try {
        return (await backend.authLog()).slice(from).map(c => c.method);
    } catch {
        return null;
    }
}

test.describe('remote mode', () => {
    test('registers with the local workflows and loads the dashboard', async ({
        page,
        backend,
    }) => {
        await page.goto('/');
        await expect(page.getByText('4 reviews tracked')).toBeVisible();

        // With no token yet, the bridge registered straight away, sending the
        // workflows and section settings from its codereviewserver.toml.
        const [first] = await backend.authLog();
        expect(first.method).toBe('RPCHandler.RegisterUser');
        expect(first.params).toEqual({
            RegistrationKey: REGISTRATION_KEY,
            Login: REMOTE_LOGIN,
            Token: '',
            ...REMOTE_USER_REGISTRATION,
        });
        expect(first.reply).toMatchObject({ okay: true, created: true, login: REMOTE_LOGIN });
        expect(first.reply.token).toMatch(/^crs_[0-9a-f]{64}$/);

        // It kept the token it was given, readable by its owner only.
        expect(savedToken()).toBe(first.reply.token);
        expect(statSync(TOKEN_FILE).mode & 0o777).toBe(0o600);
    });

    test('registers again with the same token after the server forgets the user', async ({
        page,
        backend,
    }) => {
        const token = savedToken();
        const before = (await backend.authLog()).length;

        // The retention sweep drops the user, and the server hangs up: the
        // bridge reconnects on its own and runs the connect flow again.
        await backend.rpc('E2E.ForgetUser', { Login: REMOTE_LOGIN, Disconnect: true });
        await expect
            .poll(() => authMethodsSince(backend, before))
            .toEqual(['RPCHandler.Authenticate', 'RPCHandler.RegisterUser']);

        const [auth, register] = (await backend.authLog()).slice(before);
        expect(auth.params).toEqual({ Token: token });
        expect(auth.reply).toMatchObject({ okay: false, known: false });
        expect(register.params).toEqual({
            RegistrationKey: REGISTRATION_KEY,
            Login: REMOTE_LOGIN,
            Token: token,
            ...REMOTE_USER_REGISTRATION,
        });
        expect(register.reply).toMatchObject({ okay: true, created: true, token });
        expect(savedToken()).toBe(token);

        await page.goto('/');
        await expect(page.getByText('4 reviews tracked')).toBeVisible();
    });

    test('calls refused as not authenticated wait for one re-registration, then go through', async ({
        page,
        backend,
    }) => {
        const before = (await backend.authLog()).length;
        // The connection stays up, but the server no longer knows who is on it.
        await backend.rpc('E2E.ForgetUser', { Login: REMOTE_LOGIN, Disconnect: false });

        await page.goto('/');
        await expect(page.getByText('4 reviews tracked')).toBeVisible();

        expect(await authMethodsSince(backend, before)).toEqual([
            'RPCHandler.Authenticate',
            'RPCHandler.RegisterUser',
        ]);
        // Whichever of the page's calls reached the server first was refused
        // (the list and the AI feature list load at once); the list then went
        // through on the re-registered session.
        const [first] = await backend.calls();
        expect(first.refused).toMatch(/^not authenticated: /);
        const reviews = await backend.calls('GetAllReviews');
        expect(reviews.at(-1)!.refused).toBeUndefined();
    });

    test('the server configuration says it is for administrators only', async ({
        page,
        backend,
    }) => {
        await page.goto('/');
        await page.getByTitle('Preferences').click();
        await page.getByRole('button', { name: 'Server Configuration' }).click();
        const dialog = modal(page, 'Preferences');

        await expect(
            dialog.getByText(
                'Only an administrator of this server can view or change its configuration.'
            )
        ).toBeVisible();
        // Nothing to edit, and asking again won't help.
        await expect(dialog.getByRole('button', { name: 'Save configuration' })).toHaveCount(0);
        await expect(dialog.getByRole('button', { name: 'Retry' })).toHaveCount(0);

        const [get] = await backend.calls('GetConfig');
        expect(get.refused).toBe(
            'permission denied: RPCHandler.GetConfig requires an administrator'
        );
    });

    test('a review the server refuses to post says so and keeps the draft', async ({
        page,
        backend,
    }) => {
        await openReview(page);
        await page.getByRole('button', { name: 'Submit Review', exact: true }).click();
        const dialog = modal(page, 'Submit Review');
        await dialog.getByRole('button', { name: 'Request Changes' }).click();
        await dialog.getByPlaceholder('Review Body (Optional)').fill('Needs a test.');
        await dialog.getByRole('button', { name: 'Submit', exact: true }).click();

        await expect(page.getByRole('status')).toHaveText(
            "Review on acme/widgets#42 was not submitted: this server won't post reviews under its GitHub account. Your draft is kept — submit the review on GitHub instead."
        );
        await expect(dialog.getByPlaceholder('Review Body (Optional)')).toHaveValue(
            'Needs a test.'
        );
        const [refused] = await backend.calls('SubmitReview');
        expect(refused.refused).toBe(
            "refused: this server posts to GitHub as its own account, so it does not submit reviews or merge on users' behalf; an administrator can enable AllowServerIdentityActions"
        );
        // This server cannot link accounts, so there is nothing to offer.
        await expect(modal(page, 'Link your GitHub account')).toHaveCount(0);

        // A server that lets users act under its account takes the same draft.
        await backend.rpc('E2E.SetIdentityActions', { allowed: true });
        await dialog.getByRole('button', { name: 'Submit', exact: true }).click();
        await expect(page.getByRole('status')).toHaveText('Requested changes on acme/widgets#42');
    });

    test('a submit whose connection drops may have gone out, so it reloads the PR', async ({
        page,
        backend,
    }) => {
        await backend.rpc('E2E.SetIdentityActions', { allowed: true });
        await backend.rpc('RPCHandler.AddComment', { ...PR42_ARGS, ...DRAFT });
        await openReview(page);
        await page.getByRole('button', { name: 'Submit Review', exact: true }).click();
        const dialog = modal(page, 'Submit Review');
        await dialog.getByRole('button', { name: 'Approve' }).click();
        await dialog.getByPlaceholder('Review Body (Optional)').fill('LGTM');
        // The server posts the review, then the connection drops before the
        // bridge hears back.
        await backend.dropNext('SubmitReview');
        await dialog.getByRole('button', { name: 'Submit', exact: true }).click();

        await expect(page.getByRole('status')).toHaveText(
            'Lost the connection to the server while submitting the review on acme/widgets#42: it may or may not have gone out. Check the PR before submitting again.'
        );
        // Not handed back as unsent, which would invite posting it twice:
        // the PR is reloaded once the bridge has reconnected, showing what
        // went out.
        await expect(dialog).toHaveCount(0);
        await expect.poll(async () => (await backend.calls('SyncPR')).length).toBe(1);
        await expect(page.getByTitle('Delete local comment')).toHaveCount(0);
        await page.getByRole('button', { name: /Discussion/ }).click();
        await expect(page.getByText('LGTM')).toBeVisible();
        expect(await backend.calls('SubmitReview')).toHaveLength(1);
    });

    test('a submit whose previewed drafts are gone posts nothing and says so', async ({
        page,
        backend,
    }) => {
        await backend.rpc('E2E.SetIdentityActions', { allowed: true });
        const { id } = await backend.rpc('RPCHandler.AddComment', { ...PR42_ARGS, ...DRAFT });
        await openReview(page);
        const reviews = (await backend.rpc('RPCHandler.GetPR', PR42_ARGS)).reviews.length;
        await page.getByRole('button', { name: 'Submit Review', exact: true }).click();
        const dialog = modal(page, 'Submit Review');
        await expect(dialog.getByText(DRAFT.Body)).toBeVisible();
        // The draft goes while the preview shows it: sent from another
        // client, or deleted with the user by the server's retention.
        await backend.rpc('RPCHandler.DeleteComment', { ...PR42_ARGS, ID: id });
        await dialog.getByRole('button', { name: 'Approve' }).click();
        await dialog.getByRole('button', { name: 'Submit', exact: true }).click();

        await expect(page.getByRole('status')).toHaveText(
            'Review on acme/widgets#42 was not submitted: some of its pending comments are gone — already submitted, or removed with your account after a stretch of inactivity. Check what is pending on the PR, then submit again.'
        );
        const [submit] = await backend.calls('SubmitReview');
        expect(submit.params.ExpectedDraftIDs).toEqual([id]);
        // No verdict went out without the comment, and the PR is reloaded
        // without it.
        expect((await backend.rpc('RPCHandler.GetPR', PR42_ARGS)).reviews).toHaveLength(reviews);
        await expect.poll(async () => (await backend.calls('SyncPR')).length).toBe(1);
        await expect(page.getByTitle('Delete local comment')).toHaveCount(0);
    });

    test('the submit preview names the login the bridge signed in as', async ({
        page,
        backend,
    }) => {
        await openReview(page);
        await page.getByRole('button', { name: 'Submit Review', exact: true }).click();
        const dialog = modal(page, 'Submit Review');
        await dialog.getByRole('button', { name: 'Approve' }).click();
        await expect(dialog.getByText(`Approved by ${REMOTE_LOGIN}`)).toBeVisible();
        // Not from the server's configuration, which is its administrators'.
        expect(await backend.calls('GetConfig')).toEqual([]);
    });

    test('an embedded screenshot is served from the bytes the server sends', async ({
        page,
        request,
        backend,
    }) => {
        await backend.setPRBody(PR42, `Before: <img src="${SCREENSHOT_URL}" alt="e2e screenshot">`);
        await openReview(page);

        const image = page.getByRole('img', { name: 'e2e screenshot' });
        await expect(image).toHaveAttribute(
            'src',
            `/api/github-image?url=${encodeURIComponent(SCREENSHOT_URL)}`
        );
        await expect
            .poll(() => image.evaluate(img => (img as HTMLImageElement).naturalWidth))
            .toBe(2);

        // The path the server names is on its own machine: the bridge asked for
        // the bytes instead.
        const [asked] = await backend.calls('GetImage');
        expect(asked.params).toEqual({ URL: SCREENSHOT_URL, IncludeData: true });
        const res = await request.get(
            `/api/github-image?url=${encodeURIComponent(SCREENSHOT_URL)}`
        );
        expect(res.status()).toBe(200);
        expect(res.headers()['content-type']).toBe('image/png');
        expect((await res.body()).subarray(1, 4).toString()).toBe('PNG');
    });
});
