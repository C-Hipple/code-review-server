import React, { useEffect } from 'react';
import { colors, borderRadius, spacing, shadows } from '../styles/tokens';
import { fontSize, fontWeight } from '../styles/typography';

export interface ModalProps {
  /** Modal open state */
  isOpen: boolean;
  /** Close handler */
  onClose: () => void;
  /** Modal title */
  title?: string;
  /** Modal content */
  children: React.ReactNode;
  /** Footer content (e.g., action buttons) */
  footer?: React.ReactNode;
  /** Modal size */
  size?: 'sm' | 'md' | 'lg' | 'xl';
}

/**
 * Modal component with backdrop and ESC key handling
 */
export function Modal({
  isOpen,
  onClose,
  title,
  children,
  footer,
  size = 'md',
}: ModalProps) {
  // ESC key handler
  useEffect(() => {
    if (!isOpen) return;

    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose();
      }
    };

    window.addEventListener('keydown', handleEscape);
    return () => window.removeEventListener('keydown', handleEscape);
  }, [isOpen, onClose]);

  if (!isOpen) return null;

  // Size mapping
  const sizeMap = {
    sm: '400px',
    md: '600px',
    lg: '800px',
    xl: '1000px',
  };

  const overlayStyle: React.CSSProperties = {
    position: 'fixed',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    background: 'rgba(0, 0, 0, 0.7)',
    backdropFilter: 'blur(4px)',
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    zIndex: 1000,
    padding: spacing.xl,
  };

  const modalStyle: React.CSSProperties = {
    background: colors.bgSecondary,
    border: `1px solid ${colors.border}`,
    borderRadius: borderRadius.lg,
    boxShadow: shadows.lg,
    width: '100%',
    maxWidth: sizeMap[size],
    maxHeight: '90vh',
    display: 'flex',
    flexDirection: 'column',
  };

  const headerStyle: React.CSSProperties = {
    padding: spacing.xl,
    borderBottom: `1px solid ${colors.border}`,
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'space-between',
  };

  const bodyStyle: React.CSSProperties = {
    padding: spacing.xl,
    overflowY: 'auto',
    flex: 1,
  };

  const footerStyle: React.CSSProperties = {
    padding: spacing.xl,
    borderTop: `1px solid ${colors.border}`,
    display: 'flex',
    gap: spacing.md,
    justifyContent: 'flex-end',
  };

  return (
    <div style={overlayStyle} onClick={onClose}>
      <div style={modalStyle} onClick={(e) => e.stopPropagation()}>
        {title && (
          <div style={headerStyle}>
            <h2 style={{ margin: 0, fontSize: fontSize['2xl'], fontWeight: fontWeight.semibold }}>
              {title}
            </h2>
            <button
              onClick={onClose}
              style={{
                background: 'transparent',
                border: 'none',
                color: colors.textSecondary,
                fontSize: fontSize['2xl'],
                cursor: 'pointer',
                padding: 0,
                width: '32px',
                height: '32px',
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              ×
            </button>
          </div>
        )}
        <div style={bodyStyle}>{children}</div>
        {footer && <div style={footerStyle}>{footer}</div>}
      </div>
    </div>
  );
}
