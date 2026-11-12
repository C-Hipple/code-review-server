// The "Review tools" button the content script draws beside a pull request's
// tabs: what its menu offers, and that picking a tool opens the panel with that
// card in view.

import {
    card,
    chips,
    expect,
    modalHost,
    openTools,
    panelFrame,
    PANEL_URL,
    test,
    toolsButton,
} from './harness/test';

/** Every tool a PR page offers: the enabled features, then the plugins. */
const CHIP_LABELS = [
    'Comments addressed?',
    'Behind a flag?',
    'Change diagram',
    'File ordering',
    'Review ease',
    'security_check',
    'style_guidelines',
];

test('a PR page offers one button whose menu lists each enabled feature and plugin, and a pick opens the panel on it', async ({
    github,
    crs,
}) => {
    const page = await github.open('/acme/widgets/pull/42/files');

    // One button in the tab row, right after "Files changed", counting the tools.
    await expect(toolsButton(page)).toHaveCount(1);
    await expect(page.locator('nav[aria-label="Pull request"] li#crs-tools')).toHaveCount(1);
    await expect(toolsButton(page)).toContainText(`Review tools${CHIP_LABELS.length}`);
    // The menu is closed until asked for.
    await expect(chips(page).first()).toBeHidden();

    // Drawing it costs two read-only calls, the same the panel makes.
    await crs.waitForCall('ListAIFeatures');
    await crs.waitForCall('ListPlugins');

    await openTools(page);
    await expect(chips(page)).toHaveText(CHIP_LABELS);
    await expect(toolsButton(page)).toHaveAttribute('aria-expanded', 'true');
    // Esc closes it, as GitHub's own menus do.
    await page.keyboard.press('Escape');
    await expect(chips(page).first()).toBeHidden();
    await expect(modalHost(page)).toHaveCount(0);

    // A feature: the panel opens on the PR, with the card in view.
    await openTools(page);
    await chips(page).filter({ hasText: 'Change diagram' }).click();
    await expect(modalHost(page)).toBeAttached();
    const panel = await panelFrame(page);
    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=42&focus=change-diagram`);
    const diagram = card(panel, 'Change diagram');
    await expect(diagram).toHaveClass(/card-focused/);
    await expect(diagram).toBeInViewport();
    await crs.waitForCall('GetPR', { Owner: 'acme', Repo: 'widgets', Number: 42 });
});

test('a plugin opens the panel on that plugin', async ({ github }) => {
    const page = await github.open('/acme/widgets/pull/42');
    await openTools(page);
    await chips(page).filter({ hasText: 'security_check' }).click();

    const panel = await panelFrame(page);
    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=42&focus=security_check`);
    const security = card(panel, 'security_check');
    await expect(security).toHaveClass(/card-focused/);
    await expect(security).toBeInViewport();
    // The card the pick asked for is marked, and only that one.
    await expect(panel.locator('.card-focused')).toHaveCount(1);
});

test('"Open panel" opens the panel on the PR with nothing singled out', async ({ github }) => {
    const page = await github.open('/acme/widgets/pull/42');
    await openTools(page);
    await page.locator('#crs-tools').locator('.item.panel').click();

    const panel = await panelFrame(page);
    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=42`);
});

test('away from a PR there is no button, and GitHub’s in-page navigation re-points it', async ({
    crs,
    github,
}) => {
    const page = await github.open('/acme/widgets/pulls');
    await expect(toolsButton(page)).toHaveCount(0);

    // Into a PR without a reload: GitHub swaps the page in, and the button
    // arrives for that PR.
    await github.navigate(page, '/acme/widgets/pull/57/files');
    await openTools(page);
    await expect(chips(page)).toHaveText(CHIP_LABELS);

    await chips(page).filter({ hasText: 'File ordering' }).click();
    const panel = await panelFrame(page);
    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=57&focus=file-ordering`);
    await crs.waitForCall('GetPR', { Owner: 'acme', Repo: 'widgets', Number: 57 });

    // And back off PRs: the button goes with the tab row.
    await github.navigate(page, '/acme/widgets/issues');
    await expect(toolsButton(page)).toHaveCount(0);
});
