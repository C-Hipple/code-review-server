// Run / Re-run: AI features through RunAIFeature, plugins through
// RerunPlugins, each reading `pending` until the fake lets the run land
// (crs.finishRuns), which the panel picks up by polling.

import { card, expect, statusChip, test } from './harness/test';

const PR42_ARGS = { Owner: 'acme', Repo: 'widgets', Number: 42 };

test('Run, then Re-run, an AI feature', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42');
    const panel = await github.openPanel(page);
    const flags = card(panel, 'Behind a flag?');
    await expect(statusChip(flags)).toHaveText('Not run');

    // Nothing stored yet: Run, which doesn't force a run.
    await flags.getByRole('button', { name: 'Run Behind a flag?' }).click();
    const run = await crs.waitForCall('RunAIFeature', { Feature: 'feature-flags' });
    expect(run.params).toEqual({ ...PR42_ARGS, Feature: 'feature-flags', Force: false });
    await expect(statusChip(flags)).toHaveText('Running');
    await expect(flags.getByRole('button', { name: 'Running Behind a flag?' })).toBeDisabled();

    crs.finishRuns('feature-flags');
    await expect(statusChip(flags)).toHaveText('Success');
    await expect(flags.locator('.card-summary')).toHaveText(
        'Run 1: feature-flags found 2 of 5 logic changes outside a flag.'
    );

    // A current result on screen: Re-run, forced.
    crs.finishRuns();
    await flags.getByRole('button', { name: 'Re-run Behind a flag?' }).click();
    await expect.poll(() => crs.calls('RunAIFeature').length).toBe(2);
    expect(crs.calls('RunAIFeature')[1].params).toEqual({
        ...PR42_ARGS,
        Feature: 'feature-flags',
        Force: true,
    });
    await expect(statusChip(flags)).toHaveText('Running');
    // The previous result stays readable while the new run finishes.
    await flags.locator('.card-toggle').click();
    await expect(flags.locator('.previous-note')).toHaveText(
        'Showing the previous result while the new run finishes.'
    );

    crs.finishRuns('feature-flags');
    await expect(statusChip(flags)).toHaveText('Success');
    await expect(flags.locator('.markdown')).toHaveText(
        'Run 2: feature-flags found 2 of 5 logic changes outside a flag.'
    );
    await expect(flags.locator('.previous-note')).toHaveCount(0);
});

test('Run an on-demand plugin, then Re-run all', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42');
    const panel = await github.openPanel(page);
    const style = card(panel, 'style_guidelines');
    const security = card(panel, 'security_check');
    await expect(statusChip(style)).toHaveText('On demand');

    // One plugin: RerunPlugins naming it.
    await style.getByRole('button', { name: 'Run style_guidelines' }).click();
    const rerun = await crs.waitForCall('RerunPlugins');
    expect(rerun.params).toEqual({ ...PR42_ARGS, Plugins: ['style_guidelines'] });
    await expect(statusChip(style)).toHaveText('Running');
    await expect(statusChip(security)).toHaveText('Success');

    crs.finishRuns('style_guidelines');
    await expect(statusChip(style)).toHaveText('Success');

    // Its HTML body renders in a sandboxed frame, where its script never ran.
    await style.locator('.card-toggle').click();
    const frame = style.locator('iframe.html-body');
    await expect(frame).toHaveAttribute(
        'sandbox',
        'allow-same-origin allow-popups allow-popups-to-escape-sandbox'
    );
    const doc = frame.contentFrame();
    await expect(doc.getByRole('heading', { name: 'Style guide findings (run 1)' })).toBeVisible();
    await expect(doc.locator('body')).not.toContainText('SCRIPT RAN');

    // Re-run all names the automatic plugins: the on-demand one keeps the
    // result it was asked for, where RerunPlugins without names would clear it.
    crs.finishRuns();
    await panel.getByRole('button', { name: 'Re-run all' }).click();
    await expect.poll(() => crs.calls('RerunPlugins').length).toBe(2);
    expect(crs.calls('RerunPlugins')[1].params).toEqual({
        ...PR42_ARGS,
        Plugins: ['security_check'],
    });
    await expect(statusChip(security)).toHaveText('Running');
    await expect(statusChip(style)).toHaveText('Success');

    crs.finishRuns('security_check');
    await expect(statusChip(security)).toHaveText('Success');
    await expect(security.locator('.card-summary')).toHaveText(
        'Run 1 of security_check: no new findings.'
    );
    await expect(doc.getByRole('heading', { name: 'Style guide findings (run 1)' })).toBeVisible();
});
