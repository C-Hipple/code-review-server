import { defineConfig } from '@playwright/test';

// Drives the built extension (dist/) in Chromium through its real service
// worker and the real native host (built by global_setup.ts) against a fake
// `codereviewserver` — see e2e/README.md.
//
// Every test launches its own browser profile, so its own service worker,
// host and fake server: tests share no state and run in parallel.
export default defineConfig({
    testDir: '.',
    testMatch: '**/*.e2e.ts',
    globalSetup: './global_setup.ts',
    fullyParallel: true,
    workers: 2,
    forbidOnly: !!process.env.CI,
    retries: process.env.CI ? 1 : 0,
    timeout: 45_000,
    expect: { timeout: 10_000 },
    reporter: process.env.CI
        ? [
              ['list'],
              ['html', { open: 'never', outputFolder: `${import.meta.dirname}/playwright-report` }],
          ]
        : 'list',
    outputDir: `${import.meta.dirname}/test-results`,
    use: {
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
    },
    projects: [{ name: 'chromium' }],
});
