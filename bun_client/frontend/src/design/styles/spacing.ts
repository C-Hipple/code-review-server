/**
 * Spacing Utilities
 * Helper functions for consistent spacing using 4px base scale
 */

export const spacingScale = {
  xs: 4,
  sm: 8,
  md: 12,
  lg: 16,
  xl: 20,
  '2xl': 24,
  '3xl': 32,
  '4xl': 40,
} as const;

export type SpacingKey = keyof typeof spacingScale;

/**
 * Get spacing value in pixels
 */
export function getSpacing(size: SpacingKey): string {
  return `${spacingScale[size]}px`;
}

/**
 * Get multiple spacing values (e.g., for padding: "8px 16px")
 */
export function getSpacingValues(...sizes: SpacingKey[]): string {
  return sizes.map(size => getSpacing(size)).join(' ');
}
