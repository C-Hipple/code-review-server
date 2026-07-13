import Markdown from 'react-markdown';
import { Button, colors, shadows } from '../../design';
import type { PluginResult } from './types';

interface PluginsPanelProps {
    pluginOutputs: Record<string, PluginResult>;
    executingPlugins: Set<string>;
    onRefresh: () => void;
    onExecutePlugin: (name: string) => void;
    onClose: () => void;
}

// Right-hand drawer showing each plugin's status and (markdown) output.
export default function PluginsPanel({
    pluginOutputs,
    executingPlugins,
    onRefresh,
    onExecutePlugin,
    onClose,
}: PluginsPanelProps) {
    return (
        <div
            style={{
                position: 'fixed' as const,
                top: 0,
                left: 0,
                right: 0,
                bottom: 0,
                background: colors.overlayBg,
                display: 'flex',
                justifyContent: 'flex-end',
                zIndex: 100,
            }}
            onClick={onClose}
        >
            <div
                style={{
                    background: 'var(--bg-secondary)',
                    width: '600px',
                    maxWidth: '90vw',
                    height: '100vh',
                    borderLeft: '1px solid var(--border)',
                    boxShadow: shadows.lg,
                    display: 'flex',
                    flexDirection: 'column',
                    overflow: 'hidden',
                }}
                onClick={e => e.stopPropagation()}
            >
                <div
                    style={{
                        display: 'flex',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        padding: '20px 24px',
                        background: 'var(--bg-secondary)',
                        zIndex: 1,
                        borderBottom: '1px solid var(--border)',
                    }}
                >
                    <h2 style={{ fontSize: '18px', margin: 0, color: 'var(--accent)' }}>
                        Plugin Analysis
                    </h2>
                    <div style={{ display: 'flex', gap: '8px' }}>
                        <Button onClick={onRefresh} variant="secondary" size="sm">
                            Refresh
                        </Button>
                        <Button onClick={onClose} variant="secondary" size="sm">
                            Close (Esc)
                        </Button>
                    </div>
                </div>
                <div style={{ flex: 1, overflowY: 'auto', padding: '24px' }}>
                    {Object.keys(pluginOutputs).length === 0 ? (
                        <div
                            style={{
                                color: 'var(--text-secondary)',
                                fontStyle: 'italic',
                                padding: '20px',
                                textAlign: 'center',
                            }}
                        >
                            No plugin output found.
                        </div>
                    ) : (
                        <div
                            style={{
                                display: 'flex',
                                flexDirection: 'column',
                                gap: '20px',
                            }}
                        >
                            {Object.entries(pluginOutputs).map(([name, data]) => (
                                <div
                                    key={name}
                                    style={{
                                        background: 'var(--bg-primary)',
                                        border: '1px solid var(--border)',
                                        borderRadius: '8px',
                                        overflow: 'hidden',
                                    }}
                                >
                                    <div
                                        style={{
                                            padding: '10px 15px',
                                            background: 'var(--bg-secondary)',
                                            borderBottom: '1px solid var(--border)',
                                            display: 'flex',
                                            justifyContent: 'space-between',
                                            alignItems: 'center',
                                        }}
                                    >
                                        <span style={{ fontWeight: 600, fontSize: '14px' }}>
                                            {name}
                                        </span>
                                        <span
                                            style={{
                                                fontSize: '11px',
                                                padding: '2px 10px',
                                                borderRadius: '12px',
                                                fontWeight: 600,
                                                background:
                                                    data.status === 'success'
                                                        ? colors.bgSuccessDim
                                                        : data.status === 'pending'
                                                          ? colors.bgWarningDim
                                                          : colors.bgDangerDim,
                                                color:
                                                    data.status === 'success'
                                                        ? colors.textSuccess
                                                        : data.status === 'pending'
                                                          ? colors.textWarning
                                                          : colors.textDanger,
                                                border: `1px solid ${data.status === 'success' ? colors.borderSuccessDim : data.status === 'pending' ? colors.borderWarningDim : colors.borderDangerDim}`,
                                            }}
                                        >
                                            {data.status.toUpperCase()}
                                        </span>
                                        {data.status.toLowerCase() === 'deferred' && (
                                            <Button
                                                onClick={() => onExecutePlugin(name)}
                                                loading={executingPlugins.has(name)}
                                                variant="secondary"
                                                size="sm"
                                            >
                                                Execute
                                            </Button>
                                        )}
                                    </div>
                                    <div
                                        className="plugin-output markdown-content"
                                        style={{
                                            padding: '15px',
                                            fontSize: '14px',
                                            lineHeight: '1.6',
                                            color: 'var(--text-primary)',
                                            background: 'var(--bg-primary)',
                                        }}
                                    >
                                        {data.result ? (
                                            <Markdown>{data.result}</Markdown>
                                        ) : (
                                            <span
                                                style={{
                                                    color: 'var(--text-secondary)',
                                                    fontStyle: 'italic',
                                                }}
                                            >
                                                No output produced.
                                            </span>
                                        )}
                                    </div>
                                </div>
                            ))}
                        </div>
                    )}
                </div>
            </div>
        </div>
    );
}
