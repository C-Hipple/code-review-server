import type { Page } from '@playwright/test';
import { expect, modal, test } from './harness/test';

/** Opens Preferences on the Server Configuration tab, once the config has loaded. */
async function openServerConfig(page: Page) {
    await page.goto('/');
    await page.getByTitle('Preferences').click();
    await page.getByRole('button', { name: 'Server Configuration' }).click();
    const dialog = modal(page, 'Preferences');
    await expect(dialog.getByRole('button', { name: 'Save configuration' })).toBeVisible();
    return dialog;
}

test.describe('server configuration', () => {
    test('adds a plugin and switches an AI feature on', async ({ page, backend }) => {
        const dialog = await openServerConfig(page);

        await dialog.getByRole('button', { name: '+ Add plugin' }).click();
        await dialog.getByLabel('Name', { exact: true }).fill('Style Guidelines');
        await dialog.getByLabel("Command (on the server's $PATH)").fill('style_guidelines');
        await dialog.getByLabel('LLM provider').selectOption('openrouter');

        // OpenRouter has no default model, so the save is held back.
        await dialog.getByRole('button', { name: 'Save configuration' }).click();
        await expect(dialog.getByText('Fix 1 problem before saving.')).toBeVisible();
        await expect(dialog.getByText(/the openrouter provider needs a model/)).toBeVisible();
        expect(await backend.calls('UpdateConfig')).toHaveLength(0);

        await dialog
            .getByLabel('Model (required for OpenRouter)')
            .fill('anthropic/claude-sonnet-4.5');

        await dialog.getByLabel('Default command').fill('claude -p');
        await dialog.getByRole('checkbox', { name: 'Enable Comments addressed?' }).check();
        await dialog.getByText('Comments addressed?', { exact: true }).click();
        await dialog.getByLabel('Mode', { exact: true }).selectOption('agent');
        await dialog.getByLabel('Run automatically when a PR is fetched or updated').check();
        await expect(dialog.getByText('Runs on the command "claude -p".')).toBeVisible();

        await dialog.getByRole('button', { name: 'Save configuration' }).click();
        await expect(dialog.getByText(/Configuration saved to/)).toBeVisible();
        await expect(dialog.getByText('Unsaved changes')).toHaveCount(0);

        const [update] = await backend.calls('UpdateConfig');
        expect(update.params.Plugins).toEqual([
            expect.objectContaining({ Name: 'summarize', Command: 'summarize_diff' }),
            {
                Name: 'Style Guidelines',
                Command: 'style_guidelines',
                IncludeDiff: true,
                IncludeHeaders: false,
                IncludeComments: false,
                IncludeBranch: false,
                OnlyOnDemand: false,
                Provider: 'openrouter',
                Model: 'anthropic/claude-sonnet-4.5',
            },
        ]);
        expect(update.params.AI).toEqual({
            DefaultProvider: '',
            DefaultCommand: 'claude -p',
            DefaultModel: '',
        });
        expect(update.params.AIFeatures).toEqual([
            {
                ID: 'comments-addressed',
                Enabled: true,
                Mode: 'agent',
                Automatic: true,
                Provider: '',
                Command: '',
                Model: '',
            },
        ]);
    });

    test('a feature a legacy key switches on says so, and an edit takes it over', async ({
        page,
        backend,
    }) => {
        const dialog = await openServerConfig(page);

        await expect(
            dialog.getByText('review-ease · automatic · runs on the Gemini API')
        ).toBeVisible();
        await dialog.getByText('Review ease', { exact: true }).click();
        await expect(dialog.getByText(/Switched on by the legacy/)).toBeVisible();

        await dialog.getByRole('checkbox', { name: 'Enable Review ease' }).uncheck();
        await expect(dialog.getByText(/Switched on by the legacy/)).toHaveCount(0);
        await dialog.getByRole('button', { name: 'Save configuration' }).click();
        await expect(dialog.getByText(/Configuration saved to/)).toBeVisible();

        // The entry starts from what the key stood for, switched off.
        const [update] = await backend.calls('UpdateConfig');
        expect(update.params.AIFeatures).toEqual([
            {
                ID: 'review-ease',
                Enabled: false,
                Mode: '',
                Automatic: true,
                Provider: 'gemini',
                Command: '',
                Model: '',
            },
        ]);
    });

    test('switching a feature on and back off leaves nothing to save', async ({ page }) => {
        const dialog = await openServerConfig(page);
        const toggle = dialog.getByRole('checkbox', { name: 'Enable Change diagram' });

        await toggle.check();
        await expect(dialog.getByText('Unsaved changes')).toBeVisible();
        await toggle.uncheck();
        await expect(dialog.getByText('Unsaved changes')).toHaveCount(0);
        await expect(dialog.getByRole('button', { name: 'Save configuration' })).toBeDisabled();
    });
});
