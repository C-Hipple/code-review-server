// Connection-level failures, which take over the panel with what to do:
// the server dying under the host, the host not finding the server, and
// Chrome finding no host (or one that doesn't allow this extension).

import { mkdirSync } from 'node:fs';
import { join } from 'node:path';
import { E2E_ROOT } from './harness/paths';
import { expect, EXTENSION_ID, test } from './harness/test';

test('the server exiting shows the server error card with the host’s message', async ({
    github,
    crs,
}) => {
    crs.update({
        exitOn: {
            method: 'RPCHandler.GetAllReviews',
            code: 3,
            stderr: 'fatal: opening the review database: database is locked',
        },
    });
    const page = await github.open('/acme/widgets');
    const panel = await github.openPanel(page);

    // The host reports server_exited with the exit status and the server's
    // last output, then exits; the call in flight fails with it.
    const error = panel.locator('.error-panel');
    await expect(error.getByRole('heading')).toHaveText("The server isn't running");
    await expect(error.locator('.error-message')).toContainText('exit status 3');
    await expect(error.locator('.error-message')).toContainText(
        'fatal: opening the review database: database is locked'
    );
    await expect(error).toContainText(`The native host's log has the details: ${crs.logPath}`);
    await expect(panel.locator('.connection-label')).toHaveText('Disconnected');
    expect(crs.hostLog()).toContain('server exited unexpectedly: exit status 3');

    // Retry connects again: a new host, and a server that stays up.
    crs.update({ exitOn: undefined });
    await error.getByRole('button', { name: 'Retry' }).click();
    await expect(panel.locator('.row-title').first()).toHaveText(
        'Rate-limit the public API per token'
    );
    await expect(panel.locator('.connection-label')).toHaveText('Connected');
    expect(crs.calls('GetAllReviews')).toHaveLength(2);
});

test.describe('with no codereviewserver anywhere the host looks', () => {
    // An empty HOME (no ~/go/bin) and an env file pointing everything else
    // at nothing.
    const home = join(E2E_ROOT, 'empty-home');
    test.beforeAll(() => mkdirSync(home, { recursive: true }));
    test.use({
        browserEnv: { HOME: home },
        hostEnv: {
            CRS_SERVER_PATH: '/nonexistent/codereviewserver',
            PATH: '/nonexistent/bin',
            GOBIN: '',
            GOPATH: '',
        },
    });

    test('the server error card lists where the host looked', async ({ github, crs }) => {
        const page = await github.open('/acme/widgets/pull/42');
        const panel = await github.openPanel(page);

        const error = panel.locator('.error-panel');
        await expect(error.getByRole('heading')).toHaveText("The server isn't running");
        const message = error.locator('.error-message');
        await expect(message).toContainText(
            'codereviewserver not found; tried $CRS_SERVER_PATH=/nonexistent/codereviewserver (not an executable file), codereviewserver on PATH (/nonexistent/bin)'
        );
        await expect(message).toContainText(`${home}/go/bin/codereviewserver`);
        await expect(message).toContainText('Install it with `go install ./...`');
        await expect(error).toContainText(crs.logPath);
        expect(crs.calls()).toEqual([]);
    });
});

test.describe('with no native host installed', () => {
    test.use({ hostInstall: 'missing' });

    test('the host-missing card names install.sh and the extension id', async ({ github, crs }) => {
        const page = await github.open('/acme/widgets');
        const panel = await github.openPanel(page);

        const error = panel.locator('.error-panel');
        await expect(error.getByRole('heading')).toHaveText(
            'Connect the extension to code-review-server'
        );
        await expect(error.locator('ol.steps li').first()).toContainText(
            'In your code-review-server checkout, run chrome_extension/crs_native_host/install.sh.'
        );
        await expect(error).toContainText(`Extension id: ${EXTENSION_ID}`);
        await expect(panel.locator('.connection-label')).toHaveText('Disconnected');
        expect(crs.calls()).toEqual([]);
    });
});

test.describe('with a native host that allows another extension', () => {
    test.use({ hostInstall: 'forbidden' });

    test('the card says how to allow this extension', async ({ github, crs }) => {
        const page = await github.open('/acme/widgets/pull/42');
        const panel = await github.openPanel(page);

        const error = panel.locator('.error-panel');
        await expect(error.getByRole('heading')).toHaveText(
            "The native host doesn't allow this extension"
        );
        await expect(error).toContainText(
            `chrome_extension/crs_native_host/install.sh --extension-id ${EXTENSION_ID}`
        );
        expect(crs.calls()).toEqual([]);
    });
});
