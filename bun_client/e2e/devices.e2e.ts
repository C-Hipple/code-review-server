import { expect, modal, test } from './harness/test';

test.describe('other devices', () => {
    test('shows a QR code that opens this page at a network address', async ({ page, request }) => {
        // Which addresses the bridge offers depends on the machine running the
        // suite, so the expectations come from what it reports.
        const info = await (await request.get('/api/network')).json();
        const view = '/?owner=acme&repo=widgets&number=42&view=ai';
        await page.goto(view);
        await page.getByTitle('Preferences').click();
        const dialog = modal(page, 'Preferences');
        await dialog.getByRole('button', { name: 'Other Devices' }).click();

        // The page is opened at 127.0.0.1, which no other device can use.
        await expect(dialog.getByText('127.0.0.1')).toHaveCount(0);
        if (info.addresses.length === 0) {
            await expect(dialog.getByText(/No network address to offer/)).toBeVisible();
            return;
        }
        const url = `http://${info.addresses[0].address}:${info.port}${view}`;
        await expect(dialog.getByRole('link', { name: url })).toHaveAttribute('href', url);
        await expect(dialog.locator('svg title')).toHaveText(`QR code for ${url}`);
    });
});
