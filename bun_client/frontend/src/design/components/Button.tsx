import React from 'react';
import { colors, spacing, borderRadius, transitions } from '../styles/tokens';
import { fontSize, fontWeight } from '../styles/typography';

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  /** Visual variant of the button */
  variant?: 'primary' | 'secondary' | 'ghost' | 'danger';
  /** Size of the button */
  size?: 'sm' | 'md' | 'lg';
  /** Icon to display before children */
  icon?: React.ReactNode;
  /** Show loading spinner */
  loading?: boolean;
  /** Make button full width */
  fullWidth?: boolean;
}

/**
 * Button component with consistent styling and variants
 */
export function Button({
  variant = 'primary',
  size = 'md',
  icon,
  loading = false,
  fullWidth = false,
  children,
  disabled,
  style,
  ...props
}: ButtonProps) {
  const baseStyles: React.CSSProperties = {
    display: 'inline-flex',
    alignItems: 'center',
    justifyContent: 'center',
    gap: spacing.sm,
    border: 'none',
    borderRadius: borderRadius.md,
    fontFamily: 'inherit',
    fontWeight: fontWeight.medium,
    cursor: disabled || loading ? 'default' : 'pointer',
    transition: `all ${transitions.normal}`,
    opacity: disabled ? 0.5 : 1,
    pointerEvents: disabled ? 'none' : 'auto',
    width: fullWidth ? '100%' : 'auto',
  };

  // Variant styles
  const variantStyles: Record<string, React.CSSProperties> = {
    primary: {
      background: colors.accent,
      color: 'white',
    },
    secondary: {
      background: 'transparent',
      color: colors.textSecondary,
      border: `1px solid ${colors.border}`,
    },
    ghost: {
      background: 'transparent',
      color: colors.accent,
      border: `1px solid ${colors.accent}`,
    },
    danger: {
      background: colors.danger,
      color: 'white',
    },
  };

  // Size styles
  const sizeStyles: Record<string, React.CSSProperties> = {
    sm: {
      padding: `6px 12px`,
      fontSize: fontSize.sm,
    },
    md: {
      padding: `8px 16px`,
      fontSize: fontSize.base,
    },
    lg: {
      padding: `10px 20px`,
      fontSize: fontSize.md,
    },
  };

  const combinedStyles: React.CSSProperties = {
    ...baseStyles,
    ...variantStyles[variant],
    ...sizeStyles[size],
    ...style,
  };

  return (
    <button style={combinedStyles} disabled={disabled || loading} {...props}>
      {loading && <Spinner size={size} />}
      {!loading && icon && <span style={{ display: 'flex', alignItems: 'center' }}>{icon}</span>}
      {children}
    </button>
  );
}

/**
 * Loading spinner component
 */
function Spinner({ size }: { size: 'sm' | 'md' | 'lg' }) {
  const sizeMap = {
    sm: '12px',
    md: '14px',
    lg: '16px',
  };

  const spinnerSize = sizeMap[size];

  return (
    <span
      style={{
        width: spinnerSize,
        height: spinnerSize,
        border: '2px solid currentColor',
        borderTopColor: 'transparent',
        borderRadius: '50%',
        animation: 'spin 0.6s linear infinite',
        display: 'inline-block',
      }}
    />
  );
}

// Add spin animation to index.css if not already present
