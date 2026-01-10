import { useState, useEffect } from 'react';
import { rpcCall } from '../api';
import { Button, Badge, Card, mapStatusToVariant } from '../design';

interface PluginOutputProps {
    owner: string;
    repo: string;
    number: number;
}

interface PluginResult {
    result: string;
    status: string;
}

interface GetPluginOutputResponse {
    output: Record<string, PluginResult>;
}

export default function PluginOutput({ owner, repo, number }: PluginOutputProps) {
    const [loading, setLoading] = useState(false);
    const [pluginOutput, setPluginOutput] = useState<Record<string, PluginResult>>({});

    useEffect(() => {
        loadPluginOutput();
    }, [owner, repo, number]);

    const loadPluginOutput = async () => {
        setLoading(true);
        try {
            const response = await rpcCall<GetPluginOutputResponse>('RPCHandler.GetPluginOutput', [{
                Owner: owner,
                Repo: repo,
                Number: number
            }]);
            setPluginOutput(response.output || {});
        } catch (e) {
            console.error('Failed to load plugin output:', e);
            setPluginOutput({});
        } finally {
            setLoading(false);
        }
    };

    const pluginNames = Object.keys(pluginOutput);

    return (
        <div className="plugin-output">
            <Card padding="lg" style={{ marginBottom: '20px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '15px' }}>
                    <h2 style={{ margin: 0, fontSize: '18px' }}>
                        Plugin Output for {owner}/{repo} #{number}
                    </h2>
                    <Button onClick={loadPluginOutput} loading={loading}>
                        Refresh
                    </Button>
                </div>

                {loading ? (
                    <div style={{ padding: '40px', textAlign: 'center', color: 'var(--text-secondary)' }}>
                        Loading plugin output...
                    </div>
                ) : pluginNames.length === 0 ? (
                    <div style={{
                        padding: '40px',
                        textAlign: 'center',
                        color: 'var(--text-secondary)',
                        fontStyle: 'italic'
                    }}>
                        No plugin output available for this PR
                    </div>
                ) : (
                    <div style={{ display: 'flex', flexDirection: 'column', gap: '16px' }}>
                        {pluginNames.map(pluginName => {
                            const plugin = pluginOutput[pluginName];
                            return (
                                <Card key={pluginName} variant="outlined" padding="none" style={{ overflow: 'hidden' }}>
                                    <div style={{
                                        padding: '12px 16px',
                                        background: 'var(--bg-tertiary)',
                                        borderBottom: '1px solid var(--border)',
                                        display: 'flex',
                                        alignItems: 'center',
                                        gap: '10px'
                                    }}>
                                        <Badge variant={mapStatusToVariant(plugin.status)}>
                                            {plugin.status}
                                        </Badge>
                                        <span style={{
                                            fontWeight: 600,
                                            fontSize: '15px',
                                            fontFamily: 'var(--font-mono)'
                                        }}>
                                            {pluginName}
                                        </span>
                                    </div>
                                    <div style={{
                                        padding: '16px',
                                        maxHeight: '400px',
                                        overflowY: 'auto'
                                    }}>
                                        {plugin.result ? (
                                            <pre style={{
                                                margin: 0,
                                                fontSize: '13px',
                                                lineHeight: '1.5',
                                                fontFamily: 'var(--font-mono)',
                                                whiteSpace: 'pre-wrap',
                                                wordBreak: 'break-word',
                                                color: 'var(--text-primary)'
                                            }}>
                                                {plugin.result}
                                            </pre>
                                        ) : (
                                            <div style={{
                                                color: 'var(--text-secondary)',
                                                fontStyle: 'italic',
                                                fontSize: '14px'
                                            }}>
                                                No output
                                            </div>
                                        )}
                                    </div>
                                </Card>
                            );
                        })}
                    </div>
                )}
            </Card>
        </div>
    );
}
