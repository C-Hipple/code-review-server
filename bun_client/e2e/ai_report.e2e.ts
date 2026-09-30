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
        // Nor for the applied features the backend lists as enabled: their
        // results show in the diff and the list, not as a report.
        await expect(page.getByRole('button', { name: /^✦/ })).toHaveCount(0);
        expect(await backend.calls('RunAIFeature')).toHaveLength(0);
    });
});

// The full-page view the list's AI button opens: one card per enabled feature.
test.describe('AI view', () => {
    const aiView = '/?owner=acme&repo=widgets&number=42&view=ai';

    test('runs comments-addressed on open and polls', async ({ page, backend }) => {
        await page.goto(aiView);

        await expect(page).toHaveTitle('AI acme/widgets::42');
        await expect(
            page.getByRole('heading', { name: 'AI Reports for acme/widgets #42' })
        ).toBeVisible();
        await expect(page.getByText('1 outstanding of 1 item(s); 0 addressed.')).toBeVisible();
        await expect(page.getByText('Should punctuation have a default?')).toBeVisible();

        // Never run for this PR, so opening the page asked for a run, then polled.
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
        expect((await backend.calls('GetAIOutput')).length).toBeGreaterThanOrEqual(2);

        // There is no diff here to jump into, so the location is plain text.
        await expect(page.getByText('src/greet.ts:3')).toBeVisible();
        await expect(page.getByRole('button', { name: 'src/greet.ts:3' })).toHaveCount(0);
    });

    test('re-run forces a fresh run', async ({ page, backend }) => {
        await page.goto(aiView);
        await expect(page.getByText('Needs attention (1)')).toBeVisible();

        await page.getByRole('button', { name: '↻ Re-run' }).click();
        await expect
            .poll(async () => (await backend.calls('RunAIFeature')).map(c => c.params.Force))
            .toEqual([false, true]);
        await expect(page.getByText('Needs attention (1)')).toBeVisible();
    });

    test('opens the review, and comes back, without running again', async ({ page, backend }) => {
        await page.goto(aiView);
        await expect(page.getByText('Needs attention (1)')).toBeVisible();

        await page.getByRole('button', { name: 'Open review' }).click();
        await expect(page).toHaveURL(/\?owner=acme&repo=widgets&number=42$/);
        await expect(page.getByRole('heading', { name: 'Add greeting helper' })).toBeVisible();
        // The review's toolbar reads the result the AI view stored.
        await expect(
            page.getByRole('button', { name: /Comments addressed\? \(1\)/ })
        ).toBeVisible();

        await page.goBack();
        await expect(page).toHaveURL(/view=ai/);
        await expect(page.getByText('Needs attention (1)')).toBeVisible();
        expect(await backend.calls('RunAIFeature')).toHaveLength(1);
    });

    test('Escape goes back to the list', async ({ page }) => {
        await page.goto(aiView);
        await expect(page.getByText('Needs attention (1)')).toBeVisible();

        await page.keyboard.press('Escape');
        await expect(page).toHaveURL(/\/$/);
        await expect(page.getByText('4 reviews tracked')).toBeVisible();
    });

    test('says so when the server enables no feature', async ({ page, backend }) => {
        await backend.setAIEnabled(false);
        await page.goto(aiView);

        await expect(page.getByText('No AI features are enabled.')).toBeVisible();
        expect(await backend.calls('RunAIFeature')).toHaveLength(0);
    });
});

// change-diagram: the report is raw Mermaid, drawn by the mermaid library.
test.describe('Change diagram', () => {
    const button = /Change diagram/;

    test('draws the diagram in a modal that takes most of the screen', async ({
        page,
        backend,
    }) => {
        await backend.setDiagram(true);
        await openReview(page);

        await page.getByRole('button', { name: button }).click();
        const dialog = modal(page, 'Change diagram');
        const svg = dialog.locator('.mermaid-canvas svg');
        await expect(svg).toBeVisible();
        await expect(svg).toContainText('src/greet.ts: greet');
        await expect(svg).toContainText('src/main.ts: main');
        await expect(dialog.getByText('Added', { exact: true })).toBeVisible();

        // Never run for this PR, so opening it asked for a run.
        const runs = await backend.calls('RunAIFeature');
        expect(runs.map(c => c.params)).toEqual([
            { Owner: 'acme', Repo: 'widgets', Number: 42, Feature: 'change-diagram', Force: false },
        ]);

        // Most of the 1400×900 viewport.
        const box = (await dialog.boundingBox())!;
        expect(box.width).toBeGreaterThan(1300);
        expect(box.height).toBeGreaterThan(800);

        // Zoom steps from the fitted scale; 1:1 draws it at its natural size.
        const zoom = dialog.getByTestId('mermaid-zoom');
        const fitted = await zoom.textContent();
        const fittedWidth = (await svg.boundingBox())!.width;
        await dialog.getByRole('button', { name: 'Zoom out' }).click();
        await expect(zoom).not.toHaveText(fitted!);
        expect((await svg.boundingBox())!.width).toBeLessThan(fittedWidth);
        await dialog.getByRole('button', { name: '1:1' }).click();
        await expect(zoom).toHaveText('100%');

        // The raw source is a click away.
        await dialog.getByRole('button', { name: 'Source', exact: true }).click();
        await expect(dialog.getByTestId('mermaid-source')).toContainText('flowchart TD');
        await expect(svg).toHaveCount(0);
        await dialog.getByRole('button', { name: 'Diagram' }).click();
        await expect(svg).toBeVisible();

        await page.keyboard.press('Escape');
        await expect(dialog).toHaveCount(0);
    });

    test('shows the source when mermaid cannot draw it', async ({ page, backend }) => {
        await backend.setDiagram(true, 'flowchart TD\n    a[unclosed --> b');
        await openReview(page);

        await page.getByRole('button', { name: button }).click();
        const dialog = modal(page, 'Change diagram');
        await expect(dialog.getByRole('alert')).toContainText(
            'Mermaid could not draw this diagram'
        );
        await expect(dialog.getByTestId('mermaid-source')).toContainText('a[unclosed --> b');
        await expect(dialog.locator('.mermaid-canvas')).toHaveCount(0);
    });

    test('the AI view draws it inline', async ({ page, backend }) => {
        await backend.setDiagram(true);
        await page.goto('/?owner=acme&repo=widgets&number=42&view=ai');

        await expect(page.locator('.mermaid-canvas svg')).toContainText('src/greet.ts: greet');
        // Beside comments-addressed, which runs as before.
        await expect(page.getByText('Needs attention (1)')).toBeVisible();
    });
});
