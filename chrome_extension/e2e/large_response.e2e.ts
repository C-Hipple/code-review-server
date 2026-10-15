// A response over Chrome's 1 MB limit for one native message: the host splits
// it into crs_chunk messages on UTF-8 boundaries, and the service worker
// reassembles them. Exercised with GetPR for a PR whose diff is ~3 MB of
// mixed 1- to 4-byte UTF-8 and JSON escapes.

import { createHash } from 'node:crypto';
import { BIG_PR, BIG_PR_TITLE, bigDiff } from './fixtures/data';
import { expect, test } from './harness/test';
import type { RpcReply } from '../src/types';

test('a GetPR response over 1 MB arrives intact through the chunking path', async ({
    github,
    crs,
}) => {
    const page = await github.open(`/${BIG_PR.owner}/${BIG_PR.repo}/pull/${BIG_PR.number}/files`);
    const panel = await github.openPanel(page);

    // The panel's own GetPR came through: its title is multi-byte too.
    await expect(panel.getByRole('heading', { level: 1 })).toHaveText(`${BIG_PR_TITLE} #77`);
    await expect(panel.locator('.diffstat .added')).toHaveText('+70,000');

    // The host chunked it.
    const chunked = [...crs.hostLog().matchAll(/response id=\d+: (\d+) bytes in (\d+) chunks/g)];
    expect(chunked).toHaveLength(1);
    const [, bytes, chunks] = chunked[0].map(Number);
    expect(bytes).toBeGreaterThan(3_000_000);
    expect(chunks).toBe(Math.ceil(bytes / (256 * 1024)));

    // And the diff arrived byte for byte: ask again from the panel, through
    // the same service worker and host, and compare digests.
    const expected = bigDiff();
    const received = await panel.evaluate(
        async params => {
            const reply = (await chrome.runtime.sendMessage({
                type: 'crs-rpc',
                method: 'RPCHandler.GetPR',
                params,
            })) as RpcReply;
            if (!reply.ok) return { error: reply.error };
            const result = reply.result as { diff: string; metadata: { title: string } };
            const digest = await crypto.subtle.digest(
                'SHA-256',
                new TextEncoder().encode(result.diff)
            );
            return {
                length: result.diff.length,
                sha256: Array.from(new Uint8Array(digest), b =>
                    b.toString(16).padStart(2, '0')
                ).join(''),
                title: result.metadata.title,
            };
        },
        { Owner: BIG_PR.owner, Repo: BIG_PR.repo, Number: BIG_PR.number }
    );
    expect(received).toEqual({
        length: expected.length,
        sha256: createHash('sha256').update(expected).digest('hex'),
        title: BIG_PR_TITLE,
    });
    expect(crs.calls('GetPR')).toHaveLength(2);
});
