import { describe, expect, test } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import GitHubMarkdown from './components/GitHubMarkdown';

const render = (body: string) => renderToStaticMarkup(<GitHubMarkdown>{body}</GitHubMarkdown>);

// Verbatim shape of a GitHub comment with two screenshots attached, which is
// what the upload widget writes into the body.
const COMMENT_WITH_SCREENSHOTS = `So I've done some quick debugging.

Mid race: <img width="1905" height="1189" alt="Screenshot 2025-12-01 at 13 04 37" src="https://github.com/user-attachments/assets/9b506449-0b9b-4c14-90f7-775beb9e68af" />

End of race: <img width="1910" height="1190" alt="Screenshot 2025-12-01 at 13 05 28" src="https://github.com/user-attachments/assets/016b4b0e-57d4-4b22-8ae9-34d730246647" />`;

describe('GitHubMarkdown', () => {
    test('renders the raw <img> tags GitHub embeds in a comment', () => {
        const html = render(COMMENT_WITH_SCREENSHOTS);
        const images = html.match(/<img\b/g) ?? [];
        expect(images).toHaveLength(2);
        expect(html).toContain(
            'src="/api/github-image?url=https%3A%2F%2Fgithub.com%2Fuser-attachments%2Fassets%2F9b506449-0b9b-4c14-90f7-775beb9e68af"'
        );
        expect(html).toContain('alt="Screenshot 2025-12-01 at 13 04 37"');
        // The surrounding prose still renders as markdown.
        expect(html).toContain('<p>So I&#x27;ve done some quick debugging.</p>');
    });

    test("clamps the screenshot's stamped-on natural size to the card", () => {
        const html = render(COMMENT_WITH_SCREENSHOTS);
        expect(html).toContain('width="1905"');
        expect(html).toContain('max-width:100%');
        expect(html).toContain('height:auto');
    });

    test('proxies markdown image syntax as well as raw tags', () => {
        const html = render('![shot](https://user-images.githubusercontent.com/1/x.png)');
        expect(html).toContain(
            'src="/api/github-image?url=https%3A%2F%2Fuser-images.githubusercontent.com%2F1%2Fx.png"'
        );
    });

    test('loads non-GitHub images directly', () => {
        const html = render('![badge](https://img.shields.io/badge/ci-green.svg)');
        expect(html).toContain('src="https://img.shields.io/badge/ci-green.svg"');
        expect(html).not.toContain('/api/github-image');
    });

    test('rewrites both halves of a light/dark <picture>', () => {
        const html = render(
            '<picture>' +
                '<source media="(prefers-color-scheme: dark)" srcset="https://github.com/user-attachments/assets/dark">' +
                '<img src="https://github.com/user-attachments/assets/light" />' +
                '</picture>'
        );
        // React keeps the JSX spelling in static markup; HTML attribute
        // names are case-insensitive, so the browser reads it as `srcset`.
        expect(html).toMatch(/srcset="\/api\/github-image\?url=/i);
        expect(html).toContain('assets%2Fdark');
        expect(html).toContain('assets%2Flight');
    });

    test('keeps the other raw HTML people write in review comments', () => {
        const html = render('<details><summary>Traceback</summary>\n\nboom\n\n</details>');
        expect(html).toContain('<details>');
        expect(html).toContain('<summary>Traceback</summary>');
        expect(html).toContain('boom');
    });

    test('strips scripts and event handlers from an untrusted body', () => {
        const html = render(
            '<img src="https://github.com/user-attachments/assets/x" onerror="alert(1)">' +
                '<script>alert(2)</script>' +
                '<a href="javascript:alert(3)">click</a>' +
                '<p style="position:fixed;top:0">overlay</p>'
        );
        expect(html).toContain('<img');
        expect(html).not.toContain('onerror');
        expect(html).not.toContain('alert(2)');
        expect(html).not.toContain('javascript:');
        expect(html).not.toContain('position:fixed');
    });

    test('opens links in a new tab so the review stays put', () => {
        const html = render('see [the issue](https://github.com/o/r/issues/1)');
        expect(html).toContain('href="https://github.com/o/r/issues/1"');
        expect(html).toContain('target="_blank"');
        expect(html).toContain('rel="noopener noreferrer"');
    });
});
