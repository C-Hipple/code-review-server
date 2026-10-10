// The AI reports the panel draws itself: change-diagram's Mermaid source as
// an SVG (under the extension pages' CSP: script-src 'self', no eval), and
// file-ordering's suggested order as a list.

import { createHash } from 'node:crypto';
import { FILE_ORDER } from './fixtures/data';
import { card, expect, statusChip, test } from './harness/test';

const sha256 = (text: string) => createHash('sha256').update(text).digest('hex');

interface CspWindow {
    cspViolations: string[];
}

test('the change diagram renders as an SVG under the extension CSP', async ({ github }) => {
    const page = await github.open('/acme/widgets/pull/42');
    const errors: string[] = [];
    page.on('console', m => {
        if (m.type() === 'error') errors.push(m.text());
    });
    const panel = await github.openPanel(page);
    const diagram = card(panel, 'Change diagram');
    await expect(statusChip(diagram)).toHaveText('Success');
    await expect(diagram.locator('.card-summary')).toHaveText(
        'A diagram of what the PR changes — expand to view.'
    );

    // mermaid is imported lazily on first use, so anything the CSP blocks
    // happens from here on.
    await panel.evaluate(() => {
        const w = window as unknown as CspWindow;
        w.cspViolations = [];
        document.addEventListener('securitypolicyviolation', e =>
            w.cspViolations.push(`${e.violatedDirective} ${e.blockedURI}`)
        );
    });
    await diagram.locator('.card-toggle').click();

    const svg = diagram.locator('.diagram-canvas svg');
    await expect(svg).toBeVisible({ timeout: 20_000 });
    await expect(svg).toContainText('ratelimit.Limiter');
    await expect(svg).toContainText('legacy IP throttle');
    await expect(diagram.locator('.legend li')).toHaveText(['Added', 'Changed', 'Removed']);
    await expect(diagram.locator('.diagram-error')).toHaveCount(0);
    expect(await panel.evaluate(() => (window as unknown as CspWindow).cspViolations)).toEqual([]);
    expect(errors).toEqual([]);

    // The source is a toggle away.
    await diagram.getByRole('button', { name: 'Source', exact: true }).click();
    await expect(diagram.locator('.diagram-source')).toContainText('flowchart LR');
    await expect(svg).toHaveCount(0);
    await diagram.getByRole('button', { name: 'Source', exact: true }).click();
    await expect(svg).toBeVisible();
});

test('the file-ordering card shows the follow-up note and the suggested order', async ({
    github,
}) => {
    const page = await github.open('/acme/widgets/pull/42');
    const panel = await github.openPanel(page);
    const ordering = card(panel, 'File ordering');

    await expect(ordering.locator('.card-chips .pill')).toHaveText('Applied');
    await expect(statusChip(ordering)).toHaveText('Success');
    await expect(ordering.locator('.file-order-note')).toHaveText(
        "Applied feature — the suggested order to read this PR's files. A future version of " +
            "the extension will reorder GitHub's Files changed tab to match; for now the order is listed here."
    );

    // Open by default, in the stored order, each file linking to its diff.
    const files = ordering.locator('ol.file-order > li');
    await expect(files).toHaveText(FILE_ORDER);
    for (const [i, path] of FILE_ORDER.entries()) {
        const link = files.nth(i).locator('a');
        await expect(link).toHaveAttribute(
            'href',
            `https://github.com/acme/widgets/pull/42/files#diff-${sha256(path)}`
        );
        await expect(link).toHaveAttribute('target', '_top');
    }
});
