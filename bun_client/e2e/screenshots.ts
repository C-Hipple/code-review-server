// Takes the web client screenshots in docs/img (the README's and the docs'),
// against the fake backend serving e2e/fixtures/demo.ts:
//
//   bun --filter frontend build    # once, the bridge serves frontend/dist
//   bun e2e/screenshots.ts [only...]
//
// With names (`list`, `review`, ...) it takes only those. The Emacs ones are
// taken by e2e/screenshots_emacs.sh. Set CHROMIUM_PATH to use a Chromium other
// than the one `bunx playwright install chromium` downloads.

import { chromium, type Page } from '@playwright/test';
import { resolve } from 'node:path';

const PORT = 5199;
const BASE = `http://localhost:${PORT}`;
const OUT = resolve(import.meta.dirname, '../../docs/img');
const PR = '/?owner=acme&repo=code-review-server&number=315';

const server = Bun.spawn(
    [
        process.execPath,
        resolve(import.meta.dirname, 'harness/start_server.ts'),
        String(PORT),
        'fake',
    ],
    { env: { ...process.env, CRS_E2E_FIXTURES: 'demo' }, stdout: 'ignore', stderr: 'inherit' }
);

async function waitForBridge() {
    for (let i = 0; i < 60; i++) {
        try {
            if ((await fetch(`${BASE}/api/prs`)).ok) return;
        } catch {
            // not up yet
        }
        await Bun.sleep(500);
    }
    throw new Error('the bridge never came up');
}

/** Runs an AI feature on the demo PR and waits for its result to land. */
async function runAI(feature: string) {
    const rpc = (method: string, params: unknown) =>
        fetch(`${BASE}/api/rpc`, {
            method: 'POST',
            headers: { 'content-type': 'application/json' },
            body: JSON.stringify({ method, params: [params] }),
        }).then(r => r.json());
    const pr = { Owner: 'acme', Repo: 'code-review-server', Number: 315 };
    await rpc('RPCHandler.RunAIFeature', { ...pr, Feature: feature });
    for (let i = 0; i < 40; i++) {
        const out = await rpc('RPCHandler.GetAIOutput', { ...pr, Feature: feature });
        if (JSON.stringify(out).includes('"status":"success"')) return;
        await Bun.sleep(250);
    }
    throw new Error(`${feature} never landed`);
}

async function settle(page: Page) {
    await page.mouse.move(1, 1);
    await page.waitForTimeout(600);
}

const shots: Record<string, (page: Page) => Promise<void>> = {
    // The review list.
    async list(page) {
        await page.setViewportSize({ width: 1440, height: 760 });
        await page.goto('/');
        await page.getByText("Report a merged PR's state as merged").waitFor();
        await settle(page);
        await page.screenshot({ path: `${OUT}/bun-client-list.png` });
    },

    // A review with the comment tray open on its first thread.
    async review(page) {
        await page.setViewportSize({ width: 1600, height: 1300 });
        await page.goto(PR);
        await page.getByTitle('Show comments in the side tray').first().click();
        await page.evaluate(() => window.scrollTo(0, 0));
        await settle(page);
        await page.screenshot({ path: `${OUT}/bun-review.png` });
    },

    // The plugins view.
    async plugins(page) {
        await page.setViewportSize({ width: 1440, height: 540 });
        await page.goto(`${PR}&view=plugins`);
        await page.getByText('No issues found.').waitFor();
        await settle(page);
        await page.screenshot({ path: `${OUT}/bun-plugins.png` });
    },

    // The comments-addressed report, pinned beside the code.
    async ai(page) {
        await runAI('comments-addressed');
        await page.setViewportSize({ width: 1600, height: 1000 });
        await page.goto(PR);
        await page.getByRole('button', { name: /Comments addressed\?/ }).click();
        await page.getByText('Needs attention (1)').waitFor();
        await settle(page);
        await page.screenshot({ path: `${OUT}/bun-ai-comments-addressed.png` });
        await page.getByRole('button', { name: /Pin beside code/ }).click();
        await page.getByTitle('Close split view').waitFor();
        await settle(page);
        await page.screenshot({ path: `${OUT}/bun-ai-pinned.png` });
    },

    // The change diagram.
    async diagram(page) {
        await runAI('change-diagram');
        await page.setViewportSize({ width: 1440, height: 900 });
        await page.goto(PR);
        await page.getByRole('button', { name: /Change diagram/ }).click();
        await page
            .locator('svg[id^="mermaid"], .mermaid svg, [class*="mermaid"] svg')
            .first()
            .waitFor();
        await page.waitForTimeout(1500);
        await settle(page);
        await page.screenshot({ path: `${OUT}/bun-change-diagram.png` });
    },

    // The review chat, after one question.
    async chat(page) {
        await page.setViewportSize({ width: 1600, height: 1000 });
        await page.goto(PR);
        await page.getByRole('button', { name: 'Chat with an agent about this PR' }).click();
        const panel = page.getByRole('dialog', { name: 'Review chat' });
        await panel
            .getByRole('textbox', { name: 'Message' })
            .fill('Is anything relying on the state being "closed" for merged PRs?');
        await panel.getByRole('button', { name: 'Send' }).click();
        await panel.getByText('Looks safe to merge').waitFor();
        await page.waitForTimeout(500);
        await page.screenshot({ path: `${OUT}/bun-review-chat.png` });
    },
};

try {
    await waitForBridge();
    // CHROMIUM_PATH: a Chromium other than the one this Playwright downloads.
    const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH });
    const only = process.argv.slice(2);
    for (const [name, shoot] of Object.entries(shots)) {
        if (only.length && !only.includes(name)) continue;
        const context = await browser.newContext({ baseURL: BASE, deviceScaleFactor: 1 });
        const page = await context.newPage();
        await shoot(page);
        await context.close();
        console.log(`took ${name}`);
    }
    await browser.close();
} finally {
    server.kill();
}
