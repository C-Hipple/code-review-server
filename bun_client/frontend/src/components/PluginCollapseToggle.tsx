import type React from 'react';

interface PluginCollapseToggleProps {
    name: string;
    collapsed: boolean;
    /** Lines the output would show, noted while it's collapsed. */
    lineCount: number;
    onToggle: () => void;
    style?: React.CSSProperties;
}

/**
 * The plugin's name in its card header, doubling as the button that collapses
 * or expands the plugin's output.
 */
export default function PluginCollapseToggle({
    name,
    collapsed,
    lineCount,
    onToggle,
    style,
}: PluginCollapseToggleProps) {
    return (
        <button
            type="button"
            onClick={onToggle}
            aria-expanded={!collapsed}
            title={collapsed ? `Expand ${name}` : `Collapse ${name}`}
            style={{
                display: 'flex',
                alignItems: 'center',
                gap: '8px',
                minWidth: 0,
                padding: 0,
                border: 'none',
                background: 'transparent',
                color: 'var(--text-primary)',
                fontFamily: 'inherit',
                fontWeight: 600,
                textAlign: 'left',
                cursor: 'pointer',
                ...style,
            }}
        >
            <span
                aria-hidden="true"
                style={{
                    display: 'inline-block',
                    transform: collapsed ? 'rotate(-90deg)' : 'rotate(0deg)',
                    transition: 'transform 0.15s ease',
                    fontSize: '10px',
                    color: 'var(--text-secondary)',
                }}
            >
                ▼
            </span>
            <span>{name}</span>
            {collapsed && lineCount > 0 && (
                <span
                    style={{
                        fontSize: '12px',
                        fontWeight: 400,
                        color: 'var(--text-secondary)',
                    }}
                >
                    {lineCount} line{lineCount === 1 ? '' : 's'}
                </span>
            )}
        </button>
    );
}
