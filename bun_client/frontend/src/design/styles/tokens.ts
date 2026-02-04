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

  // Status Text
  textSuccess: 'var(--text-success)',
  textDanger: 'var(--text-danger)',
  textWarning: 'var(--text-warning)',
  textMerged: 'var(--text-merged)',

  // Status Backgrounds
  bgSuccessDim: 'var(--bg-success-dim)',
  bgDangerDim: 'var(--bg-danger-dim)',
  bgWarningDim: 'var(--bg-warning-dim)',
  bgMergedDim: 'var(--bg-merged-dim)',
  bgInfoDim: 'var(--bg-info-dim)',
  bgInfoDimStrong: 'var(--bg-info-dim-strong)',

  // Status Borders
  borderSuccessDim: 'var(--border-success-dim)',
  borderDangerDim: 'var(--border-danger-dim)',
  borderWarningDim: 'var(--border-warning-dim)',
  borderInfoDim: 'var(--border-info-dim)',
  borderMergedDim: 'var(--border-merged-dim)',

  // Diff
  diffAddBg: 'var(--diff-add-bg)',
  diffAddGutterBg: 'var(--diff-add-gutter-bg)',
  diffDelBg: 'var(--diff-del-bg)',
  diffDelGutterBg: 'var(--diff-del-gutter-bg)',
  diffHunkBg: 'var(--diff-hunk-bg)',
  overlayBg: 'var(--overlay-bg)',
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
  sm: 'var(--shadow-sm)',
  md: 'var(--shadow-md)',
  lg: 'var(--shadow-lg)',
  glow: 'var(--shadow-glow)',
};
