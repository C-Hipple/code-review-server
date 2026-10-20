import { useEffect } from 'react';
import { Button, Badge, Card, mapStatusToVariant, Theme, type StatusVariant } from '../design';
import { usePluginCollapse } from '../hooks/usePluginCollapse';
import { usePluginOutputs } from '../hooks/usePluginOutputs';
import {
    canRerunPlugin,
    pluginOutputLineCount,
    pluginRerunFailedMessage,
    resolvePluginBody,
    sortedAnnotations,
} from '../plugin_utils';
import PluginAnnotations from './PluginAnnotations';
import PluginBodyView from './PluginBodyView';
import PluginCollapseToggle from './PluginCollapseToggle';

interface PluginOutputProps {
    owner: string;
    repo: string;
    number: number;
    theme: Theme;
    onThemeChange: (theme: Theme) => void;
    onClose?: () => void;
    onToast: (message: string, variant?: StatusVariant) => void;
}

export default function PluginOutput({
    owner,
    repo,
    number,
    theme: _theme,
    onThemeChange: _onThemeChange,
    onClose,
    onToast,
}: PluginOutputProps) {
    const {
        outputs: pluginOutput,
        loading,
        executing: executingPlugins,
        load: loadPluginOutput,
        execute: executePlugin,
    } = usePluginOutputs(owner, repo, number, (name, e) =>
        onToast(pluginRerunFailedMessage(name, e), 'danger')
    );
    const { isCollapsed, toggle } = usePluginCollapse(pluginOutput);

    useEffect(() => {
        if (!onClose) return;
        const handleKeyDown = (e: KeyboardEvent) => {
            if (e.key === 'Escape') {
                onClose();
            }
        };
        window.addEventListener('keydown', handleKeyDown);
        return () => window.removeEventListener('keydown', handleKeyDown);
    }, [onClose]);

    const pluginNames = Object.keys(pluginOutput);

    return (
        <div className="plugin-output">
            <Card padding="lg" style={{ marginBottom: '20px' }}>
                <div
                    style={{
                        display: 'flex',
                        justifyContent: 'space-between',
                        alignItems: 'center',
                        marginBottom: '15px',
                    }}
                >
                    <h2 style={{ margin: 0, fontSize: '18px' }}>
                        Plugin Output for {owner}/{repo} #{number}
                    </h2>
                    <div style={{ display: 'flex', gap: '12px', alignItems: 'center' }}>
                        <Button onClick={loadPluginOutput} loading={loading} variant="secondary">
                            Refresh
                        </Button>
                        {onClose && (
                            <Button onClick={onClose} variant="secondary">
                                Close (Esc)
                            </Button>
                        )}
                    </div>
                </div>

                {loading ? (
                    <div
                        style={{
                            padding: '40px',
                            textAlign: 'center',
                            color: 'var(--text-secondary)',
                        }}
                    >
                        Loading plugin output...
                    </div>
                ) : pluginNames.length === 0 ? (
                    <div
                        style={{
                            padding: '40px',
                            textAlign: 'center',
                            color: 'var(--text-secondary)',
                            fontStyle: 'italic',
                        }}
                    >
                        No plugin output available for this PR
                    </div>
                ) : (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
                        {pluginNames.map(pluginName => {
                            const plugin = pluginOutput[pluginName];
                            const body = resolvePluginBody(plugin);
                            const annotations = sortedAnnotations(plugin);
                            const collapsed = isCollapsed(pluginName);
                            return (
                                <Card
                                    key={pluginName}
                                    variant="outlined"
                                    padding="none"
                                    style={{ overflow: 'hidden' }}
                                >
                                    <div
                                        style={{
                                            padding: '12px 16px',
                                            background: 'var(--bg-tertiary)',
                                            borderBottom: collapsed
                                                ? 'none'
                                                : '1px solid var(--border)',
                                            display: 'flex',
                                            alignItems: 'center',
                                            gap: '10px',
                                        }}
                                    >
                                        {canRerunPlugin(plugin) && (
                                            <Button
                                                onClick={() => executePlugin(pluginName)}
                                                loading={executingPlugins.has(pluginName)}
                                                variant="secondary"
                                                size="sm"
                                                title={`Re-run ${pluginName}`}
                                            >
                                                Re-run
                                            </Button>
                                        )}
                                        <Badge variant={mapStatusToVariant(plugin.status)}>
                                            {plugin.status}
                                        </Badge>
                                        <PluginCollapseToggle
                                            name={pluginName}
                                            collapsed={collapsed}
                                            lineCount={pluginOutputLineCount(plugin)}
                                            onToggle={() => toggle(pluginName)}
                                            style={{
                                                fontSize: '15px',
                                                fontFamily: 'var(--font-mono)',
                                                flex: 1,
                                            }}
                                        />
                                        {annotations.length > 0 && (
                                            <Badge variant="info" size="sm" pill>
                                                {annotations.length} annotation
                                                {annotations.length === 1 ? '' : 's'}
                                            </Badge>
                                        )}
                                        {plugin.status.toLowerCase() === 'deferred' && (
                                            <Button
                                                onClick={() => executePlugin(pluginName)}
                                                loading={executingPlugins.has(pluginName)}
                                                variant="secondary"
                                                size="sm"
                                            >
                                                Execute
                                            </Button>
                                        )}
                                    </div>
                                    {!collapsed && (
                                        <>
                                            <div
                                                className="markdown-content"
                                                style={{
                                                    padding: '16px',
                                                    maxHeight: '400px',
                                                    overflowY: 'auto',
                                                    fontSize: '14px',
                                                    lineHeight: '1.6',
                                                    color: 'var(--text-primary)',
                                                }}
                                            >
                                                <PluginBodyView body={body} />
                                            </div>
                                            <PluginAnnotations annotations={annotations} />
                                        </>
                                    )}
                                </Card>
                            );
                        })}
                    </div>
                )}
            </Card>
        </div>
    );
}
