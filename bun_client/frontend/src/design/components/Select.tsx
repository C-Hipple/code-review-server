import React from 'react';
import { colors, borderRadius, spacing, transitions } from '../styles/tokens';
import { fontSize } from '../styles/typography';

export interface SelectProps extends React.SelectHTMLAttributes<HTMLSelectElement> {
  /** Select label */
  label?: string;
  /** Select options */
  options?: Array<{ value: string; label: string }>;
}

/**
 * Select dropdown component with consistent styling
 */
export function Select({
  label,
  options,
  style,
  children,
  ...props
}: SelectProps) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: spacing.xs }}>
      {label && (
        <label
          style={{
            fontSize: fontSize.sm,
            color: colors.textSecondary,
            fontWeight: 500,
          }}
        >
          {label}
        </label>
      )}
      <select
        style={{
          background: colors.bgPrimary,
          border: `1px solid ${colors.border}`,
          color: colors.textPrimary,
          padding: `6px ${spacing.sm}`,
          borderRadius: borderRadius.md,
          fontSize: fontSize.base,
          outline: 'none',
          cursor: 'pointer',
          transition: `border-color ${transitions.normal}`,
          minWidth: '120px',
          ...style,
        }}
        onFocus={(e) => {
          e.currentTarget.style.borderColor = colors.accent;
        }}
        onBlur={(e) => {
          e.currentTarget.style.borderColor = colors.border;
        }}
        {...props}
      >
        {options
          ? options.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))
          : children}
      </select>
    </div>
  );
}
