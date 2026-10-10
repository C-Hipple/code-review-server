import type { ButtonHTMLAttributes, ReactNode } from 'react';
import { Spinner } from './StatusChip';

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
    variant?: 'default' | 'primary' | 'invisible';
    size?: 'sm' | 'md';
    /** Shows a spinner in place of the icon and disables the button. */
    busy?: boolean;
    icon?: ReactNode;
}

export function Button({
    variant = 'default',
    size = 'md',
    busy = false,
    icon,
    children,
    className,
    disabled,
    type = 'button',
    ...rest
}: ButtonProps) {
    const classes = ['btn', `btn-${variant}`, `btn-${size}`, className].filter(Boolean).join(' ');
    return (
        <button
            type={type}
            className={classes}
            disabled={disabled || busy}
            aria-busy={busy || undefined}
            {...rest}
        >
            {busy ? <Spinner /> : icon}
            {children}
        </button>
    );
}
