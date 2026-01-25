/**
 * Theme configuration and types
 */

export const VALID_THEMES = [
    'light',
    'dark',
    'gruvbox-dark',
    'gruvbox-light',
    'solarized-light',
    'solarized-dark',
    'dracula',
    'nord',
    'night-owl'
] as const;

export type Theme = (typeof VALID_THEMES)[number];

export interface ThemeOption {
    value: Theme;
    label: string;
}

export const THEME_OPTIONS: ThemeOption[] = [
    { value: 'dark', label: '🌙 Dark (One Dark)' },
    { value: 'light', label: '☀️ Light (One Light)' },
    { value: 'gruvbox-dark', label: '📦 Gruvbox Dark' },
    { value: 'gruvbox-light', label: '📦 Gruvbox Light' },
    { value: 'solarized-dark', label: '☀️ Solarized Dark' },
    { value: 'solarized-light', label: '☀️ Solarized Light' },
    { value: 'dracula', label: '🧛 Dracula' },
    { value: 'nord', label: '❄️ Nord' },
    { value: 'night-owl', label: '🦉 Night Owl' },
];

/**
 * Review Location preferences
 */
export type ReviewLocation = 'local' | 'github';

export const REVIEW_LOCATION_OPTIONS = [
    { value: 'local', label: '💻 Local (in app)' },
    { value: 'github', label: '🐙 GitHub' },
];
