import { useState, useEffect } from 'react';
import { rpcCall } from '../api';
import { Button, Badge, Card, mapStatusToVariant, Theme } from '../design';
import { resolvePluginBody, sortedAnnotations, type PluginResult } from '../plugin_utils';
import PluginAnnotations from './PluginAnnotations';
import PluginBodyView from './PluginBodyView';

interface PluginOutputProps {
    owner: string;
    repo: string;
    number: number;
    theme: Theme;
    onThemeChange: (theme: Theme) => void;
    onClose?: () => void;
}

interface GetPluginOutputResponse {
    output: Record<string, PluginResult>;
}

export default function PluginOutput({
    owner,
    repo,
    number,
    theme: _theme,
    onThemeChange: _onThemeChange,
    onClose,
}: PluginOutputProps) {
    const [loading, setLoading] = useState(false);
    const [pluginOutput, setPluginOutput] = useState<Record<string, PluginResult>>({});
    const [executingPlugins, setExecutingPlugins] = useState<Set<string>>(new Set());

    useEffect(() => {
        loadPluginOutput();
    }, [owner, repo, number]);

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

    const loadPluginOutput = async () => {
        setLoading(true);
        try {
            const response = await rpcCall<GetPluginOutputResponse>('RPCHandler.GetPluginOutput', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                },
            ]);
            setPluginOutput(response.output || {});
        } catch (e) {
            console.error('Failed to load plugin output:', e);
            setPluginOutput({});
        } finally {
            setLoading(false);
        }
    };

    const executePlugin = async (pluginName: string) => {
        setExecutingPlugins((prev: Set<string>) => new Set(prev).add(pluginName));
        try {
            await rpcCall('RPCHandler.RerunPlugins', [
                {
                    Owner: owner,
                    Repo: repo,
                    Number: number,
                    Plugins: [pluginName],
                },
            ]);
            await loadPluginOutput();
        } catch (e) {
            console.error('Failed to execute plugin:', e);
        } finally {
            setExecutingPlugins((prev: Set<string>) => {
                const next = new Set(prev);
                next.delete(pluginName);
                return next;
            });
        }
    };

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
                                            borderBottom: '1px solid var(--border)',
                                            display: 'flex',
                                            alignItems: 'center',
                                            gap: '10px',
                                        }}
                                    >
                                        <Badge variant={mapStatusToVariant(plugin.status)}>
                                            {plugin.status}
                                        </Badge>
                                        <span
                                            style={{
                                                fontWeight: 600,
                                                fontSize: '15px',
                                                fontFamily: 'var(--font-mono)',
                                                flex: 1,
                                            }}
                                        >
                                            {pluginName}
                                        </span>
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
                                </Card>
                            );
                        })}
                    </div>
                )}
            </Card>
        </div>
    );
}
