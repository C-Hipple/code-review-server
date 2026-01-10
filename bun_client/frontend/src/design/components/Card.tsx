import React from 'react';
import { colors, borderRadius, spacing, transitions, shadows } from '../styles/tokens';

export interface CardProps {
  /** Visual variant of the card */
  variant?: 'default' | 'elevated' | 'outlined';
  /** Padding size */
  padding?: 'none' | 'sm' | 'md' | 'lg';
  /** Enable hover effects */
  hover?: boolean;
  /** Gradient background */
  gradient?: boolean;
  /** Click handler (makes card clickable) */
  onClick?: () => void;
  /** Additional class name */
  className?: string;
  /** Additional styles */
  style?: React.CSSProperties;
  /** Card content */
  children: React.ReactNode;
}

/**
 * Card component for container elements
 */
export function Card({
  variant = 'default',
  padding = 'lg',
  hover = false,
  gradient = false,
  onClick,
  className,
  style,
  children,
}: CardProps) {
  const baseStyles: React.CSSProperties = {
    borderRadius: borderRadius.lg,
    transition: `all ${transitions.normal}`,
    cursor: onClick ? 'pointer' : 'default',
  };

  // Variant styles
  const variantStyles: Record<string, React.CSSProperties> = {
    default: {
      background: colors.bgSecondary,
      border: `1px solid ${colors.border}`,
    },
    elevated: {
      background: colors.bgSecondary,
      border: `1px solid ${colors.border}`,
      boxShadow: shadows.md,
    },
    outlined: {
      background: colors.bgPrimary,
      border: `1px solid ${colors.border}`,
    },
  };

  // Padding styles
  const paddingStyles: Record<string, React.CSSProperties> = {
    none: { padding: 0 },
    sm: { padding: spacing.md },
    md: { padding: spacing.lg },
    lg: { padding: spacing.xl },
  };

  // Gradient background
  const gradientStyle: React.CSSProperties = gradient
    ? {
        background: `linear-gradient(135deg, ${colors.bgSecondary} 0%, ${colors.bgTertiary} 100%)`,
      }
    : {};

  const combinedStyles: React.CSSProperties = {
    ...baseStyles,
    ...variantStyles[variant],
    ...paddingStyles[padding],
    ...gradientStyle,
    ...style,
  };

  return (
    <div
      className={className}
      style={combinedStyles}
      onClick={onClick}
      onMouseEnter={(e) => {
        if (hover) {
          e.currentTarget.style.transform = 'translateY(-2px)';
          e.currentTarget.style.boxShadow = shadows.md;
        }
      }}
      onMouseLeave={(e) => {
        if (hover) {
          e.currentTarget.style.transform = 'translateY(0)';
          e.currentTarget.style.boxShadow = variant === 'elevated' ? shadows.md : 'none';
        }
      }}
    >
      {children}
    </div>
  );
}
