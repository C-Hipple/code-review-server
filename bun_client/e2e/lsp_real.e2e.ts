// The review LSP flows from lsp.e2e.ts, but through the real diff-lsp and
// typescript-language-server instead of the fakes — the check that the fake's
// position contract (1-indexed tempfile lines, raw diff columns) is the one
// diff-lsp actually implements.
//
// Opt-in, since it needs both servers installed. typescript-language-server
// needs TypeScript 5 (7.x ships no tsserver.js):
//
//   cargo install --git https://github.com/C-Hipple/diff-lsp
//   npm install -g typescript-language-server typescript@5
//   CRS_E2E_REAL_LSP=1 bun run test:e2e

import type { Locator, Page } from '@playwright/test';
import { REAL_LSP, REAL_LSP_PORT, urlFor } from './harness/config';
import { clickWord, diffRow, expect, openReview, test } from './harness/test';

test.skip(!REAL_LSP, 'set CRS_E2E_REAL_LSP=1 to run against the real diff-lsp');
test.use({ baseURL: urlFor(REAL_LSP_PORT) });
test.setTimeout(90_000);

/** The LSP popover (there can be one per diff row and one per viewer) holding `text`. */
function popoverWith(page: Page, text: string | RegExp): Locator {
    return page
        .getByRole('button', { name: 'Close', exact: true })
        .locator('..')
        .filter({ hasText: text });
}

/** Any location row in a diff popover that points at formatGreeting's declaration. */
function declarationRow(page: Page): Locator {
    return page
        .locator('.lsp-location-row')
        .filter({ hasText: 'export function formatGreeting(greeting: Greeting): string {' });
}

// TypeScript loads the project in the background; until it has, queries come
// back empty and a click shows nothing, so keep clicking until one answers.
async function clickUntil(click: () => Promise<void>, answered: Locator) {
    await expect(async () => {
        if ((await answered.count()) === 0) await click();
        await expect(answered.first()).toBeVisible({ timeout: 2_000 });
    }).toPass({ timeout: 45_000 });
}

test('diff-lsp resolves a clicked diff symbol against the checkout', async ({ page }) => {
    await openReview(page);

    // Only the locations are asserted, not which heading they sit under:
    // diff-lsp (src/client.rs send_value_request) takes the first message the
    // backend writes after a request as its response without checking the
    // id, so a notification from tsserver shifts answers onto the wrong
    // request. Every answer for formatGreeting still names its declaration.
    const declaration = declarationRow(page);
    await clickUntil(
        () => clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting'),
        declaration
    );
    await expect(declaration.first()).toContainText('8');
    await expect(declaration.first().locator('mark')).toHaveText('formatGreeting');
});

test('the code viewer talks to typescript-language-server directly', async ({ page }) => {
    await openReview(page);

    const declaration = declarationRow(page);
    await clickUntil(
        () => clickWord(diffRow(page, 'const message = formatGreeting('), 'formatGreeting'),
        declaration
    );
    await declaration.first().click();

    await expect(page.getByTitle('Language server connected')).toBeVisible();
    const line8 = page.locator('[data-line="8"]');
    await expect(line8).toContainText('export function formatGreeting');

    // The `Greeting` type annotation (the second "Greeting" on the line).
    const info = popoverWith(page, 'interface Greeting');
    await clickUntil(() => clickWord(line8, 'Greeting', 1), info);
    const definition = info.getByText('Definition', { exact: true }).locator('..');
    await expect(definition.locator('.lsp-location-row')).toHaveCount(1);
    await expect(definition.locator('.lsp-location-row')).toContainText(
        'export interface Greeting {'
    );
    await expect(info.getByText(/^References( \(\d+\))?$/)).toBeVisible();
});
