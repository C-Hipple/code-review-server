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
    'night-owl',
    'tokyo-night',
    'catppuccin',
    'everforest',
    'rose-pine',
    'synthwave-84',
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
    { value: 'tokyo-night', label: '🌃 Tokyo Night' },
    { value: 'catppuccin', label: '🐱 Catppuccin' },
    { value: 'everforest', label: '🌲 Everforest' },
    { value: 'rose-pine', label: '🌹 Rose Pine' },
    { value: 'synthwave-84', label: "🕹️ SynthWave '84" },
];

/**
 * Review Location preferences
 */
export type ReviewLocation = 'local' | 'github';

export const REVIEW_LOCATION_OPTIONS = [
    { value: 'local', label: '💻 Local (in app)' },
    { value: 'github', label: '🐙 GitHub' },
];

/**
 * Review diff font size preferences
 */
export const VALID_DIFF_FONT_SIZES = ['xs', 'sm', 'md', 'lg', 'xl'] as const;

export type DiffFontSize = (typeof VALID_DIFF_FONT_SIZES)[number];

export const DEFAULT_DIFF_FONT_SIZE: DiffFontSize = 'md';

// Base font size (px) of the review diff container. Line numbers, the +/-
// gutter and hunk headers size themselves in `em` off this value, so the whole
// diff scales together. Published as `--diff-font-size` on the document root.
export const DIFF_FONT_SIZE_PX: Record<DiffFontSize, number> = {
    xs: 11,
    sm: 12,
    md: 13,
    lg: 15,
    xl: 17,
};

export interface DiffFontSizeOption {
    value: DiffFontSize;
    label: string;
}

export const DIFF_FONT_SIZE_OPTIONS: DiffFontSizeOption[] = [
    { value: 'xs', label: `Extra Small (${DIFF_FONT_SIZE_PX.xs}px)` },
    { value: 'sm', label: `Small (${DIFF_FONT_SIZE_PX.sm}px)` },
    { value: 'md', label: `Medium (${DIFF_FONT_SIZE_PX.md}px)` },
    { value: 'lg', label: `Large (${DIFF_FONT_SIZE_PX.lg}px)` },
    { value: 'xl', label: `Extra Large (${DIFF_FONT_SIZE_PX.xl}px)` },
];
