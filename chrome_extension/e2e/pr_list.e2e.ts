// The review list: what the toolbar button opens on a GitHub page that is not
// a PR (and, in a popup window, anywhere else).

import { SECTIONS_IN_ORDER } from './fixtures/data';
import { expect, modalHost, PANEL_URL, test } from './harness/test';

test('on a GitHub page that is not a PR, the button opens the modal on the review list', async ({
    github,
    crs,
}) => {
    const page = await github.open('/acme/widgets');
    const panel = await github.openPanel(page);

    // No PR in the iframe's query: the panel starts on the list.
    expect(panel.url()).toBe(PANEL_URL);
    await expect(panel.locator('.crumb-current')).toHaveText('Review list');
    await expect(panel.locator('.list-summary')).toHaveText('5 PRs in 3 sections');

    // Sections by section_priority (the server sent the merged PR first);
    // rows in the server's order within a section.
    await expect(panel.locator('.group-name')).toHaveText(SECTIONS_IN_ORDER);
    await expect(
        panel.getByRole('region', { name: 'Needs my review' }).locator('.row-title')
    ).toHaveText([
        'Rate-limit the public API per token',
        'Fix off-by-one in the pagination cursor',
        'WIP: dark mode for the settings page',
    ]);
    await expect(
        panel.getByRole('region', { name: 'Needs my review' }).locator('.counter')
    ).toHaveText('3');

    // Each row: owner/repo#number, author, tags and comment count.
    const row = (ref: string) => panel.locator('li.row', { hasText: ref });
    await expect(row('acme/widgets#42').locator('.row-meta')).toContainText(
        'acme/widgets#42 · mona'
    );
    await expect(row('acme/widgets#42').locator('.pill')).toHaveText(['Conflict', 'hard']);
    await expect(row('acme/widgets#42').locator('.row-comments')).toHaveText('7');
    await expect(row('acme/widgets#57').locator('.pill')).toHaveText(['easy']);
    await expect(row('acme/hooks#211').locator('.pill')).toHaveText(['medium']);
    await expect(row('acme/web#880').locator('.pill')).toHaveText(['Draft']);
    await expect(row('acme/widgets#39').locator('.pill')).toHaveText(['Merged']);
    await expect(row('acme/web#880').locator('.row-comments')).toHaveCount(0);

    // Not on a PR, so no row is this tab's.
    await expect(panel.locator('li.row-current')).toHaveCount(0);
    expect(crs.calls('GetAllReviews')).toHaveLength(1);
});

test('clicking a PR in the list navigates the GitHub tab to it', async ({ github }) => {
    const page = await github.open('/acme/widgets/pulls');
    const panel = await github.openPanel(page);

    const link = panel.getByRole('link', { name: /Fix off-by-one in the pagination cursor/ });
    await expect(link).toHaveAttribute('href', 'https://github.com/acme/widgets/pull/57');
    await expect(link).toHaveAttribute('target', '_top');
    await link.click();

    // A top-level navigation from inside the extension's iframe: the page
    // itself goes to the PR, and the modal goes with the old document.
    await expect(page).toHaveURL('https://github.com/acme/widgets/pull/57');
    await expect(page.locator('#gh-title')).toHaveText('Pull request acme/widgets#57');
    await expect(modalHost(page)).toHaveCount(0);
});

test('an empty review list says how to fill it', async ({ github, crs }) => {
    crs.update({ reviews: 'empty' });
    const page = await github.open('/');
    const panel = await github.openPanel(page);

    await expect(panel.getByText('No PRs in your review list.')).toBeVisible();
    await expect(panel.locator('.list-summary')).toHaveText('0 PRs in 0 sections');
});

test('on a page that is not GitHub, the button opens the panel in a popup window', async ({
    context,
    github,
}) => {
    const blank = context.pages()[0];
    const popupOpened = context.waitForEvent('page');
    await github.clickToolbarButton(blank);
    const popup = await popupOpened;

    await expect(popup).toHaveURL(`${PANEL_URL}?standalone=1`);
    await expect(popup.locator('.row-title').first()).toHaveText(
        'Rate-limit the public API per token'
    );
    // Standalone: no ✕, and PR links open new tabs instead of navigating.
    await expect(popup.getByRole('button', { name: 'Close' })).toHaveCount(0);
    await expect(
        popup.getByRole('link', { name: /Fix off-by-one in the pagination cursor/ })
    ).toHaveAttribute('target', '_blank');
});
