import { expect, test } from './harness/test';

test.describe('PR list', () => {
    test('loads tracked reviews grouped by section', async ({ page, backend }) => {
        await page.goto('/');

        await expect(page).toHaveTitle('Code Review');
        await expect(page.getByText('4 reviews tracked')).toBeVisible();

        // Each test gets a fresh browser context, so the list opens on its
        // default "Open" bucket: two open PRs in two sections.
        const nav = page.getByRole('navigation', { name: 'Filter by state' });
        await expect(nav.getByRole('button', { name: /Open\s*2/ })).toHaveAttribute(
            'aria-current',
            'true'
        );
        await expect(nav.getByRole('button', { name: /Draft\s*1/ })).toBeVisible();
        await expect(nav.getByRole('button', { name: /Merged\s*1/ })).toBeVisible();
        await expect(nav.getByRole('button', { name: /Everything\s*4/ })).toBeVisible();

        await expect(page.getByRole('button', { name: /Needs Review\s*1/ })).toBeVisible();
        await expect(page.getByRole('button', { name: /My PRs\s*1/ })).toBeVisible();
        await expect(page.getByText('Add greeting helper')).toBeVisible();
        await expect(page.getByText('Fix gadget overflow')).toBeVisible();
        await expect(page.getByText('Refactor build scripts')).toHaveCount(0);
        await expect(page.getByText('2 of 4')).toBeVisible();

        // The list warms plugin output for every PR it shows.
        await expect
            .poll(async () => (await backend.calls('GetPluginOutput')).length)
            .toBeGreaterThanOrEqual(4);
    });

    test('filters by lifecycle state', async ({ page }) => {
        await page.goto('/');
        const nav = page.getByRole('navigation', { name: 'Filter by state' });

        await nav.getByRole('button', { name: /Draft/ }).click();
        await expect(page.getByText('Refactor build scripts')).toBeVisible();
        await expect(page.getByText('Add greeting helper')).toHaveCount(0);

        await nav.getByRole('button', { name: /Merged/ }).click();
        await expect(page.getByText('Bump dependencies')).toBeVisible();
        await expect(page.getByRole('button', { name: /Recently Merged/ })).toBeVisible();

        await nav.getByRole('button', { name: /Everything/ }).click();
        await expect(page.getByText('4 of 4')).toBeVisible();

        // The choice survives a reload.
        await page.reload();
        await expect(nav.getByRole('button', { name: /Everything/ })).toHaveAttribute(
            'aria-current',
            'true'
        );
    });

    test('narrows by text, repo and author', async ({ page }) => {
        await page.goto('/');
        await page
            .getByRole('navigation', { name: 'Filter by state' })
            .getByRole('button', { name: /Everything/ })
            .click();

        const search = page.getByRole('textbox', { name: 'Filter reviews' });
        await search.fill('gadget');
        await expect(page.getByText('1 of 4')).toBeVisible();
        await expect(page.getByText('Fix gadget overflow')).toBeVisible();

        await search.fill('no such pr');
        await expect(page.getByText(/No\s+PRs match the current filters/)).toBeVisible();
        await page.getByRole('button', { name: 'Clear filters' }).click();
        await expect(page.getByText('4 of 4')).toBeVisible();

        await page.getByRole('combobox', { name: 'Filter by repo' }).selectOption('gadgets');
        await expect(page.getByText('1 of 4')).toBeVisible();
        await page.getByRole('combobox', { name: 'Filter by repo' }).selectOption('');

        await page.getByRole('combobox', { name: 'Filter by author' }).selectOption('dave');
        await expect(page.getByText('Refactor build scripts')).toBeVisible();
        await expect(page.getByText('1 of 4')).toBeVisible();
    });

    test('collapses and expands sections', async ({ page }) => {
        await page.goto('/');
        const section = page.getByRole('button', { name: /Needs Review/ });

        await section.click();
        await expect(section).toHaveAttribute('aria-expanded', 'false');
        await expect(page.getByText('Add greeting helper')).toHaveCount(0);

        await section.click();
        await expect(page.getByText('Add greeting helper')).toBeVisible();

        await page.getByRole('button', { name: 'Collapse all sections' }).click();
        await expect(page.getByText('Add greeting helper')).toHaveCount(0);
        await expect(page.getByText('Fix gadget overflow')).toHaveCount(0);
        await page.getByRole('button', { name: 'Expand all sections' }).click();
        await expect(page.getByText('Fix gadget overflow')).toBeVisible();
    });

    test('shows an error with a working retry when the backend fails', async ({
        page,
        backend,
    }) => {
        await backend.failNext('GetAllReviews', 'database is locked');
        await page.goto('/');

        await expect(page.getByText("Couldn't load reviews.")).toBeVisible();
        await page.getByRole('button', { name: 'Retry' }).click();
        await expect(page.getByText('Add greeting helper')).toBeVisible();
    });

    test('opens a review from a row', async ({ page }) => {
        await page.goto('/');
        await page.getByText('Add greeting helper').click();

        await expect(page).toHaveURL(/\?owner=acme&repo=widgets&number=42$/);
        await expect(page.getByRole('heading', { name: 'Add greeting helper' })).toBeVisible();

        await page.getByRole('button', { name: '← Back to List' }).click();
        await expect(page).toHaveURL(/\/$/);
        await expect(page.getByText('4 reviews tracked')).toBeVisible();
    });

    test('opens a review from a pasted GitHub URL', async ({ page }) => {
        await page.goto('/');
        await page
            .getByRole('textbox', { name: 'Filter reviews' })
            .fill('https://github.com/acme/gadgets/pull/7');

        await expect(page).toHaveURL(/owner=acme&repo=gadgets&number=7/);
        await expect(page.getByRole('heading', { name: 'Fix gadget overflow' })).toBeVisible();
    });

    test('opens a review by number', async ({ page }) => {
        await page.goto('/');
        await page.getByText('Open a PR by number').click();
        await page.getByRole('textbox', { name: 'Owner' }).fill('acme');
        await page.getByRole('textbox', { name: 'Repo' }).fill('widgets');
        await page.getByRole('spinbutton', { name: 'PR number' }).fill('43');
        await page.getByRole('button', { name: 'Open PR' }).click();

        await expect(page.getByRole('heading', { name: 'Refactor build scripts' })).toBeVisible();
    });

    test('opens plugin output from a row', async ({ page }) => {
        await page.goto('/');
        await page
            .locator('.crs-row')
            .filter({ hasText: 'Add greeting helper' })
            .getByRole('button', { name: 'Plugins' })
            .click();

        await expect(page).toHaveURL(/view=plugins/);
        await expect(page.getByRole('heading', { name: 'Summary' })).toBeVisible();
        await expect(page.getByText('greeting helper', { exact: true })).toBeVisible();
    });
});
