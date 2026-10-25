// Linking a GitHub account, on the remote bridge: the fake crs refuses a
// submit with `refused: link-github:` until the user links their account,
// hands out a device-flow code, and ends the link when a test says GitHub
// would have (backend.endGitHubLink). Every test starts unlinked, with
// linking switched off until it calls backend.setGitHubLinkAvailable.

import type { Page } from '@playwright/test';
import { REMOTE_LOGIN } from './fixtures/remote_user';
import { REMOTE_PORT, urlFor } from './harness/config';
import { expect, modal, openReview, PR42, test } from './harness/test';

test.use({ baseURL: urlFor(REMOTE_PORT) });

const LINK_DIALOG = 'Link your GitHub account';

/** Submit the open review with `event` and `body` from the Submit Review modal. */
async function submitReview(page: Page, event: string, body: string) {
    await page.getByRole('button', { name: 'Submit Review', exact: true }).click();
    const dialog = modal(page, 'Submit Review');
    await dialog.getByRole('button', { name: event }).click();
    await dialog.getByPlaceholder('Review Body (Optional)').fill(body);
    await dialog.getByRole('button', { name: 'Submit', exact: true }).click();
}

/** The code the link dialog shows, once the server has issued one. */
async function shownCode(page: Page) {
    const code = modal(page, LINK_DIALOG).getByLabel('GitHub user code');
    await expect(code).toHaveText(/^[A-Z]{4}-[A-Z]{4}$/);
    return (await code.textContent()) ?? '';
}

test.describe('linking a GitHub account', () => {
    test('a refused submit offers a link, and submits the kept draft once linked', async ({
        page,
        context,
        backend,
    }) => {
        await context.grantPermissions(['clipboard-read', 'clipboard-write']);
        await backend.setGitHubLinkAvailable(true);
        // A pending comment, which the refused submit must leave alone.
        await backend.rpc('RPCHandler.AddComment', {
            Owner: 'acme',
            Repo: 'widgets',
            Number: 42,
            Filename: 'src/main.ts',
            Position: 4,
            Body: 'Name this better.',
        });
        await openReview(page);
        await submitReview(page, 'Request Changes', 'Needs a test.');

        // The server needs the reviewer's own account: the dialog says why
        // and offers to link it, rather than a toast saying it failed.
        const dialog = modal(page, LINK_DIALOG);
        await expect(
            dialog.getByText(
                'Your review on acme/widgets#42 was not submitted: link your GitHub account to submit reviews as yourself. Your draft is kept.'
            )
        ).toBeVisible();
        const [refused] = await backend.calls('SubmitReview');
        expect(refused.refused).toBe(
            'refused: link-github: link your GitHub account to submit reviews as yourself'
        );

        await dialog.getByRole('button', { name: 'Link GitHub account' }).click();
        const code = await shownCode(page);
        expect((await backend.gitHubLink(REMOTE_LOGIN)).flow).toMatchObject({
            status: 'pending',
            user_code: code,
        });
        const open = dialog.getByRole('link', { name: 'Open github.com/login/device ↗' });
        await expect(open).toHaveAttribute('href', 'https://github.com/login/device');
        await expect(open).toHaveAttribute('target', '_blank');
        await expect(dialog.getByText(`Sign in to GitHub as ${REMOTE_LOGIN}`)).toBeVisible();

        await dialog.getByRole('button', { name: 'Copy' }).click();
        await expect(dialog.getByRole('button', { name: '✓ Copied' })).toBeVisible();
        expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(code);

        // The user authorizes on GitHub; the next poll sees it.
        await expect(dialog.getByText('Waiting for GitHub…')).toBeVisible();
        await backend.endGitHubLink(REMOTE_LOGIN, 'linked');
        await expect(dialog.getByText(`Linked to GitHub as ${REMOTE_LOGIN}`)).toBeVisible();
        expect((await backend.calls('GetGitHubLink')).length).toBeGreaterThan(0);

        await dialog.getByRole('button', { name: 'Submit again' }).click();
        await expect(page.getByRole('status')).toHaveText('Requested changes on acme/widgets#42');

        // The same draft went out, this time as the reviewer, pending comment
        // and all.
        const [first, second] = await backend.calls('SubmitReview');
        expect(second.params).toEqual(first.params);
        expect(second.params).toMatchObject({ Event: 'REQUEST_CHANGES', Body: 'Needs a test.' });
        expect(second.refused).toBeUndefined();
        const pr = await backend.rpc('RPCHandler.GetPR', {
            Owner: PR42.owner,
            Repo: PR42.repo,
            Number: PR42.number,
        });
        expect(pr.reviews.at(-1)).toMatchObject({ user: REMOTE_LOGIN, body: 'Needs a test.' });
        expect(
            pr.comments.find((c: { body: string }) => c.body === 'Name this better.')
        ).toMatchObject({ author: REMOTE_LOGIN });

        // The header says the account is linked now.
        await expect(
            page.getByRole('button', { name: `Account: ${REMOTE_LOGIN}, GitHub account linked` })
        ).toBeVisible();
    });

    test('a declined link says so and offers another go; closing returns to the draft', async ({
        page,
        backend,
    }) => {
        await backend.setGitHubLinkAvailable(true);
        await openReview(page);
        await submitReview(page, 'Approve', 'LGTM');

        const dialog = modal(page, LINK_DIALOG);
        await dialog.getByRole('button', { name: 'Link GitHub account' }).click();
        await shownCode(page);
        await backend.endGitHubLink(REMOTE_LOGIN, 'denied');
        await expect(dialog.getByText('The authorization was declined on GitHub')).toBeVisible();
        await expect(dialog.getByRole('button', { name: 'Submit again' })).toHaveCount(0);

        // Another go is another code.
        await dialog.getByRole('button', { name: 'Try again' }).click();
        await shownCode(page);
        expect(await backend.calls('StartGitHubLink')).toHaveLength(2);

        // Closing stops the polling, and hands back the draft.
        await dialog.getByRole('button', { name: 'Cancel' }).click();
        await expect(dialog).toHaveCount(0);
        const submit = modal(page, 'Submit Review');
        await expect(submit.getByPlaceholder('Review Body (Optional)')).toHaveValue('LGTM');
        const polls = (await backend.calls('GetGitHubLink')).length;
        await page.waitForTimeout(2500);
        expect(await backend.calls('GetGitHubLink')).toHaveLength(polls);
        expect(await backend.calls('SubmitReview')).toHaveLength(1);
    });

    test('a link GitHub has since revoked offers to link again', async ({ page, backend }) => {
        await backend.setGitHubLinkAvailable(true);
        await backend.linkGitHub(REMOTE_LOGIN);
        await backend.breakGitHubLink(REMOTE_LOGIN, 'revoked');
        await openReview(page);
        await submitReview(page, 'Comment', 'One question.');

        await expect(
            modal(page, LINK_DIALOG).getByText(
                'Your review on acme/widgets#42 was not submitted: your GitHub authorization has expired or was revoked; link your account again. Your draft is kept.'
            )
        ).toBeVisible();
        // The server cleared the link it could no longer use.
        expect((await backend.gitHubLink(REMOTE_LOGIN)).link).toBeNull();
    });

    test('the account button shows the login and link state, links and unlinks', async ({
        page,
        backend,
    }) => {
        await backend.setGitHubLinkAvailable(true);
        await page.goto('/');
        const notLinked = page.getByRole('button', {
            name: `Account: ${REMOTE_LOGIN}, GitHub account not linked`,
        });
        await expect(notLinked).toHaveText(REMOTE_LOGIN);
        await notLinked.click();

        const prefs = modal(page, 'Preferences');
        await expect(prefs.getByText(REMOTE_LOGIN, { exact: true })).toBeVisible();
        await expect(
            prefs.getByText('Not linked. Link it so your reviews go to GitHub as you.')
        ).toBeVisible();

        // Linking from here starts straight away: there is no refusal to explain.
        await prefs.getByRole('button', { name: 'Link GitHub account' }).click();
        await shownCode(page);
        await backend.endGitHubLink(REMOTE_LOGIN, 'linked');
        const dialog = modal(page, LINK_DIALOG);
        await expect(dialog.getByText(`Linked to GitHub as ${REMOTE_LOGIN}`)).toBeVisible();
        await dialog.getByRole('button', { name: 'Done' }).click();
        await expect(
            prefs.getByText(`Linked as ${REMOTE_LOGIN}. Your reviews go to GitHub as you.`)
        ).toBeVisible();
        await expect(
            page.getByRole('button', { name: `Account: ${REMOTE_LOGIN}, GitHub account linked` })
        ).toBeVisible();

        // Unlinking asks first.
        await prefs.getByRole('button', { name: 'Unlink' }).click();
        await expect(prefs.getByText('Unlink it?')).toBeVisible();
        await prefs.getByRole('button', { name: 'Unlink' }).click();
        await expect(prefs.getByText(/^Unlinked your GitHub account/)).toBeVisible();
        await expect(
            prefs.getByText('Not linked. Link it so your reviews go to GitHub as you.')
        ).toBeVisible();
        await expect(notLinked).toBeVisible();
        expect(await backend.calls('UnlinkGitHub')).toHaveLength(1);
        expect((await backend.gitHubLink(REMOTE_LOGIN)).link).toBeNull();
    });

    test('on a server that cannot link, the account tab says so', async ({ page }) => {
        await page.goto('/');
        await page
            .getByRole('button', {
                name: `Account: ${REMOTE_LOGIN}, GitHub accounts cannot be linked on this server`,
            })
            .click();
        const prefs = modal(page, 'Preferences');
        await expect(prefs.getByText("This server can't link GitHub accounts.")).toBeVisible();
        await expect(
            prefs.getByText(
                'This server has no GitHubOAuthClientID configured, so GitHub accounts cannot be linked'
            )
        ).toBeVisible();
        await expect(prefs.getByRole('button', { name: 'Link GitHub account' })).toHaveCount(0);
    });
});
