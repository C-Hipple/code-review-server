// The chips the content script draws beside a pull request's tabs: what they
// offer, and that pressing one opens the panel with that card in view.

import { card, chips, expect, modalHost, panelFrame, PANEL_URL, test } from './harness/test';

/** Every chip a PR page offers: the enabled features, then the plugins. */
const CHIP_LABELS = [
    'Comments addressed?',
    'Behind a flag?',
    'Change diagram',
    'File ordering',
    'Review ease',
    'security_check',
    'style_guidelines',
];

test('a PR page offers a chip per enabled feature and plugin, and a chip opens the panel on it', async ({
    github,
    crs,
}) => {
    const page = await github.open('/acme/widgets/pull/42/files');

    // The chips are in the tab row, right after "Files changed".
    await expect(chips(page)).toHaveText(CHIP_LABELS);
    await expect(page.locator('nav[aria-label="Pull request"] li#crs-tools')).toHaveCount(1);
    await expect(page.locator('#crs-tools .chip').last()).toHaveText('style_guidelines');

    // Drawing them costs two read-only calls, the same the panel makes.
    await crs.waitForCall('ListAIFeatures');
    await crs.waitForCall('ListPlugins');

    // A feature chip: the panel opens on the PR, with the card in view.
    await chips(page).filter({ hasText: 'Change diagram' }).click();
    await expect(modalHost(page)).toBeAttached();
    const panel = await panelFrame(page);
    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=42&focus=change-diagram`);
    const diagram = card(panel, 'Change diagram');
    await expect(diagram).toHaveClass(/card-focused/);
    await expect(diagram).toBeInViewport();
    await crs.waitForCall('GetPR', { Owner: 'acme', Repo: 'widgets', Number: 42 });
});

test('a plugin chip opens the panel on that plugin', async ({ github }) => {
    const page = await github.open('/acme/widgets/pull/42');
    await chips(page).filter({ hasText: 'security_check' }).click();

    const panel = await panelFrame(page);
    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=42&focus=security_check`);
    const security = card(panel, 'security_check');
    await expect(security).toHaveClass(/card-focused/);
    await expect(security).toBeInViewport();
    // The card the chip asked for is marked, and only that one.
    await expect(panel.locator('.card-focused')).toHaveCount(1);
});

test('away from a PR there are no chips, and GitHub’s in-page navigation re-points them', async ({
    crs,
    github,
}) => {
    const page = await github.open('/acme/widgets/pulls');
    await expect(chips(page)).toHaveCount(0);

    // Into a PR without a reload: the chips arrive for that PR.
    await page.evaluate(() => {
        history.pushState({}, '', '/acme/widgets/pull/57/files');
        document.dispatchEvent(new Event('turbo:load'));
    });
    await expect(chips(page)).toHaveText(CHIP_LABELS);

    await chips(page).filter({ hasText: 'File ordering' }).click();
    const panel = await panelFrame(page);
    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=57&focus=file-ordering`);
    await crs.waitForCall('GetPR', { Owner: 'acme', Repo: 'widgets', Number: 57 });

    // And back off PRs: the chips go with the tab row.
    await page.evaluate(() => {
        history.pushState({}, '', '/acme/widgets/issues');
        document.dispatchEvent(new Event('turbo:load'));
    });
    await expect(chips(page)).toHaveCount(0);
});
