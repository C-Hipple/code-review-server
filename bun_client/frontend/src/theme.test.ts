import { expect, test, describe } from 'bun:test';
import { readFileSync } from 'node:fs';
import { VALID_THEMES, THEME_OPTIONS, isTheme, resolveTheme } from './design/theme';
import { getSyntaxTheme } from './components/review/diff_theme';
import { parseCssColor } from './mermaid_utils';

const css = readFileSync(new URL('./index.css', import.meta.url), 'utf8');

// WCAG relative luminance of a color, 0 (black) to 1 (white); null when the
// color can't be read.
const luminance = (color: string): number | null => {
    const rgb = parseCssColor(color);
    if (!rgb || rgb.some(n => !Number.isFinite(n))) return null;
    const [r, g, b] = rgb.map(v => {
        const c = v / 255;
        return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

// WCAG contrast ratio of two colors, 1:1 to 21:1; null when either can't be read.
const contrastRatio = (a: string, b: string): number | null => {
    const la = luminance(a);
    const lb = luminance(b);
    if (la === null || lb === null) return null;
    return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
};

// Every var the dark theme defines is one the app expects to resolve under any
// theme, so a new theme block that forgets one silently inherits the default.
const REQUIRED_VARS = [
    '--bg-primary',
    '--bg-secondary',
    '--bg-tertiary',
    '--text-primary',
    '--text-secondary',
    '--text-tertiary',
    '--accent',
    '--accent-dim',
    '--border',
    '--success',
    '--danger',
    '--warning',
    '--text-success',
    '--text-danger',
    '--text-warning',
    '--text-merged',
    '--bg-success-dim',
    '--bg-danger-dim',
    '--bg-warning-dim',
    '--bg-merged-dim',
    '--bg-info-dim',
    '--bg-info-dim-strong',
    '--border-success-dim',
    '--border-danger-dim',
    '--border-warning-dim',
    '--border-info-dim',
    '--border-merged-dim',
    '--diff-add-bg',
    '--diff-add-gutter-bg',
    '--diff-del-bg',
    '--diff-del-gutter-bg',
    '--diff-hunk-bg',
    '--shadow-sm',
    '--shadow-md',
    '--shadow-lg',
    '--shadow-glow',
    '--overlay-bg',
];

// Body of the `[data-theme='name'] { ... }` rule, or null when absent. The
// default 'dark' theme shares its block with `:root`.
const themeBlock = (name: string): string | null => {
    const selector =
        name === 'dark' ? `:root,\\s*\\[data-theme='dark'\\]` : `\\[data-theme='${name}'\\]`;
    const match = css.match(new RegExp(`${selector}\\s*\\{([^}]*)\\}`));
    return match ? match[1] : null;
};

describe('theme definitions', () => {
    test('every theme in the picker is a valid theme', () => {
        for (const option of THEME_OPTIONS) {
            expect(VALID_THEMES).toContain(option.value);
        }
    });

    test('every valid theme is offered in the picker', () => {
        const offered = THEME_OPTIONS.map(o => o.value);
        for (const theme of VALID_THEMES) {
            expect(offered).toContain(theme);
        }
    });

    test('the picker has no duplicate entries', () => {
        const offered = THEME_OPTIONS.map(o => o.value);
        expect(new Set(offered).size).toBe(offered.length);
    });

    test('every theme has a CSS block defining all required variables', () => {
        for (const theme of VALID_THEMES) {
            const block = themeBlock(theme);
            expect(block, `missing [data-theme='${theme}'] block in index.css`).not.toBeNull();
            for (const cssVar of REQUIRED_VARS) {
                expect(block, `${theme} is missing ${cssVar}`).toContain(`${cssVar}:`);
            }
        }
    });

    test('a theme’s syntax colors are legible on its background', () => {
        // A light theme falling through to a dark Prism style paints pale code
        // on pale rows, so the two halves of a theme have to agree.
        for (const theme of VALID_THEMES) {
            const bg = themeBlock(theme)?.match(/--bg-primary:\s*([^;]+);/)?.[1];
            expect(bg, `${theme} has no --bg-primary`).toBeTruthy();

            const style = getSyntaxTheme(theme) as Record<string, { color?: string }>;
            const text = (style['code[class*="language-"]'] ?? style["code[class*='language-']"])
                ?.color;
            expect(text, `${theme}'s Prism style has no code color`).toBeTruthy();

            const ratio = contrastRatio(text!, bg!.trim());
            if (ratio === null) continue; // a color form we can't read
            expect(
                ratio,
                `${theme}: code ${text} on ${bg} is ${ratio.toFixed(1)}:1`
            ).toBeGreaterThan(3);
        }
    });
});

describe('resolveTheme', () => {
    test('accepts known themes', () => {
        expect(resolveTheme('tokyo-night')).toBe('tokyo-night');
        expect(resolveTheme('github-dark')).toBe('github-dark');
    });

    test('migrates the pre-flavor catppuccin value to mocha', () => {
        expect(resolveTheme('catppuccin')).toBe('catppuccin-mocha');
    });

    test('rejects unknown and missing values so callers can pick a default', () => {
        expect(resolveTheme('not-a-theme')).toBeNull();
        expect(resolveTheme(null)).toBeNull();
    });

    test('isTheme narrows only known values', () => {
        expect(isTheme('monokai')).toBe(true);
        expect(isTheme('catppuccin')).toBe(false);
        expect(isTheme(undefined)).toBe(false);
    });
});
