import React from 'react';
import { colors, borderRadius, spacing } from '../styles/tokens';
import { fontSize, fontWeight } from '../styles/typography';
import { getStatusColor, type StatusVariant } from '../styles/utils';

export interface BadgeProps {
  /** Visual variant of the badge */
  variant?: StatusVariant;
  /** Size of the badge */
  size?: 'sm' | 'md' | 'lg';
  /** Icon to display before children */
  icon?: React.ReactNode;
  /** Make badge fully rounded (pill shape) */
  pill?: boolean;
  /** Badge content */
  children: React.ReactNode;
  /** Additional styles */
  style?: React.CSSProperties;
}

/**
 * Badge component for status indicators, labels, and tags
 */
export function Badge({
  variant = 'neutral',
  size = 'md',
  icon,
  pill = false,
  children,
  style,
}: BadgeProps) {
  const baseStyles: React.CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    gap: spacing.xs,
    fontWeight: fontWeight.bold,
    textTransform: 'uppercase' as const,
    borderRadius: pill ? borderRadius.pill : borderRadius.sm,
    background: getStatusColor(variant),
    color: 'white',
    whiteSpace: 'nowrap',
  };

  // Size styles
  const sizeStyles: Record<string, React.CSSProperties> = {
    sm: {
      padding: `2px 6px`,
      fontSize: fontSize.xs,
    },
    md: {
      padding: `3px 8px`,
      fontSize: fontSize.xs,
    },
    lg: {
      padding: `4px 10px`,
      fontSize: fontSize.sm,
    },
  };

  const combinedStyles: React.CSSProperties = {
    ...baseStyles,
    ...sizeStyles[size],
    ...style,
  };

  return (
    <span style={combinedStyles}>
      {icon && <span style={{ display: 'flex', alignItems: 'center' }}>{icon}</span>}
      {children}
    </span>
  );
}
