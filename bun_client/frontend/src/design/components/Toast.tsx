import React, { useEffect, useRef, useState } from 'react';
import { colors, borderRadius, spacing, shadows } from '../styles/tokens';
import { fontSize, fontWeight } from '../styles/typography';
import { getStatusColor, type StatusVariant } from '../styles/utils';

export interface ToastProps {
    /** Message to display */
    message: string;
    /** Visual variant of the toast */
    variant?: StatusVariant;
    /** Called when the toast should be removed (after the timeout or a click) */
    onDismiss: () => void;
    /** Auto-dismiss delay in milliseconds */
    duration?: number;
    /** Distance from the bottom of the viewport, e.g. to clear a mobile action bar */
    bottomOffset?: number;
}

/**
 * Toast notification that floats at the bottom center of the viewport,
 * fades in on mount, and auto-dismisses after `duration`. Click to dismiss
 * early. Render with a changing `key` to restart the timer for a new message.
 */
export function Toast({
    message,
    variant = 'info',
    onDismiss,
    duration = 4000,
    bottomOffset = 24,
}: ToastProps) {
    const [visible, setVisible] = useState(false);

    // Keep the latest onDismiss without re-arming the timers when the parent
    // re-renders and passes a new function identity.
    const onDismissRef = useRef(onDismiss);
    onDismissRef.current = onDismiss;

    const exitMs = 200;

    useEffect(() => {
        const showTimer = setTimeout(() => setVisible(true), 10);
        const hideTimer = setTimeout(() => setVisible(false), Math.max(duration - exitMs, 0));
        const dismissTimer = setTimeout(() => onDismissRef.current(), duration);
        return () => {
            clearTimeout(showTimer);
            clearTimeout(hideTimer);
            clearTimeout(dismissTimer);
        };
    }, [duration]);

    const toastStyle: React.CSSProperties = {
        position: 'fixed',
        bottom: `${bottomOffset}px`,
        left: '50%',
        transform: visible ? 'translate(-50%, 0)' : 'translate(-50%, 8px)',
        opacity: visible ? 1 : 0,
        transition: `opacity ${exitMs}ms ease, transform ${exitMs}ms ease`,
        zIndex: 1100,
        display: 'flex',
        alignItems: 'center',
        gap: spacing.sm,
        maxWidth: 'calc(100vw - 32px)',
        padding: `${spacing.md} ${spacing.lg}`,
        background: colors.bgSecondary,
        border: `1px solid ${colors.border}`,
        borderLeft: `3px solid ${getStatusColor(variant)}`,
        borderRadius: borderRadius.md,
        boxShadow: shadows.lg,
        color: colors.textPrimary,
        fontSize: fontSize.md,
        fontWeight: fontWeight.medium,
        cursor: 'pointer',
        whiteSpace: 'pre-line',
    };

    return (
        <div role="status" aria-live="polite" style={toastStyle} onClick={onDismiss}>
            {message}
        </div>
    );
}
