import { expect, test } from './harness/test';

// The bridge subscribes to the backend's notifications at startup and relays
// them to the page over /api/events. The e2e bridges ask for a heartbeat every
// second (start_server.ts), so the page takes the server for gone after three
// silent seconds.

test.describe('Server heartbeat', () => {
    test('shows the server connected while heartbeats arrive', async ({ page }) => {
        await page.goto('/');
        await expect(page.getByRole('img', { name: 'Server connected' })).toBeVisible();
        await expect(page.getByRole('img', { name: 'Server not responding' })).toHaveCount(0);
    });

    test('warns when heartbeats stop, and recovers when they resume', async ({ page, backend }) => {
        await page.goto('/');
        await expect(page.getByRole('img', { name: 'Server connected' })).toBeVisible();

        await backend.setHeartbeat(false);
        const warning = page.getByRole('img', { name: 'Server not responding' });
        await expect(warning).toBeVisible();
        await expect(warning).toHaveText('Server not responding');

        await backend.setHeartbeat(true);
        await expect(page.getByRole('img', { name: 'Server connected' })).toBeVisible();
        await expect(warning).toHaveCount(0);
    });

    test('relays the backend heartbeat as a Server-Sent Event', async ({ page }) => {
        await page.goto('/');
        const heartbeat = await page.evaluate(
            () =>
                new Promise<unknown>((resolve, reject) => {
                    const source = new EventSource('/api/events');
                    const timer = setTimeout(() => reject(new Error('no heartbeat')), 5000);
                    source.addEventListener('Server.Heartbeat', event => {
                        clearTimeout(timer);
                        source.close();
                        resolve(JSON.parse((event as MessageEvent<string>).data));
                    });
                })
        );
        expect(heartbeat).toMatchObject({
            seq: expect.any(Number),
            time: expect.any(String),
            started_at: expect.any(String),
            interval_seconds: 1,
        });
    });
});
