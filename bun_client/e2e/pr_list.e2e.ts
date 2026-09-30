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

    test('narrows by review ease', async ({ page }) => {
        await page.goto('/');
        const nav = page.getByRole('navigation', { name: 'Filter by state' });
        const level = (label: string) =>
            page.getByRole('checkbox', { name: new RegExp(`^${label}`) });

        await expect(
            page
                .locator('.crs-row')
                .filter({ hasText: 'Add greeting helper' })
                .locator('.crs-ease-pill')
        ).toHaveText('EASY');
        // Easiest first, each level with its open-PR count: the medium PR is a draft.
        await expect(
            page.locator('.crs-facet-item').filter({ has: page.locator('.crs-ease-pill') })
        ).toHaveText([/EASY\s*1/, /MEDIUM\s*0/, /HARD\s*1/]);

        await level('EASY').check();
        await expect(page.getByText('1 of 4')).toBeVisible();
        await expect(page.getByText('Add greeting helper')).toBeVisible();
        await expect(page.getByText('Fix gadget overflow')).toHaveCount(0);
        // Like the other narrowing, it applies before the state counts.
        await expect(nav.getByRole('button', { name: /Open\s*1/ })).toBeVisible();
        await expect(nav.getByRole('button', { name: /Draft\s*0/ })).toBeVisible();

        // Levels OR together, and a PR with no rating matches none of them.
        await level('MEDIUM').check();
        await level('HARD').check();
        await nav.getByRole('button', { name: /Everything/ }).click();
        await expect(page.getByText('3 of 4')).toBeVisible();
        await expect(page.getByText('Bump dependencies')).toHaveCount(0);

        // Clearing the filters clears the levels too.
        await page.getByRole('textbox', { name: 'Filter reviews' }).fill('no such pr');
        await page.getByRole('button', { name: 'Clear filters' }).click();
        await expect(page.getByText('4 of 4')).toBeVisible();
        await expect(level('EASY')).not.toBeChecked();
    });

    test('offers no review-ease filter while no PR is rated', async ({ page, backend }) => {
        await backend.setReviewEase(false);
        await page.goto('/');

        await expect(page.getByText('Add greeting helper')).toBeVisible();
        await expect(page.locator('.crs-ease-pill')).toHaveCount(0);
        await expect(page.getByText('Review ease', { exact: true })).toHaveCount(0);
    });

    test('keeps a checked review-ease level after the ratings go', async ({ page, backend }) => {
        await page.goto('/');
        const easy = page.getByRole('checkbox', { name: /^EASY/ });
        await easy.check();
        await expect(page.getByText('1 of 4')).toBeVisible();

        // Nothing is rated now, so nothing matches, but the level stays to be unchecked.
        await backend.setReviewEase(false);
        await page.getByRole('button', { name: 'Refresh' }).click();
        await expect(page.getByText(/No\s+Open\s+PRs match the current filters/)).toBeVisible();
        await expect(easy).toBeChecked();
        // A click, not uncheck(): unchecking takes the whole list, checkbox and all.
        await easy.click();
        await expect(page.getByText('2 of 4')).toBeVisible();
        await expect(page.getByText('Review ease', { exact: true })).toHaveCount(0);
    });

    test('shows how many comments each PR has', async ({ page }) => {
        await page.goto('/');

        const discussed = page.locator('.crs-row').filter({ hasText: 'Add greeting helper' });
        await expect(discussed.getByRole('img', { name: '2 comments' })).toHaveText('2');

        // Like GitHub's list, a PR nobody has commented on shows no count.
        const quiet = page.locator('.crs-row').filter({ hasText: 'Fix gadget overflow' });
        await expect(quiet).toBeVisible();
        await expect(quiet.getByRole('img', { name: /comment/ })).toHaveCount(0);
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

        // Each plugin's output collapses from its name.
        await page.getByRole('button', { name: /^summarize/ }).click();
        await expect(page.getByRole('heading', { name: 'Summary' })).toHaveCount(0);
    });

    test('opens AI reports from a row', async ({ page }) => {
        await page.goto('/');
        await page
            .locator('.crs-row')
            .filter({ hasText: 'Add greeting helper' })
            .getByRole('button', { name: 'AI', exact: true })
            .click();

        await expect(page).toHaveURL(/view=ai/);
        await expect(page).toHaveTitle('AI acme/widgets::42');
        await expect(page.getByRole('heading', { name: 'Comments addressed?' })).toBeVisible();
        await expect(page.getByText('1 outstanding of 1 item(s); 0 addressed.')).toBeVisible();
    });

    test('offers no AI button when the server enables no feature', async ({ page, backend }) => {
        await backend.setAIEnabled(false);
        await page.goto('/');
        const row = page.locator('.crs-row').filter({ hasText: 'Add greeting helper' });

        await expect(row.getByRole('button', { name: 'Plugins' })).toBeAttached();
        await expect
            .poll(async () => (await backend.calls('ListAIFeatures')).length)
            .toBeGreaterThan(0);
        await expect(row.getByRole('button', { name: 'AI', exact: true })).toHaveCount(0);
    });
});
