import React from 'react';
import { colors, borderRadius, spacing, transitions } from '../styles/tokens';
import { fontSize } from '../styles/typography';

export interface InputProps extends React.InputHTMLAttributes<HTMLInputElement> {
  /** Input label */
  label?: string;
  /** Error message */
  error?: string;
  /** Leading icon */
  icon?: React.ReactNode;
  /** Show clear button */
  clearable?: boolean;
  /** Clear handler */
  onClear?: () => void;
}

/**
 * Text input component with consistent styling
 */
export function Input({
  label,
  error,
  icon,
  clearable = false,
  onClear,
  style,
  value,
  ...props
}: InputProps) {
  const hasValue = value !== undefined && value !== '';

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
      <div style={{ position: 'relative' }}>
        {icon && (
          <span
            style={{
              position: 'absolute',
              left: spacing.md,
              top: '50%',
              transform: 'translateY(-50%)',
              color: colors.textSecondary,
              display: 'flex',
              alignItems: 'center',
              pointerEvents: 'none',
            }}
          >
            {icon}
          </span>
        )}
        <input
          style={{
            width: '100%',
            background: colors.bgPrimary,
            border: `1px solid ${colors.border}`,
            color: colors.textPrimary,
            padding: `${spacing.sm} ${spacing.md}`,
            paddingLeft: icon ? spacing['3xl'] : spacing.md,
            paddingRight: clearable && hasValue ? spacing['3xl'] : spacing.md,
            borderRadius: borderRadius.md,
            fontSize: fontSize.md,
            outline: 'none',
            transition: `border-color ${transitions.normal}`,
            ...style,
          }}
          value={value}
          onFocus={(e) => {
            e.currentTarget.style.borderColor = colors.accent;
          }}
          onBlur={(e) => {
            e.currentTarget.style.borderColor = colors.border;
          }}
          {...props}
        />
        {clearable && hasValue && onClear && (
          <button
            type="button"
            onClick={onClear}
            style={{
              position: 'absolute',
              right: spacing.sm,
              top: '50%',
              transform: 'translateY(-50%)',
              background: colors.bgTertiary,
              border: 'none',
              borderRadius: '50%',
              width: '20px',
              height: '20px',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              cursor: 'pointer',
              color: colors.textSecondary,
              fontSize: fontSize.sm,
              padding: 0,
            }}
          >
            ×
          </button>
        )}
      </div>
      {error && (
        <span style={{ fontSize: fontSize.sm, color: colors.danger }}>
          {error}
        </span>
      )}
    </div>
  );
}

export interface TextAreaProps extends React.TextareaHTMLAttributes<HTMLTextAreaElement> {
  /** TextArea label */
  label?: string;
  /** Error message */
  error?: string;
}

/**
 * TextArea component with consistent styling
 */
export function TextArea({
  label,
  error,
  style,
  ...props
}: TextAreaProps) {
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
      <textarea
        style={{
          width: '100%',
          background: colors.bgPrimary,
          border: `1px solid ${colors.border}`,
          color: colors.textPrimary,
          padding: `${spacing.sm} ${spacing.md}`,
          borderRadius: borderRadius.md,
          fontSize: fontSize.md,
          outline: 'none',
          transition: `border-color ${transitions.normal}`,
          fontFamily: 'inherit',
          resize: 'vertical',
          ...style,
        }}
        onFocus={(e) => {
          e.currentTarget.style.borderColor = colors.accent;
        }}
        onBlur={(e) => {
          e.currentTarget.style.borderColor = colors.border;
        }}
        {...props}
      />
      {error && (
        <span style={{ fontSize: fontSize.sm, color: colors.danger }}>
          {error}
        </span>
      )}
    </div>
  );
}
