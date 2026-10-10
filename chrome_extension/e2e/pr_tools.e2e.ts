// The PR tools view: what the toolbar button opens on a pull request page.

import { createHash } from 'node:crypto';
import { card, expect, PANEL_URL, statusChip, test } from './harness/test';
import { FAKE_CRS_BIN } from './harness/paths';

const PR42_ARGS = { Owner: 'acme', Repo: 'widgets', Number: 42 };

const sha256 = (text: string) => createHash('sha256').update(text).digest('hex');

test('on a PR page, the button opens the PR tools view for that PR', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42/files');
    const panel = await github.openPanel(page);

    expect(panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=42`);
    await expect(panel.locator('.crumb-current')).toHaveText('acme/widgets#42');
    await expect(panel.getByRole('heading', { level: 1 })).toHaveText(
        'Rate-limit the public API per token #42'
    );
    await expect(panel.locator('.summary-meta')).toHaveText(
        'mona wants to merge mona/token-rate-limits into main'
    );
    await expect(panel.locator('.diffstat .added')).toHaveText('+412');
    await expect(panel.locator('.diffstat .removed')).toHaveText('−58');
    await expect(panel.locator('.summary-stats > .muted')).toHaveText('7 files changed');
    await expect(panel.locator('.summary-stats .pill')).toHaveText('Hard to review');

    // GetPR for this PR, first: it fills the server's caches and starts the
    // PR's post-update hooks. The outputs are read only once it has answered.
    const getPR = await crs.waitForCall('GetPR', PR42_ARGS);
    expect(getPR.params).toEqual({ ...PR42_ARGS, SkipCache: false });
    await crs.waitForCall('GetAIOutput', PR42_ARGS);
    await crs.waitForCall('GetPluginOutput', PR42_ARGS);
    const methods = crs.calls().map(c => c.method);
    expect(methods.indexOf('RPCHandler.GetPR')).toBeLessThan(
        methods.indexOf('RPCHandler.GetAIOutput')
    );
    expect(methods.indexOf('RPCHandler.GetPR')).toBeLessThan(
        methods.indexOf('RPCHandler.GetPluginOutput')
    );
    expect(crs.calls('ListAIFeatures')).toHaveLength(1);
    expect(crs.calls('ListPlugins')).toHaveLength(1);

    // AI features: a card per enabled report feature plus file-ordering;
    // review-ease is the summary's pill, and the disabled one is named.
    const ai = panel.locator('section[aria-labelledby="ai-heading"]');
    await expect(ai.locator('.card-title')).toHaveText([
        'Comments addressed?',
        'Behind a flag?',
        'Change diagram',
        'File ordering',
    ]);
    await expect(ai.locator('.section-header .counter')).toHaveText('4');
    await expect(statusChip(card(panel, 'Comments addressed?'))).toHaveText('Success');
    await expect(statusChip(card(panel, 'Behind a flag?'))).toHaveText('Not run');
    await expect(statusChip(card(panel, 'Change diagram'))).toHaveText('Success');
    await expect(statusChip(card(panel, 'File ordering'))).toHaveText('Success');
    await expect(
        card(panel, 'Comments addressed?').getByRole('button', {
            name: 'Re-run Comments addressed?',
        })
    ).toBeEnabled();
    await expect(
        card(panel, 'Behind a flag?').getByRole('button', { name: 'Run Behind a flag?' })
    ).toBeEnabled();
    await expect(card(panel, 'Comments addressed?').locator('.card-summary')).toHaveText(
        '1 of 4 review threads is still outstanding.'
    );
    await expect(ai.locator('.disabled-line')).toHaveText(
        'Not enabled: Test gaps — turn them on with [[AIFeatures]] in the server config.'
    );

    // Plugins, with their statuses.
    const plugins = panel.locator('section[aria-labelledby="plugins-heading"]');
    await expect(plugins.locator('.card-title')).toHaveText(['security_check', 'style_guidelines']);
    const security = card(panel, 'security_check');
    await expect(statusChip(security)).toHaveText('Success');
    await expect(security.locator('.card-chips .pill')).toHaveText('3 annotations');
    await expect(security.getByRole('button', { name: 'Re-run security_check' })).toBeEnabled();
    const style = card(panel, 'style_guidelines');
    await expect(statusChip(style)).toHaveText('On demand');
    await expect(style.locator('.card-summary')).toHaveText(
        'Runs on demand: press Run to run it for this PR.'
    );

    // A plugin's body and annotations, each linking to its line in the diff.
    await security.locator('.card-toggle').click();
    await expect(security.locator('.markdown')).toContainText('Found 2 issues worth a look');
    const first = security.locator('.annotation').first();
    await expect(first.locator('.annotation-loc')).toHaveText('internal/api/router.go:31');
    await expect(first.locator('.annotation-loc')).toHaveAttribute(
        'href',
        `https://github.com/acme/widgets/pull/42/files#diff-${sha256('internal/api/router.go')}R31`
    );

    // Everything went through the real host to the fake server.
    await expect(panel.locator('.connection-label')).toHaveText('Connected');
    await expect(panel.locator('.connection')).toHaveAttribute(
        'title',
        new RegExp(`^Connected to ${FAKE_CRS_BIN} \\(0\\.1\\.0`)
    );
    expect(crs.hostLog()).toContain('request "RPCHandler.GetPR"');
});

test('Back goes to the review list, which marks the tab’s PR', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42');
    const panel = await github.openPanel(page);
    await expect(panel.getByRole('heading', { level: 1 })).toHaveText(
        'Rate-limit the public API per token #42'
    );
    expect(crs.calls('GetAllReviews')).toHaveLength(0);

    await panel.getByRole('button', { name: 'Back to the review list' }).click();
    await expect(panel.locator('.crumb-current')).toHaveText('Review list');
    const current = panel.locator('li.row-current');
    await expect(current).toHaveCount(1);
    await expect(current.locator('.row-meta')).toContainText('acme/widgets#42');
    await expect(current.locator('.pill', { hasText: 'This tab' })).toBeVisible();
    await crs.waitForCall('GetAllReviews');

    // And on to another PR's tools from the list, without leaving the page.
    await panel
        .getByRole('button', { name: 'AI features and plugins for acme/widgets#57' })
        .click();
    await expect(panel.getByRole('heading', { level: 1 })).toHaveText(
        'Fix off-by-one in the pagination cursor #57'
    );
    await crs.waitForCall('GetPR', { Owner: 'acme', Repo: 'widgets', Number: 57 });
    await expect(statusChip(card(panel, 'Comments addressed?'))).toHaveText('Not run');
    await expect(statusChip(card(panel, 'security_check'))).toHaveText('Not run yet');
    expect(page.url()).toBe('https://github.com/acme/widgets/pull/42');
});
