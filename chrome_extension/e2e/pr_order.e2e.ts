// The file order the content script lays over a pull request's Files changed
// view: the per-file diffs and the file tree, both in the order the server
// stored (file-ordering's report), with files the report doesn't name left
// where GitHub put them.

import type { Page } from '@playwright/test';
import { FILE_ORDER, RERUN_FILE_ORDER } from './fixtures/data';
import { expect, test } from './harness/test';

/** A file the fixture report doesn't name: GitHub's order keeps it, so it stays last. */
const UNNAMED = ['README.md'];

/** The files the page has diffs for, in the order the page reads. */
function diffPaths(page: Page): Promise<string[]> {
    return page
        .locator('#files .file.js-file')
        .evaluateAll(files =>
            files.map(file => file.querySelector('[data-path]')?.getAttribute('data-path') ?? '')
        );
}

/** Every file tree entry's label, in the order the tree reads. */
function treeLabels(page: Page): Promise<string[]> {
    return page.locator('ul[aria-label="File Tree"] .ActionList-item-label').allTextContents();
}

/** The tree the fixture's order gives: directories ranked by their latest file. */
const ORDERED_TREE = [
    'internal',
    'api',
    'internal/api/router.go',
    'ratelimit',
    'internal/ratelimit/middleware.go',
    'internal/ratelimit/limiter.go',
    'internal/ratelimit/limiter_test.go',
    'docs',
    'docs/api.md',
    'README.md',
];

test('a PR’s Files changed view is laid out in the stored order', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42/files');

    // The order comes from one read of the server's stored result, the same
    // call the panel makes — opening a page never starts a run.
    await crs.waitForCall('GetAIOutput', {
        Owner: 'acme',
        Repo: 'widgets',
        Number: 42,
        Feature: 'file-ordering',
    });

    // The diffs, then the tree.
    await expect.poll(() => diffPaths(page)).toEqual([...FILE_ORDER, ...UNNAMED]);
    await expect.poll(() => treeLabels(page)).toEqual(ORDERED_TREE);
});

test('GitHub re-rendering the diff list brings the stored order back', async ({ github }) => {
    const page = await github.open('/acme/widgets/pull/42/files');
    await expect.poll(() => diffPaths(page)).toEqual([...FILE_ORDER, ...UNNAMED]);

    // GitHub puts its own order back when it loads the next batch of files, or
    // re-renders the page; the page is watched, so the order is re-laid.
    await page.evaluate(() => {
        for (const container of document.querySelectorAll('.js-diff-progressive-container')) {
            container.append(...Array.from(container.children).reverse());
        }
        document.dispatchEvent(new Event('turbo:load'));
    });
    await expect.poll(() => diffPaths(page)).toEqual([...FILE_ORDER, ...UNNAMED]);
});

test('a new order the server lands is laid over the open page', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42/files');
    await expect.poll(() => diffPaths(page)).toEqual([...FILE_ORDER, ...UNNAMED]);

    // file-ordering runs again for the PR (a push, say) and lands another
    // order. The service worker hears it land and tells the page, which asks
    // for the order once more; it never asks on a timer.
    crs.startServerRuns('file-ordering');
    crs.finishRuns('file-ordering');
    await expect.poll(() => diffPaths(page)).toEqual([...RERUN_FILE_ORDER, ...UNNAMED]);
    expect(crs.calls('GetAIOutput')).toHaveLength(2);
});

test('elsewhere on the PR nothing is asked for or moved', async ({ github, crs }) => {
    const page = await github.open('/acme/widgets/pull/42');

    // The content script has run — it asked for the chips — but the file order
    // belongs to the Files changed view, so nothing asked for one.
    await crs.waitForCall('ListPlugins');
    expect(await diffPaths(page)).toEqual([]);
    expect(crs.calls('GetAIOutput')).toEqual([]);
});
