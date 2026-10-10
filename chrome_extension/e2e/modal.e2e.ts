// The modal the content script puts over GitHub: the ways it closes, the
// page's scroll lock, and following GitHub's soft navigation while open.

import type { Page } from '@playwright/test';
import { EXTENSION_ORIGIN } from './harness/paths';
import { expect, modalHost, PANEL_URL, test } from './harness/test';

const rootOverflow = (page: Page) => page.evaluate(() => document.documentElement.style.overflow);

interface PageWatch {
    /** Origins of the `close` messages the page received. */
    closes: string[];
    /** Keys pressed while focus was in the page itself (not the panel's iframe). */
    keys: string[];
}

/** Starts recording, from the page's own world, how the modal is asked to close. */
async function watchPage(page: Page): Promise<() => Promise<PageWatch>> {
    await page.evaluate(() => {
        const w = window as unknown as { crsWatch: PageWatch };
        w.crsWatch = { closes: [], keys: [] };
        window.addEventListener('message', e => {
            const data = e.data as { source?: string; type?: string } | null;
            if (data?.source === 'crs-panel' && data.type === 'close') {
                w.crsWatch.closes.push(e.origin);
            }
        });
        document.addEventListener('keydown', e => w.crsWatch.keys.push(e.key), true);
    });
    return () =>
        page.evaluate(() => {
            const w = window as unknown as { crsWatch: PageWatch };
            const seen = w.crsWatch;
            w.crsWatch = { closes: [], keys: [] };
            return seen;
        });
}

test('Esc, ✕ and the backdrop close the modal', async ({ github }) => {
    const page = await github.open('/acme/widgets/pull/42');
    const seen = await watchPage(page);

    // Esc inside the panel: the panel posts `close` to the page. The key goes
    // up only once the modal is gone: a keyup sent to the iframe while it is
    // being removed can leave Chromium's input dispatch waiting for good.
    let panel = await github.openPanel(page);
    await expect(panel.getByRole('heading', { level: 1 })).toBeVisible();
    expect(await rootOverflow(page)).toBe('hidden');
    await panel.locator('main.content').focus();
    await page.keyboard.down('Escape');
    await expect(modalHost(page)).toHaveCount(0);
    await page.keyboard.up('Escape');
    expect(await rootOverflow(page)).toBe('');
    expect(await seen()).toEqual({ closes: [EXTENSION_ORIGIN], keys: [] });

    // Esc with focus on the page itself: the content script's own listener.
    panel = await github.openPanel(page);
    await expect(panel.getByRole('heading', { level: 1 })).toBeVisible();
    await page.locator('#gh-search').press('Escape');
    await expect(modalHost(page)).toHaveCount(0);
    expect(await seen()).toEqual({ closes: [], keys: ['Escape'] });

    // The panel's ✕.
    panel = await github.openPanel(page);
    await panel.getByRole('button', { name: 'Close' }).click();
    await expect(modalHost(page)).toHaveCount(0);
    expect(await seen()).toEqual({ closes: [EXTENSION_ORIGIN], keys: [] });

    // The dimmed backdrop around the panel.
    panel = await github.openPanel(page);
    await expect(panel.getByRole('heading', { level: 1 })).toBeVisible();
    await page.mouse.click(5, 5);
    await expect(modalHost(page)).toHaveCount(0);
    expect(await rootOverflow(page)).toBe('');

    // And the toolbar button toggles it closed.
    await github.openPanel(page);
    await github.clickToolbarButton(page);
    await expect(modalHost(page)).toHaveCount(0);
});

test('a close message from the page itself is ignored', async ({ github }) => {
    const page = await github.open('/acme/widgets/pull/42');
    const panel = await github.openPanel(page);
    await expect(panel.getByRole('heading', { level: 1 })).toBeVisible();

    // github.com's own script posting what the panel posts: wrong origin and
    // source. The sentinel, posted after it, arrives once every listener —
    // the content script's included — has seen the forged message.
    await page.evaluate(
        () =>
            new Promise<void>(resolve => {
                window.addEventListener('message', e => {
                    if (e.data === 'sentinel') resolve();
                });
                window.postMessage({ source: 'crs-panel', type: 'close' }, '*');
                window.postMessage('sentinel', '*');
            })
    );
    await expect(modalHost(page)).toBeAttached();
});

test('soft navigation while open reloads the panel on the new page', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42');
    const panel = await github.openPanel(page);
    await expect(panel.getByRole('heading', { level: 1 })).toHaveText(
        'Rate-limit the public API per token #42'
    );

    // GitHub navigates with pushState and no reload; the content script
    // notices on its next check of location.
    await page.evaluate(() => history.pushState({}, '', '/acme/widgets/pull/57/files'));
    await expect.poll(() => panel.url()).toBe(`${PANEL_URL}?owner=acme&repo=widgets&number=57`);
    await expect(panel.getByRole('heading', { level: 1 })).toHaveText(
        'Fix off-by-one in the pagination cursor #57'
    );
    await crs.waitForCall('GetPR', { Owner: 'acme', Repo: 'widgets', Number: 57 });
    await expect(page).toHaveURL('https://github.com/acme/widgets/pull/57/files');

    // Turbo's own event, off PRs altogether: the panel goes to the list.
    await page.evaluate(() => {
        history.pushState({}, '', '/acme/widgets/pulls');
        document.dispatchEvent(new Event('turbo:load'));
    });
    await expect.poll(() => panel.url()).toBe(PANEL_URL);
    await expect(panel.locator('.crumb-current')).toHaveText('Review list');
    await expect(panel.locator('.row-title').first()).toHaveText(
        'Rate-limit the public API per token'
    );
});
