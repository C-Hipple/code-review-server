import { expect, test, describe } from 'bun:test';
import { readFileSync } from 'node:fs';
import { VALID_THEMES, THEME_OPTIONS, isTheme, resolveTheme } from './design/theme';

const css = readFileSync(new URL('./index.css', import.meta.url), 'utf8');

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
