import type { Page } from '@playwright/test';
import { diffRow, expect, openReview, PR42, test } from './harness/test';

// When a GitHub webhook delivery changes a PR, the server sends a
// Server.PRUpdated notification, which the bridge relays to the page over
// /api/events. backend.updatePR changes the fake's fixture and sends one.

/** The page hears updates over its event stream; a heartbeat means it's open. */
async function connected(page: Page) {
    await expect(page.getByRole('img', { name: 'Server connected' })).toBeVisible();
}

/** The toast, which is the page's only status role. */
const toast = (page: Page) => page.getByRole('status');

test.describe('live updates', () => {
    test('the review refreshes in place and says what changed', async ({ page, backend }) => {
        await openReview(page);
        await connected(page);

        // Context expanded by hand survives a refresh that leaves the diff be.
        const fileOnly = page.getByText('// Greeting helpers used by the CLI entry point.');
        await page
            .locator('.diff-hunk-row')
            .filter({ hasText: '@@ -3,7 +3,8 @@' })
            .getByTitle('Expand 20 lines before this hunk')
            .click();
        await expect(fileOnly).toBeVisible();

        await backend.updatePR(PR42, {
            Comment: { author: 'carol', body: 'Ship it once CI is green.' },
            Actor: 'carol',
            Summary: 'carol commented',
        });

        await expect(toast(page)).toHaveText('carol commented');
        await page.getByRole('button', { name: /Discussion/ }).click();
        await expect(page.getByText('Ship it once CI is green.')).toBeVisible();
        await expect(fileOnly).toBeVisible();

        // Fetched again from the server's caches, not synced with GitHub.
        expect(await backend.calls('GetPR')).toHaveLength(2);
        expect(await backend.calls('SyncPR')).toHaveLength(0);
    });

    test("the user's own change refreshes without a toast", async ({ page, backend }) => {
        await openReview(page);
        await connected(page);

        await backend.updatePR(PR42, {
            Title: 'Add a greeting helper',
            Event: 'pull_request.edited',
            Changes: ['pr', 'metadata'],
            Actor: 'e2e-reviewer',
            Own: true,
            Summary: 'e2e-reviewer edited the title',
        });

        await expect(page.getByRole('heading', { name: 'Add a greeting helper' })).toBeVisible();
        await expect(toast(page)).toHaveCount(0);
    });

    test('a new diff waits for the comment being written', async ({ page, backend }) => {
        await openReview(page);
        await connected(page);

        await page.getByTitle('Add comment to src/main.ts:5').click();
        const draft = page.getByPlaceholder('Write a comment...');
        await draft.fill('half-written');

        await backend.updatePR(PR42, {
            ReplaceInDiff: ["console.log('hello');", "console.log('hello, world');"],
            Event: 'pull_request.synchronize',
            Changes: ['pr', 'head'],
            Actor: 'alice',
            Summary: 'alice pushed new commits',
        });

        await expect(toast(page)).toContainText('alice pushed new commits');
        await expect(toast(page)).toContainText('It will refresh once you finish your comment.');
        await expect(diffRow(page, "console.log('hello');")).toBeVisible();
        await expect(draft).toHaveValue('half-written');

        await page.getByRole('button', { name: 'Cancel' }).click();
        await expect(diffRow(page, "console.log('hello, world');")).toBeVisible();
        await expect(diffRow(page, "console.log('hello');")).toHaveCount(0);
        await expect(toast(page)).toHaveText('Refreshed with the latest changes');
    });

    test('a change to the list rows alone leaves the review be', async ({ page, backend }) => {
        await openReview(page);
        await connected(page);

        await backend.updatePR(PR42, { Event: 'sections', Changes: ['sections'] });
        // Longer than a batch collects for, so the next update can't join it.
        await page.waitForTimeout(1000);
        await backend.updatePR(PR42, {
            Comment: { author: 'dave', body: 'One more thing.' },
            Actor: 'dave',
            Summary: 'dave commented',
        });

        await expect(toast(page)).toHaveText('dave commented');
        expect(await backend.calls('GetPR')).toHaveLength(2);
    });

    test('the list refreshes a changed row and highlights it', async ({ page, backend }) => {
        await page.goto('/');
        await expect(page.getByText('Add greeting helper')).toBeVisible();
        await connected(page);

        await backend.updatePR(PR42, {
            Title: 'Add a greeting helper',
            Event: 'pull_request.edited',
            Changes: ['pr', 'metadata'],
            Actor: 'alice',
            Summary: 'alice edited the title',
        });

        await expect(toast(page)).toHaveText('widgets#42: alice edited the title');
        const row = page.locator('.crs-row').filter({ hasText: 'Add a greeting helper' });
        await expect(row).toHaveClass(/crs-row-updated/);
        await expect(page.getByText('Loading reviews…')).toHaveCount(0);
        expect(await backend.calls('GetAllReviews')).toHaveLength(2);

        // The highlight is a moment's.
        await expect(row).not.toHaveClass(/crs-row-updated/);
    });
});
