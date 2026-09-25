import { defineConfig, devices } from '@playwright/test';
import { LSP_PORT, NO_LSP_PORT, REAL_LSP, REAL_LSP_PORT, urlFor } from './harness/config';

// Runs the real Bun bridge (server.ts) and the built frontend against a fake
// `crs` backend and fake language servers — see e2e/README.md.
//
// One worker: every spec shares the bridges and the fake backend's in-memory
// state, which each test resets in its beforeEach.
export default defineConfig({
    testDir: '.',
    testMatch: '**/*.e2e.ts',
    fullyParallel: false,
    workers: 1,
    forbidOnly: !!process.env.CI,
    retries: process.env.CI ? 1 : 0,
    timeout: 30_000,
    expect: { timeout: 10_000 },
    reporter: process.env.CI
        ? [
              ['list'],
              ['html', { open: 'never', outputFolder: `${import.meta.dirname}/playwright-report` }],
          ]
        : 'list',
    outputDir: `${import.meta.dirname}/test-results`,
    use: {
        baseURL: urlFor(LSP_PORT),
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
    },
    projects: [
        {
            name: 'chromium',
            use: { ...devices['Desktop Chrome'], viewport: { width: 1400, height: 900 } },
        },
    ],
    webServer: [
        bridge(LSP_PORT, 'fake'),
        bridge(NO_LSP_PORT, 'none'),
        ...(REAL_LSP ? [bridge(REAL_LSP_PORT, 'real')] : []),
    ],
});

function bridge(port: number, lsp: 'fake' | 'none' | 'real') {
    return {
        command: `bun e2e/harness/start_server.ts ${port} ${lsp}`,
        cwd: '..',
        url: `${urlFor(port)}/api/check-lsp`,
        reuseExistingServer: false,
        stdout: 'pipe' as const,
        stderr: 'pipe' as const,
        timeout: 30_000,
    };
}
