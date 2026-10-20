import { describe, expect, test } from 'bun:test';
import { buildFrameDocument, readFrameVars } from './html_frame';

describe('buildFrameDocument', () => {
    test('carries the theme in and the body as is', () => {
        const doc = buildFrameDocument('<p>Hi</p>', { '--fg': '#e6edf3', '--bg': '#0d1117' });
        expect(doc).toContain(':root{color-scheme:light dark;--fg:#e6edf3;--bg:#0d1117;}');
        expect(doc).toContain('<body><p>Hi</p></body>');
        // Links open in a new tab, without a referrer.
        expect(doc).toContain('<base target="_blank">');
        expect(doc).toContain('<meta name="referrer" content="no-referrer">');
    });

    test('a variable value cannot break out of the stylesheet', () => {
        const doc = buildFrameDocument('', { '--fg': 'red;}</style><script>x()</script>' });
        expect(doc).not.toContain('</style><script>');
    });

    test('no document (tests, SSR): no variables', () => {
        expect(readFrameVars()).toEqual({});
    });
});
