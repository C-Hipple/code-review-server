// The panel follows the system's light/dark setting (prefers-color-scheme),
// not GitHub's theme. Set CRS_E2E_SCREENSHOTS to a directory to also save
// screenshots of the modal in both themes.

import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import type { Frame, Page } from '@playwright/test';
import { card, expect, statusChip, test } from './harness/test';

// The palette's --bg: #ffffff in light, #0d1117 in dark.
const THEMES = {
    light: { token: 'rgb(255, 255, 255)', body: 'rgb(255, 255, 255)', dark: false },
    dark: { token: 'rgb(13, 17, 23)', body: 'rgb(13, 17, 23)', dark: true },
} as const;

/** The --bg token as a color, the body's background, and what the panel's media query sees. */
async function colors(panel: Frame) {
    return panel.evaluate(() => {
        const probe = document.createElement('div');
        probe.style.color = 'var(--bg)';
        document.body.append(probe);
        const token = getComputedStyle(probe).color;
        probe.remove();
        return {
            token,
            body: getComputedStyle(document.body).backgroundColor,
            dark: matchMedia('(prefers-color-scheme: dark)').matches,
        };
    });
}

async function screenshot(page: Page, name: string) {
    const dir = process.env.CRS_E2E_SCREENSHOTS;
    if (!dir) return;
    mkdirSync(dir, { recursive: true });
    await page.screenshot({ path: join(dir, `${name}.png`) });
}

for (const [scheme, expected] of Object.entries(THEMES)) {
    test.describe(`with the system in ${scheme} mode`, () => {
        test.use({ colorScheme: scheme as keyof typeof THEMES });

        test(`the panel is ${scheme}`, async ({ github }) => {
            const page = await github.open('/acme/widgets/pull/42');
            const panel = await github.openPanel(page);
            await expect(statusChip(card(panel, 'Comments addressed?'))).toHaveText('Success');
            await expect(statusChip(card(panel, 'security_check'))).toHaveText('Success');
            expect(await colors(panel)).toEqual(expected);
            await screenshot(page, `modal-pr-${scheme}`);

            await panel.getByRole('button', { name: 'Back to the review list' }).click();
            await expect(panel.locator('.row-title').first()).toBeVisible();
            expect(await colors(panel)).toEqual(expected);
            await screenshot(page, `modal-list-${scheme}`);
        });
    });
}
