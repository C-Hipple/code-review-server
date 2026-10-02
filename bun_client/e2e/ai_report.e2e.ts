import { readFileSync } from 'node:fs';
import type { Page } from '@playwright/test';
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
        // The open thread shows the code it was left on.
        await expect(page.getByTestId('ai-item-code')).toContainText('+    punctuation: string;');

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

        const downloading = page.waitForEvent('download');
        await page.getByRole('button', { name: 'Download image' }).click();
        expect((await downloading).suggestedFilename()).toBe('acme-widgets-42-change-diagram.png');
    });
});

// Copy image and Download image: a PNG of the diagram as drawn.
test.describe('Change diagram image', () => {
    const button = /Change diagram/;
    // The fills of the fixture's added and changed nodes, and of the legend's
    // swatches for them, which are far smaller.
    const ADDED = '#dcfce7';
    const CHANGED = '#fef3c7';
    // The fixture's diagram with a non-breaking space in a label, which
    // mermaid writes as &nbsp; — no entity in the XML an SVG image is read as.
    const NBSP_SOURCE = `flowchart TD
    main["src/main.ts: main"]:::changed --> greet["src/greet.ts: greet"]:::added
    greet --> punct["src/greet.ts:&nbsp;punctuation"]:::added
    classDef added fill:${ADDED},stroke:#16a34a,color:#14532d
    classDef changed fill:${CHANGED},stroke:#d97706,color:#78350f`;

    async function openDiagram(page: Page) {
        await openReview(page);
        await page.getByRole('button', { name: button }).click();
        const dialog = modal(page, 'Change diagram');
        await expect(dialog.locator('.mermaid-canvas svg')).toBeVisible();
        return dialog;
    }

    async function downloadImage(page: Page, dialog: ReturnType<typeof modal>) {
        const downloading = page.waitForEvent('download');
        await dialog.getByRole('button', { name: 'Download image' }).click();
        return downloading;
    }

    test('downloads and copies the diagram as a PNG', async ({ page, backend, context }) => {
        await context.grantPermissions(['clipboard-read', 'clipboard-write']);
        await backend.setDiagram(true, NBSP_SOURCE);
        const dialog = await openDiagram(page);
        await expect(dialog.locator('.mermaid-canvas svg')).toContainText(
            'src/greet.ts: punctuation'
        );
        const viewBox = await dialog.locator('.mermaid-canvas svg').getAttribute('viewBox');
        const [, , width, height] = viewBox!.split(/[\s,]+/).map(Number);
        const background = await page.evaluate(() =>
            getComputedStyle(document.documentElement).getPropertyValue('--bg-primary').trim()
        );

        // Named for the PR: the diagram at twice its size, on the app's
        // background, with the legend below it.
        const download = await downloadImage(page, dialog);
        expect(download.suggestedFilename()).toBe('acme-widgets-42-change-diagram.png');
        const saved = await readPng(page, readFileSync(await download.path()), [ADDED, CHANGED]);
        expect(saved.width).toBeGreaterThanOrEqual(2 * width);
        expect(saved.height).toBeGreaterThan(2 * height);
        expect(saved.corner).toBe(background.toLowerCase());
        // The nodes were drawn, not just the legend.
        expect(saved.counts[ADDED]).toBeGreaterThan(5000);
        expect(saved.counts[CHANGED]).toBeGreaterThan(2000);

        await dialog.getByRole('button', { name: 'Copy image' }).click();
        await expect(dialog.getByRole('button', { name: '✓ Copied' })).toBeVisible();
        const copied = await page.evaluate(async () => {
            const [item] = await navigator.clipboard.read();
            const png = await item.getType('image/png');
            return Array.from(new Uint8Array(await png.arrayBuffer()));
        });
        expect(await readPng(page, Buffer.from(copied), [ADDED, CHANGED])).toEqual(saved);
        await expect(dialog.getByRole('alert')).toHaveCount(0);
    });

    test("draws the labels as SVG text where the browser won't read back HTML ones", async ({
        page,
        backend,
    }) => {
        // Safari won't let a canvas be read back once an SVG with a
        // <foreignObject> (mermaid's HTML labels) has been drawn on it.
        // Chromium won't either when the SVG comes from a blob: URL, so this
        // routes the image's data: URLs through blob: URLs, noting whether
        // each SVG had a <foreignObject>.
        await page.addInitScript(() => {
            const src = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'src')!;
            const prefix = 'data:image/svg+xml;charset=utf-8,';
            const drawn: boolean[] = [];
            Object.assign(window, { drawnSvgs: drawn });
            Object.defineProperty(HTMLImageElement.prototype, 'src', {
                ...src,
                set(this: HTMLImageElement, url: string) {
                    if (url.startsWith(prefix)) {
                        const svg = decodeURIComponent(url.slice(prefix.length));
                        drawn.push(svg.includes('<foreignObject'));
                        url = URL.createObjectURL(new Blob([svg], { type: 'image/svg+xml' }));
                    }
                    src.set!.call(this, url);
                },
            });
        });
        await backend.setDiagram(true);
        const dialog = await openDiagram(page);
        const drawnSvgs = () =>
            page.evaluate(() => (window as unknown as { drawnSvgs: boolean[] }).drawnSvgs);
        expect(await drawnSvgs()).toEqual([]);

        const download = await downloadImage(page, dialog);
        const saved = await readPng(page, readFileSync(await download.path()), [ADDED]);
        expect(saved.counts[ADDED]).toBeGreaterThan(5000);
        // The drawing with HTML labels was refused, so it was drawn again without.
        expect(await drawnSvgs()).toEqual([true, false]);
        await expect(dialog.getByRole('alert')).toHaveCount(0);
    });

    test("says so when the browser can't copy an image", async ({ page, backend }) => {
        // As in a browser without ClipboardItem.
        await page.addInitScript(() => Reflect.deleteProperty(window, 'ClipboardItem'));
        await backend.setDiagram(true);
        const dialog = await openDiagram(page);

        await dialog.getByRole('button', { name: 'Copy image' }).click();
        await expect(dialog.getByRole('alert')).toHaveText(
            "Could not copy the image: this browser can't copy an image here; use Download image instead"
        );

        // Gone once an image is made.
        await downloadImage(page, dialog);
        await expect(dialog.getByRole('alert')).toHaveCount(0);
    });
});

/**
 * A PNG's size, its top left pixel's color, and how many of its pixels are
 * each of `colors` (#rrggbb), as the browser decodes it.
 */
async function readPng(page: Page, png: Buffer, colors: string[]) {
    return page.evaluate(
        async ({ base64, colors }) => {
            const bytes = Uint8Array.from(atob(base64), c => c.charCodeAt(0));
            const bitmap = await createImageBitmap(new Blob([bytes], { type: 'image/png' }));
            const ctx = new OffscreenCanvas(bitmap.width, bitmap.height).getContext('2d')!;
            ctx.drawImage(bitmap, 0, 0);
            const { data } = ctx.getImageData(0, 0, bitmap.width, bitmap.height);
            const hex = (i: number) =>
                '#' +
                [data[i], data[i + 1], data[i + 2]]
                    .map(v => v.toString(16).padStart(2, '0'))
                    .join('');
            const counts: Record<string, number> = Object.fromEntries(colors.map(c => [c, 0]));
            for (let i = 0; i < data.length; i += 4) {
                const color = hex(i);
                if (color in counts) counts[color]++;
            }
            return { width: bitmap.width, height: bitmap.height, corner: hex(0), counts };
        },
        { base64: png.toString('base64'), colors }
    );
}
