import { describe, expect, test } from 'bun:test';
import { isGitHubImageHost, parseGitHubImageUrl } from './github_images';

describe('parseGitHubImageUrl', () => {
    test('accepts the hosts GitHub serves attachments from', () => {
        const accepted = [
            'https://github.com/user-attachments/assets/9b506449-0b9b-4c14-90f7-775beb9e68af',
            'https://www.github.com/user-attachments/assets/abc',
            'https://user-images.githubusercontent.com/1234/screenshot.png',
            'https://private-user-images.githubusercontent.com/1234/x.png?jwt=token',
            'https://raw.githubusercontent.com/owner/repo/main/docs/img/diagram.png',
            'https://githubusercontent.com/x.png',
        ];
        for (const href of accepted) {
            expect(parseGitHubImageUrl(href)?.href).toBe(href);
        }
    });

    test('rejects anything not on a GitHub host', () => {
        const rejected = [
            'https://example.com/pixel.png',
            // Suffix matching has to be on a dot boundary, or this passes.
            'https://githubusercontent.com.evil.test/x.png',
            'https://evil.test/?q=githubusercontent.com',
            'https://github.com.evil.test/x.png',
        ];
        for (const href of rejected) {
            expect(parseGitHubImageUrl(href)).toBeNull();
        }
    });

    test('rejects non-http protocols and unparseable input', () => {
        expect(parseGitHubImageUrl('file:///etc/passwd')).toBeNull();
        expect(parseGitHubImageUrl('data:image/png;base64,AAAA')).toBeNull();
        expect(parseGitHubImageUrl('/user-attachments/assets/abc')).toBeNull();
        expect(parseGitHubImageUrl('')).toBeNull();
        expect(parseGitHubImageUrl(null)).toBeNull();
    });

    test('matches hosts case-insensitively', () => {
        expect(parseGitHubImageUrl('https://GitHub.com/user-attachments/assets/a')).not.toBeNull();
    });
});

describe('isGitHubImageHost', () => {
    test('only matches on a dot boundary', () => {
        expect(isGitHubImageHost('user-images.githubusercontent.com')).toBe(true);
        expect(isGitHubImageHost('githubusercontent.com.evil.test')).toBe(false);
        expect(isGitHubImageHost('notgithub.com')).toBe(false);
    });
});
