import { afterEach, describe, expect, mock, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { getFilteredDiff, listDiffFilters } from './api';
import FilteredDiffBanner from './components/review/FilteredDiffBanner';
import type { FilteredDiffResult } from './diff_filter_utils';

const ready: FilteredDiffResult = {
    filter: 'since-last-review',
    status: 'ready',
    head_sha: 'ffffffffff',
    base_sha: 'aaaaaaaaaa',
    baseline: {
        sha: 'aaaaaaaaaa',
        at: '2026-10-01T12:00:00Z',
        kind: 'review',
        detail: 'your approving review',
        inferred: false,
    },
    diff: 'diff --git a/x b/x\n',
    commits: ['ffffffffff'],
    message: '',
    computed_at: '2026-10-01T12:05:00Z',
};

describe('FilteredDiffBanner', () => {
    test('says what the diff below covers and offers the way back', () => {
        const html = renderToStaticMarkup(
            <FilteredDiffBanner
                result={ready}
                filterName="Since my last review"
                onShowFullDiff={() => {}}
            />
        );
        expect(html).toContain('aria-live="polite"');
        expect(html).toContain('Since my last review: changes since your approving review');
        expect(html).toContain('Show full diff');
    });
});

describe('filtered diff RPCs', () => {
    const realFetch = globalThis.fetch;
    afterEach(() => {
        globalThis.fetch = realFetch;
    });

    const respondWith = (result: unknown) => {
        const calls: { url: string; body: unknown }[] = [];
        globalThis.fetch = mock(async (url: string | URL | Request, init?: RequestInit) => {
            calls.push({ url: String(url), body: JSON.parse(String(init?.body)) });
            return new Response(JSON.stringify({ result }));
        }) as unknown as typeof fetch;
        return calls;
    };

    test('getFilteredDiff posts the PR and filter to its endpoint', async () => {
        const calls = respondWith(ready);
        const res = await getFilteredDiff({
            Owner: 'acme',
            Repo: 'widgets',
            Number: 7,
            Filter: 'since-last-review',
        });
        expect(res).toEqual(ready);
        expect(calls).toHaveLength(1);
        expect(calls[0].url).toEndWith('/api/get-filtered-diff');
        expect(calls[0].body).toEqual({
            Owner: 'acme',
            Repo: 'widgets',
            Number: 7,
            Filter: 'since-last-review',
        });
    });

    test('listDiffFilters reads a null list as none', async () => {
        const calls = respondWith({ filters: null });
        expect(await listDiffFilters()).toEqual([]);
        expect(calls[0].url).toEndWith('/api/list-diff-filters');
        expect(calls[0].body).toEqual({});
    });
});
