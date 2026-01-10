/**
 * Design Tokens
 * Maps CSS variables to TypeScript constants for type-safe styling
 */

export const colors = {
  bgPrimary: 'var(--bg-primary)',
  bgSecondary: 'var(--bg-secondary)',
  bgTertiary: 'var(--bg-tertiary)',
  textPrimary: 'var(--text-primary)',
  textSecondary: 'var(--text-secondary)',
  textTertiary: 'var(--text-tertiary)',
  accent: 'var(--accent)',
  accentDim: 'var(--accent-dim)',
  border: 'var(--border)',
  success: 'var(--success)',
  danger: 'var(--danger)',
  warning: 'var(--warning)',
};

export const spacing = {
  xs: '4px',
  sm: '8px',
  md: '12px',
  lg: '16px',
  xl: '20px',
  '2xl': '24px',
  '3xl': '32px',
  '4xl': '40px',
};

export const borderRadius = {
  sm: '4px',
  md: '6px',
  lg: '8px',
  xl: '12px',
  pill: '999px',
};

export const transitions = {
  fast: '0.1s ease',
  normal: '0.15s ease',
  slow: '0.3s ease',
};

export const shadows = {
  sm: '0 1px 3px rgba(0, 0, 0, 0.3)',
  md: '0 4px 12px rgba(0, 0, 0, 0.3)',
  lg: '0 8px 24px rgba(0, 0, 0, 0.4)',
  glow: '0 0 0 1px rgba(88, 166, 255, 0.2)',
};
