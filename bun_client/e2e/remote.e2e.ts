// Remote mode: the bridge on REMOTE_PORT connects to a hosted crs over TCP
// (CRS_REMOTE) instead of spawning one, and has to authenticate first — here a
// fake crs started with --listen, which registered users outlive E2E.Reset.
// The bridge starts with no token, so it registered as it came up.

import { readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { REMOTE_LOGIN, REMOTE_USER_REGISTRATION } from './fixtures/remote_user';
import { REGISTRATION_KEY, REMOTE_PORT, remoteHomeFor, urlFor } from './harness/config';
import { type AuthCall, expect, modal, openReview, test } from './harness/test';

test.use({ baseURL: urlFor(REMOTE_PORT) });

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
        const reviews = await backend.calls('GetAllReviews');
        expect(reviews[0].refused).toMatch(/^not authenticated: /);
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
        expect(refused.refused).toMatch(/^refused: /);

        // A server that lets users act under its account takes the same draft.
        await backend.rpc('E2E.SetIdentityActions', { allowed: true });
        await dialog.getByRole('button', { name: 'Submit', exact: true }).click();
        await expect(page.getByRole('status')).toHaveText('Requested changes on acme/widgets#42');
    });
});
