import {
    oneDark,
    oneLight,
    gruvboxDark,
    gruvboxLight,
    solarizedlight,
    solarizedDarkAtom,
    dracula,
    nord,
    nightOwl,
} from 'react-syntax-highlighter/dist/esm/styles/prism';
import type { Theme } from '../../design';

// Custom Prism theme based on the selected app theme, adjusted so highlighted
// lines blend into the diff rows (transparent background, no padding).
export const buildDiffTheme = (theme: Theme) => {
    const getBaseTheme = () => {
        switch (theme) {
            case 'light':
                return oneLight;
            case 'gruvbox-dark':
                return gruvboxDark;
            case 'gruvbox-light':
                return gruvboxLight;
            case 'solarized-light':
                return solarizedlight;
            case 'solarized-dark':
                return solarizedDarkAtom;
            case 'dracula':
                return dracula;
            case 'nord':
                return nord;
            case 'night-owl':
                return nightOwl;
            default:
                return oneDark;
        }
    };
    const baseTheme = getBaseTheme();
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
