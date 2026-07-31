import {
    oneDark,
    oneLight,
    ghcolors,
    vscDarkPlus,
    gruvboxDark,
    gruvboxLight,
    solarizedlight,
    solarizedDarkAtom,
    okaidia,
    dracula,
    nord,
    nightOwl,
    synthwave84,
} from 'react-syntax-highlighter/dist/esm/styles/prism';
import type { Theme } from '../../design';

// Prism style that pairs with an app theme. Themes with no close Prism
// equivalent fall back to One Dark (or One Light for the light flavors) so the
// syntax colors never fight the surrounding chrome.
export const getSyntaxTheme = (theme: Theme) => {
    switch (theme) {
        case 'light':
            return oneLight;
        case 'github-light':
            return ghcolors;
        case 'github-dark':
            return vscDarkPlus;
        case 'gruvbox-dark':
            return gruvboxDark;
        case 'gruvbox-light':
            return gruvboxLight;
        case 'solarized-light':
            return solarizedlight;
        case 'solarized-dark':
            return solarizedDarkAtom;
        case 'monokai':
            return okaidia;
        case 'dracula':
            return dracula;
        case 'nord':
            return nord;
        case 'night-owl':
            return nightOwl;
        case 'synthwave-84':
            return synthwave84;
        case 'catppuccin-latte':
            return oneLight;
        default:
            return oneDark;
    }
};

// Custom Prism theme based on the selected app theme, adjusted so highlighted
// lines blend into the diff rows (transparent background, no padding).
export const buildDiffTheme = (theme: Theme) => {
    const baseTheme = getSyntaxTheme(theme);
    return {
        ...baseTheme,
        'pre[class*="language-"]': {
            ...(baseTheme['pre[class*="language-"]'] as any),
            background: 'transparent',
            margin: 0,
            padding: 0,
            overflow: 'visible',
        },
        'code[class*="language-"]': {
            ...(baseTheme['code[class*="language-"]'] as any),
            background: 'transparent',
        },
    };
};

export type DiffTheme = ReturnType<typeof buildDiffTheme>;
