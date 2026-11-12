import type { Page } from '@playwright/test';
import { expect, openReview, test } from './harness/test';

// The review chat: a floating button in the bottom-left corner that opens a
// chat with a local agent when hovered. The fake backend offers one agent
// (a stand-in for Claude Code) whose turns run in the background: the reply
// is drafted, it reads pr.diff, then the reply lands, each step announced
// with a Server.ChatChanged. backend.holdRuns keeps a turn short of its reply.

const chatButton = (page: Page) =>
    page.getByRole('button', { name: 'Chat with an agent about this PR' });
const panel = (page: Page) => page.getByRole('dialog', { name: 'Review chat' });
const messageBox = (page: Page) => panel(page).getByRole('textbox', { name: 'Message' });

async function hoverOpen(page: Page) {
    await chatButton(page).hover();
    await expect(panel(page)).toBeVisible();
}

test.describe('Review chat', () => {
    test('opens on hover and closes when the pointer leaves', async ({ page }) => {
        await openReview(page);
        const button = chatButton(page);
        await expect(button).toBeVisible();
        const box = await button.boundingBox();
        expect(box!.x).toBeLessThan(40);
        expect(box!.y + box!.height).toBeGreaterThan(900 - 40);

        await expect(panel(page)).toHaveCount(0);
        await hoverOpen(page);
        await expect(panel(page)).toContainText('Ask Claude Code about this PR');

        await page.mouse.move(900, 300);
        await expect(panel(page)).toHaveCount(0);
    });

    test('stays open while the reviewer is writing', async ({ page }) => {
        await openReview(page);
        await hoverOpen(page);
        await messageBox(page).fill('Half a thought');
        await page.mouse.move(900, 300);
        await page.waitForTimeout(800);
        await expect(panel(page)).toBeVisible();

        // Escape closes it all the same.
        await messageBox(page).press('Escape');
        await expect(panel(page)).toHaveCount(0);
    });

    test('starts a chat with the picked model, and the answer streams in', async ({
        page,
        backend,
    }) => {
        await openReview(page);
        await hoverOpen(page);
        const dialog = panel(page);
        await dialog.getByRole('combobox', { name: 'Model' }).selectOption({ label: 'Opus' });

        await backend.holdRuns(true);
        await messageBox(page).fill('Is the greeting exported on purpose?');
        await messageBox(page).press('Enter');

        const starts = await backend.calls('StartChat');
        expect(starts.map(c => c.params)).toEqual([
            {
                Owner: 'acme',
                Repo: 'widgets',
                Number: 42,
                Backend: 'claude',
                Model: 'opus',
                Message: 'Is the greeting exported on purpose?',
            },
        ]);
        await expect(messageBox(page)).toHaveValue('');
        await expect(dialog).toContainText('Is the greeting exported on purpose?');
        await expect(dialog).toContainText('PR description and diff attached');
        await expect(dialog).toContainText('Claude Code · Opus');

        // The draft and the tool call arrive while the agent works; it can be stopped.
        await expect(dialog).toContainText('Reading the');
        await expect(dialog.locator('.crs-chat-tool')).toContainText('Readpr.diff');
        await expect(dialog.getByRole('button', { name: 'Stop' })).toBeVisible();
        await expect(chatButton(page).locator('.crs-chat-button-dot')).toHaveCount(1);

        await backend.holdRuns(false);
        await expect(dialog.locator('strong', { hasText: 'Fake answer' })).toBeVisible();
        await expect(dialog).toContainText('to: Is the greeting exported on purpose?');
        await expect(dialog.getByRole('button', { name: 'Send' })).toBeVisible();
        await expect(dialog).toContainText('$0.01');

        // The next message continues the same chat.
        await messageBox(page).fill('And the default?');
        await dialog.getByRole('button', { name: 'Send' }).click();
        const sends = await backend.calls('SendChatMessage');
        expect(sends.map(c => c.params)).toEqual([
            { ChatID: 'chat-1', Message: 'And the default?' },
        ]);
        await expect(dialog.locator('strong', { hasText: 'Fake answer' })).toHaveCount(2);
    });

    test('a reopened review comes back to its chat', async ({ page, backend }) => {
        await openReview(page);
        await hoverOpen(page);
        await messageBox(page).fill('Remember me');
        await messageBox(page).press('Enter');
        await expect(panel(page)).toContainText('to: Remember me');

        await page.reload();
        await expect(page.getByRole('heading', { name: 'Add greeting helper' })).toBeVisible();
        await hoverOpen(page);
        await expect(panel(page)).toContainText('to: Remember me');
        expect((await backend.calls('GetChat')).at(-1)?.params).toEqual({ ChatID: 'chat-1' });

        // A new chat leaves the old one in the history.
        await panel(page).getByRole('button', { name: 'New chat' }).click();
        await expect(panel(page)).toContainText('Ask Claude Code about this PR');
        await expect(panel(page).getByRole('combobox', { name: 'Chats on this PR' })).toContainText(
            'Remember me'
        );
    });

    test('stops the agent', async ({ page, backend }) => {
        await openReview(page);
        await hoverOpen(page);
        await backend.holdRuns(true);
        await messageBox(page).fill('Take your time');
        await messageBox(page).press('Enter');
        const stop = panel(page).getByRole('button', { name: 'Stop' });
        await expect(stop).toBeVisible();
        await stop.click();
        await expect(panel(page)).toContainText('Stopped.');
        await expect(panel(page).getByRole('button', { name: 'Send' })).toBeVisible();
        expect(await backend.calls('StopChat')).toHaveLength(1);
    });

    test('says why the agent is unavailable', async ({ page, backend }) => {
        await backend.setChatAvailable(false);
        await openReview(page);
        await hoverOpen(page);
        await expect(panel(page)).toContainText("Claude Code isn't available.");
        await expect(panel(page)).toContainText("claude isn't installed");
        await expect(messageBox(page)).toBeDisabled();
    });
});
