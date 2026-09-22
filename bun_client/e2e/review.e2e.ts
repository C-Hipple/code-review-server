import { diffRow, expect, modal, openReview, PR42, test } from './harness/test';

test.describe('review view', () => {
    test('renders the PR header, discussion and diff', async ({ page, backend }) => {
        await openReview(page);

        await expect(page).toHaveTitle('Add greeting helper (#42)');
        await expect(page.getByText('#42', { exact: true })).toBeVisible();
        await expect(page.getByText('Closes #12.')).toBeVisible();

        // Bob's review sits in the discussion panel, which starts collapsed.
        const discussion = page.getByRole('button', { name: /Discussion/ });
        await expect(discussion).toHaveAttribute('aria-expanded', 'false');
        await discussion.click();
        await expect(page.getByText('A couple of questions.')).toBeVisible();

        // Every file in the diff gets a header row, in diff order.
        const files = page.locator('.diff-file-row');
        await expect(files).toHaveCount(3);
        await expect(files.nth(0)).toContainText('README.md');
        await expect(files.nth(1)).toContainText('src/greet.ts');
        await expect(files.nth(2)).toContainText('src/main.ts');

        await expect(diffRow(page, "console.log('hello');")).toBeVisible();
        await expect(diffRow(page, 'const message = formatGreeting(')).toBeVisible();

        const [getPR] = await backend.calls('GetPR');
        expect(getPR.params).toEqual({ Owner: 'acme', Repo: 'widgets', Number: 42 });
    });

    test('shows an existing comment thread on its diff line', async ({ page }) => {
        await openReview(page);

        const row = diffRow(page, 'punctuation: string;').first();
        await row.getByTitle('Click to show comment thread').click();

        const thread = page.locator('.hover-thread').filter({ hasText: 'bob commented' });
        await expect(thread).toContainText('Should punctuation have a default?');
        await expect(thread).toContainText('Callers always pass one today, so no.');

        await row.getByTitle('Hide comment thread').click();
        await expect(thread).toHaveCount(0);
    });

    test('adds, edits and deletes a local inline comment', async ({ page, backend }) => {
        await openReview(page);

        // Click the +/- gutter of the new `const message` line: position 4 in
        // src/main.ts counting from its first hunk header.
        await page.getByTitle('Add comment to src/main.ts:4').click();
        await expect(page.getByText('Commenting on src/main.ts:4')).toBeVisible();
        await page.getByPlaceholder('Write a comment...').fill('Inline *message* here?');
        await page.getByRole('button', { name: 'Add Comment' }).click();

        const [add] = await backend.calls('AddComment');
        expect(add.params).toMatchObject({
            Owner: 'acme',
            Repo: 'widgets',
            Number: 42,
            Filename: 'src/main.ts',
            Position: 4,
            Body: 'Inline *message* here?',
        });

        // New comments reveal their thread straight away, rendered as markdown.
        const thread = page.locator('.hover-thread').filter({ hasText: 'local commented' });
        await expect(thread.locator('em')).toHaveText('message');

        // Clicking a local thread edits it.
        await thread.click();
        await expect(page.getByText(/Editing local comment #9001/)).toBeVisible();
        await page.getByPlaceholder('Edit your comment...').fill('Inline it.');
        await page.getByRole('button', { name: 'Save Edit' }).click();
        await expect(thread).toContainText('Inline it.');
        const [edit] = await backend.calls('EditComment');
        expect(edit.params).toMatchObject({ ID: 9001, Body: 'Inline it.' });

        await thread.getByTitle('Delete local comment').click();
        await expect(thread).toHaveCount(0);
        const [del] = await backend.calls('DeleteComment');
        expect(del.params).toMatchObject({ ID: 9001 });
    });

    test('replies to a GitHub comment thread', async ({ page, backend }) => {
        await openReview(page);

        const row = diffRow(page, 'punctuation: string;').first();
        await row.getByTitle('Click to show comment thread').click();
        await page.locator('.hover-thread').filter({ hasText: 'bob commented' }).click();

        // Replies target the latest comment in the conversation.
        await expect(page.getByText('Replying to comment #5002')).toBeVisible();
        await page.getByPlaceholder('Write a reply...').fill('Fair enough.');
        await page.getByRole('button', { name: 'Reply', exact: true }).click();

        await expect(page.locator('.hover-thread')).toContainText('Fair enough.');
        const [reply] = await backend.calls('AddComment');
        expect(reply.params).toMatchObject({
            Filename: 'src/greet.ts',
            Position: 3,
            ReplyToID: 5002,
            Body: 'Fair enough.',
        });
    });

    test('cancelling an inline comment sends nothing', async ({ page, backend }) => {
        await openReview(page);

        await page.getByTitle('Add comment to src/main.ts:5').click();
        await page.getByPlaceholder('Write a comment...').fill('never mind');
        await page.getByRole('button', { name: 'Cancel' }).click();

        await expect(page.getByPlaceholder('Write a comment...')).toHaveCount(0);
        expect(await backend.calls('AddComment')).toHaveLength(0);
    });

    test('submits a review with its pending comments', async ({ page, backend }) => {
        await openReview(page);

        await page.getByTitle('Add comment to src/main.ts:4').click();
        await page.getByPlaceholder('Write a comment...').fill('Nice helper.');
        await page.getByRole('button', { name: 'Add Comment' }).click();
        await expect(page.locator('.hover-thread')).toContainText('Nice helper.');

        await page.getByRole('button', { name: 'Submit Review', exact: true }).click();
        const dialog = modal(page, 'Submit Review');
        // The preview lists the pending comment it will post.
        await expect(dialog.getByText('Nice helper.')).toBeVisible();

        await dialog.getByRole('button', { name: 'Approve' }).click();
        await dialog.getByPlaceholder('Review Body (Optional)').fill('LGTM');
        await dialog.getByRole('button', { name: 'Submit', exact: true }).click();
        await expect(dialog).toHaveCount(0);

        const [submit] = await backend.calls('SubmitReview');
        expect(submit.params).toMatchObject({
            Owner: 'acme',
            Repo: 'widgets',
            Number: 42,
            Event: 'APPROVE',
            Body: 'LGTM',
        });

        // The reply is the refreshed PR: the review is in the discussion and
        // the comment now belongs to the reviewer rather than being local.
        await page.getByRole('button', { name: /Discussion/ }).click();
        await expect(page.getByText('LGTM')).toBeVisible();
        await expect(page.locator('.hover-thread')).toContainText('e2e-reviewer commented');
        await expect(page.getByTitle('Delete local comment')).toHaveCount(0);
    });

    test('syncs and reports whether anything changed', async ({ page, backend }) => {
        await openReview(page);

        await page.getByRole('button', { name: '↻ Sync' }).click();
        await expect(page.getByText('Synced — already up to date')).toBeVisible();

        await backend.setSyncUpdated(true);
        await page.getByRole('button', { name: '↻ Sync' }).click();
        await expect(
            page.getByText('Synced — new commits, comments, or reviews pulled in')
        ).toBeVisible();

        await backend.failNext('SyncPR');
        await page.getByRole('button', { name: '↻ Sync' }).click();
        await expect(page.getByText('Sync failed — see console for details')).toBeVisible();

        expect(await backend.calls('SyncPR')).toHaveLength(3);
    });

    test('collapses and expands files', async ({ page }) => {
        await openReview(page);
        const code = diffRow(page, 'const message = formatGreeting(');

        const mainHeader = page.locator('.diff-file-row').filter({ hasText: 'src/main.ts' });
        await mainHeader.getByTitle('Collapse file').click();
        await expect(code).toHaveCount(0);
        await mainHeader.getByTitle('Expand file').click();
        await expect(code).toBeVisible();

        await page.getByRole('button', { name: '◀ Collapse All' }).click();
        await expect(diffRow(page, 'A tiny example project.')).toHaveCount(0);
        await expect(code).toHaveCount(0);
        await page.getByRole('button', { name: '▼ Expand All' }).click();
        await expect(code).toBeVisible();
    });

    test('expands hunk context from the checkout', async ({ page, backend }) => {
        await openReview(page);
        const fileOnly = page.getByText('// Greeting helpers used by the CLI entry point.');
        await expect(fileOnly).toHaveCount(0);

        // src/greet.ts's hunk starts at line 3; the two lines above it are
        // only in the file.
        const hunk = page.locator('.diff-hunk-row').filter({ hasText: '@@ -3,7 +3,8 @@' });
        await hunk.getByTitle('Expand 20 lines before this hunk').click();

        await expect(fileOnly).toBeVisible();
        await expect(
            page.locator('.diff-hunk-row').filter({ hasText: '@@ -1,9 +1,10 @@' })
        ).toBeVisible();

        const [ctx] = await backend.calls('GetHunkContext');
        expect(ctx.params).toMatchObject({
            Filename: 'src/greet.ts',
            Direction: 'before',
            AnchorLine: 3,
            NewStart: 3,
            NewLength: 8,
        });
    });

    test('adds a comment from the general comment dialog', async ({ page, backend }) => {
        await openReview(page);

        await page.getByRole('button', { name: '+ Comment' }).click();
        const dialog = modal(page, 'Add Comment');
        await dialog.getByPlaceholder('Filename').fill('README.md');
        await dialog.getByPlaceholder('Line Position in Diff').fill('5');
        await dialog.getByPlaceholder('Comment', { exact: true }).fill('Document the flag too.');
        await dialog.getByRole('button', { name: 'Add', exact: true }).click();

        await expect(dialog).toHaveCount(0);
        const [add] = await backend.calls('AddComment');
        expect(add.params).toMatchObject({
            Filename: 'README.md',
            Position: 5,
            Body: 'Document the flag too.',
        });
    });

    test('moves between PRs in list order', async ({ page }) => {
        await openReview(page);

        await page.getByRole('button', { name: 'Next PR →' }).click();
        await expect(page.getByRole('heading', { name: 'Refactor build scripts' })).toBeVisible();
        await expect(page).toHaveURL(/number=43/);

        await page.getByRole('button', { name: '← Prev PR' }).click();
        await expect(page.getByRole('heading', { name: 'Add greeting helper' })).toBeVisible();

        // Browser history walks back through the reviews too.
        await page.goBack();
        await expect(page.getByRole('heading', { name: 'Refactor build scripts' })).toBeVisible();
    });

    test('shows plugin output in the plugins panel', async ({ page }) => {
        await openReview(page);

        await page.getByRole('button', { name: /^Plugins/ }).click();
        await expect(page.getByText('summarize').first()).toBeVisible();
    });

    test('saves the review feedback draft and pre-fills the submit body', async ({
        page,
        backend,
    }) => {
        await openReview(page);

        await page.getByRole('button', { name: /Review Feedback Draft/ }).click();
        await page.getByPlaceholder(/Write your overall review feedback here/).fill('Solid work.');
        await page.getByRole('button', { name: 'Save Draft' }).click();
        await expect
            .poll(async () => (await backend.calls('SetFeedback')).map(c => c.params.Body))
            .toEqual(['Solid work.']);

        await page.getByRole('button', { name: 'Submit Review', exact: true }).click();
        await expect(
            modal(page, 'Submit Review').getByPlaceholder('Review Body (Optional)')
        ).toHaveValue('Solid work.');

        // The draft is stored server-side, so it comes back on reload.
        await page.reload();
        await page.getByRole('button', { name: /Review Feedback Draft/ }).click();
        await expect(page.getByPlaceholder(/Write your overall review feedback here/)).toHaveValue(
            'Solid work.'
        );
    });

    // Known gap: Review sets its content to "Error loading PR." when GetPR
    // fails but never renders `content`, so a PR that cannot be loaded shows
    // an empty review. Remove test.fail() once the view surfaces the error.
    test('reports a PR the backend cannot load', async ({ page }) => {
        test.fail();
        await page.goto('/?owner=acme&repo=widgets&number=999');
        await expect(page.getByText('Error loading PR.')).toBeVisible({ timeout: 3_000 });
    });
});

test.describe('preferences', () => {
    test('switching theme applies and persists it', async ({ page }) => {
        await openReview(page, PR42);

        await page.getByTitle('Preferences').click();
        const dialog = modal(page, 'Preferences');
        const themeSelect = dialog.getByRole('combobox').first();
        const current = await page.evaluate(() =>
            document.documentElement.getAttribute('data-theme')
        );
        const next = current === 'light' ? 'dark' : 'light';
        await themeSelect.selectOption(next);
        await expect(page.locator('html')).toHaveAttribute('data-theme', next);
        await dialog.getByRole('button', { name: 'Close', exact: true }).click();

        await page.reload();
        await expect(page.locator('html')).toHaveAttribute('data-theme', next);
    });

    test('server configuration tab loads the backend config', async ({ page, backend }) => {
        await page.goto('/');
        await page.getByTitle('Preferences').click();
        await page.getByRole('button', { name: 'Server Configuration' }).click();

        await expect(modal(page, 'Preferences').getByText('acme/widgets')).toBeVisible();
        expect(await backend.calls('GetConfig')).not.toHaveLength(0);
    });
});
