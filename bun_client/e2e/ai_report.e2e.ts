import { expect, modal, openReview, test } from './harness/test';

// The fake backend enables comments-addressed and serves a canned report for
// acme/widgets#42: bob's thread on src/greet.ts is unresolved, with alice's
// reply after the latest commit. A run reads "pending" for one poll first.

const button = /Comments addressed\?/;

test.describe('AI report', () => {
    test('runs comments-addressed on open, polls, and jumps to the thread', async ({
        page,
        backend,
    }) => {
        await openReview(page);

        await page.getByRole('button', { name: button }).click();
        const dialog = modal(page, 'Comments addressed?');
        await expect(dialog.getByText('1 outstanding of 1 item(s); 0 addressed.')).toBeVisible();
        await expect(dialog.getByText('Needs attention (1)')).toBeVisible();
        await expect(dialog.getByText('Should punctuation have a default?')).toBeVisible();
        await expect(
            dialog.getByText(
                'Unresolved on GitHub, and alice (the author) replied after the latest commit.'
            )
        ).toBeVisible();

        // Never run for this PR, so opening it asked for a run, then polled.
        const runs = await backend.calls('RunAIFeature');
        expect(runs.map(c => c.params)).toEqual([
            {
                Owner: 'acme',
                Repo: 'widgets',
                Number: 42,
                Feature: 'comments-addressed',
                Force: false,
            },
        ]);
        expect((await backend.calls('GetAIOutput')).length).toBeGreaterThanOrEqual(3);

        // The toolbar counts what needs attention.
        await expect(
            page.getByRole('button', { name: /Comments addressed\? \(1\)/ })
        ).toBeVisible();

        // Jumping closes the report and reveals the thread in the diff.
        await dialog.getByRole('button', { name: 'src/greet.ts:3' }).click();
        await expect(dialog).toHaveCount(0);
        await expect(
            page.locator('.hover-thread').filter({ hasText: 'Should punctuation have a default?' })
        ).toBeVisible();
    });

    test('re-run forces a fresh run', async ({ page, backend }) => {
        await openReview(page);
        await page.getByRole('button', { name: button }).click();
        const dialog = modal(page, 'Comments addressed?');
        await expect(dialog.getByText('Needs attention (1)')).toBeVisible();

        await dialog.getByRole('button', { name: '↻ Re-run' }).click();
        await expect
            .poll(async () => (await backend.calls('RunAIFeature')).map(c => c.params.Force))
            .toEqual([false, true]);
        await expect(dialog.getByText('Needs attention (1)')).toBeVisible();
    });

    test('reopening a current report does not run it again', async ({ page, backend }) => {
        await openReview(page);
        await page.getByRole('button', { name: button }).click();
        const dialog = modal(page, 'Comments addressed?');
        await expect(dialog.getByText('Needs attention (1)')).toBeVisible();
        await dialog.getByRole('button', { name: 'Close', exact: true }).click();

        await page.getByRole('button', { name: button }).click();
        await expect(
            modal(page, 'Comments addressed?').getByText('Needs attention (1)')
        ).toBeVisible();
        expect(await backend.calls('RunAIFeature')).toHaveLength(1);
    });

    test('no AI buttons when the server enables no feature', async ({ page, backend }) => {
        await backend.setAIEnabled(false);
        await openReview(page);

        await expect(page.getByRole('button', { name: '↻ Sync' })).toBeVisible();
        await expect(page.getByRole('button', { name: button })).toHaveCount(0);
        expect(await backend.calls('RunAIFeature')).toHaveLength(0);
    });
});
