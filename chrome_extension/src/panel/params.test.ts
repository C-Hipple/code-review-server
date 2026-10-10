import { describe, expect, test } from 'bun:test';
import { linkTarget, readPanelParams } from './params';

describe('readPanelParams', () => {
    test('the PR the content script passed', () => {
        expect(readPanelParams('?owner=octocat&repo=hello.js&number=42')).toEqual({
            standalone: false,
            pr: { owner: 'octocat', repo: 'hello.js', number: 42 },
        });
    });

    test('the standalone window', () => {
        expect(readPanelParams('?standalone=1')).toEqual({ standalone: true, pr: null });
    });

    test('no query', () => {
        expect(readPanelParams('')).toEqual({ standalone: false, pr: null });
    });

    test.each([
        '?owner=octocat&repo=hello',
        '?owner=octocat&repo=hello&number=x',
        '?owner=octocat&repo=hello&number=0',
        '?owner=oct/cat&repo=hello&number=1',
        '?owner=octocat&repo=../x&number=1',
        '?owner=octocat&repo=hello&number=1%2Ffiles',
    ])('rejects %s', search => {
        expect(readPanelParams(search).pr).toBeNull();
    });
});

describe('linkTarget', () => {
    test('navigates the GitHub tab when embedded, a new tab when standalone', () => {
        expect(linkTarget({ standalone: false, pr: null })).toBe('_top');
        expect(linkTarget({ standalone: true, pr: null })).toBe('_blank');
    });
});
