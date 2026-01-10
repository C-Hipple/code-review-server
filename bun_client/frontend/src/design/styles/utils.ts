/**
 * Style Utility Functions
 * Helpers for common styling patterns
 */

import { colors } from './tokens';

export type StatusVariant = 'success' | 'danger' | 'warning' | 'info' | 'neutral';

/**
 * Get background color for status variant
 */
export function getStatusColor(variant: StatusVariant): string {
  switch (variant) {
    case 'success':
      return colors.success;
    case 'danger':
      return colors.danger;
    case 'warning':
      return colors.warning;
    case 'info':
      return colors.accent;
    case 'neutral':
      return colors.textSecondary;
    default:
      return colors.textSecondary;
  }
}

/**
 * Map common status strings to variants
 */
export function mapStatusToVariant(status: string): StatusVariant {
  const statusLower = status.toLowerCase();

  if (statusLower === 'success' || statusLower === 'done' || statusLower === 'passed') {
    return 'success';
  }
  if (statusLower === 'error' || statusLower === 'failed' || statusLower === 'failure' || statusLower === 'cancelled') {
    return 'danger';
  }
  if (statusLower === 'warning' || statusLower === 'pending' || statusLower === 'progress' || statusLower === 'todo') {
    return 'warning';
  }
  if (statusLower === 'info') {
    return 'info';
  }

  return 'neutral';
}

/**
 * Combine class names, filtering out falsy values
 */
export function cn(...classNames: (string | undefined | null | false)[]): string {
  return classNames.filter(Boolean).join(' ');
}
