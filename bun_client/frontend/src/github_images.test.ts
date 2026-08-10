import { describe, expect, test } from 'bun:test';
import { isGitHubImageHost, proxyImageUrl, proxySrcSet } from './github_images';

// API_BASE is '' outside a browser, so the proxied URLs below are same-origin.
const proxied = (href: string) => `/api/github-image?url=${encodeURIComponent(href)}`;

describe('proxyImageUrl', () => {
    test('routes a pasted screenshot through the bridge', () => {
        const href = 'https://github.com/user-attachments/assets/9b506449-0b9b-4c14-90f7';
        expect(proxyImageUrl(href)).toBe(proxied(href));
    });

    test('routes the githubusercontent CDN hosts too', () => {
        for (const href of [
            'https://user-images.githubusercontent.com/1/x.png',
            'https://private-user-images.githubusercontent.com/1/x.png?jwt=abc',
            'https://raw.githubusercontent.com/o/r/main/docs/x.png',
        ]) {
            expect(proxyImageUrl(href)).toBe(proxied(href));
        }
    });

    test('preserves the query string through the round trip', () => {
        const href = 'https://private-user-images.githubusercontent.com/1/x.png?jwt=a.b&v=2';
        const decoded = decodeURIComponent(proxyImageUrl(href).split('url=')[1]);
        expect(decoded).toBe(href);
    });

    test('leaves images hosted elsewhere alone', () => {
        const href = 'https://img.shields.io/badge/build-passing-green.svg';
        expect(proxyImageUrl(href)).toBe(href);
    });

    test('leaves relative, empty and non-http sources alone', () => {
        // No repository context here to resolve a relative path against.
        expect(proxyImageUrl('docs/img/diagram.png')).toBe('docs/img/diagram.png');
        expect(proxyImageUrl('')).toBe('');
        expect(proxyImageUrl('data:image/png;base64,AAAA')).toBe('data:image/png;base64,AAAA');
    });
});

describe('proxySrcSet', () => {
    test('rewrites every candidate and keeps its descriptor', () => {
        const one = 'https://github.com/user-attachments/assets/one';
        const two = 'https://github.com/user-attachments/assets/two';
        expect(proxySrcSet(`${one} 1x, ${two} 2x`)).toBe(`${proxied(one)} 1x, ${proxied(two)} 2x`);
    });

    test('handles a single candidate with no descriptor', () => {
        const one = 'https://user-images.githubusercontent.com/1/x.png';
        expect(proxySrcSet(one)).toBe(proxied(one));
    });

    test('drops empty candidates from a trailing comma', () => {
        expect(proxySrcSet('https://example.com/a.png 1x, ')).toBe('https://example.com/a.png 1x');
    });
});

describe('isGitHubImageHost', () => {
    test('only matches on a dot boundary', () => {
        expect(isGitHubImageHost('user-images.githubusercontent.com')).toBe(true);
        expect(isGitHubImageHost('githubusercontent.com.evil.test')).toBe(false);
        expect(isGitHubImageHost('notgithub.com')).toBe(false);
    });
});
