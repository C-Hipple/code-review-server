// The PR tools view's data: GetPR first (it fills the server's caches for a
// PR in no list and starts the PR's automatic plugins and applied AI
// features, as opening a PR in any client does), the feature and plugin
// lists alongside it, then the outputs — polled while anything runs.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { PullRef } from '../github_url';
import {
    getAIOutput,
    getPluginOutput,
    getPR,
    listAIFeatures,
    listPlugins,
    rerunPlugins,
    RpcError,
    runAIFeature,
    syncPR,
} from '../rpc';
import type {
    AIFeature,
    AIFeatureOutput,
    PluginConfig,
    PluginOutput,
    PRPayload,
    ReviewItem,
} from '../types';
import {
    aiKey,
    ALL_PLUGINS,
    begin,
    fail,
    finish,
    NO_ACTIONS,
    pluginKey,
    SYNC,
    type ActionState,
} from './actions';
import { runOutcomeNote } from './ai_utils';
import { automaticPluginNames } from './plugin_utils';
import { REQUEST_GRACE_MS, shouldPoll } from './poll_utils';
import { PRToolsView, type PRToolsHandlers } from './PRToolsView';
import { failed, isFatal, loaded, loading, settled, toRpcError, type Resource } from './resource';
import { useNow, usePolling } from './usePolling';

type Outputs<T> = Resource<Record<string, T>>;

/** What a plugin reads as from the moment its rerun is accepted. */
const PENDING_PLUGIN: PluginOutput = {
    result: '',
    status: 'pending',
    body: { body_type: 'markdown', body_content: '' },
    annotations: null,
};

function withEntries<T>(prev: Outputs<T>, entries: Record<string, T>): Outputs<T> {
    return { ...prev, value: { ...(prev.value ?? {}), ...entries } };
}

export interface PRToolsProps {
    pr: PullRef;
    /** The PR's review-list entry, when the list is loaded and has it. */
    listItem?: ReviewItem | null;
    extensionId?: string;
    /** Called after calls settle, so the header's connection dot catches up. */
    onActivity?: () => void;
}

export function PRTools({ pr, listItem, extensionId, onActivity }: PRToolsProps) {
    const [payload, setPayload] = useState<Resource<PRPayload>>(() => loading());
    const [features, setFeatures] = useState<Resource<AIFeature[]>>(() => loading());
    const [plugins, setPlugins] = useState<Resource<PluginConfig[]>>(() => loading());
    const [aiOutputs, setAiOutputs] = useState<Outputs<AIFeatureOutput>>(() => loading());
    const [pluginOutputs, setPluginOutputs] = useState<Outputs<PluginOutput>>(() => loading());
    const [actions, setActions] = useState<ActionState>(NO_ACTIONS);
    // Runs requested vs. runs whose grace period is over: polling continues
    // while they differ, even before anything reads `pending`.
    const [requests, setRequests] = useState({ made: 0, expired: 0 });
    const [attempt, setAttempt] = useState(0);
    const now = useNow();

    const alive = useRef(true);
    useEffect(() => {
        alive.current = true;
        return () => {
            alive.current = false;
        };
    }, []);

    const activity = useRef(onActivity);
    useEffect(() => {
        activity.current = onActivity;
    });

    const markRequested = useCallback(() => {
        setRequests(r => ({ ...r, made: r.made + 1 }));
    }, []);

    useEffect(() => {
        if (requests.made === requests.expired) return;
        const made = requests.made;
        const timer = setTimeout(
            () => setRequests(r => ({ ...r, expired: Math.max(r.expired, made) })),
            REQUEST_GRACE_MS
        );
        return () => clearTimeout(timer);
    }, [requests.made, requests.expired]);

    const fetchOutputs = useCallback(async () => {
        const [ai, pl] = await Promise.allSettled([getAIOutput(pr), getPluginOutput(pr)]);
        if (!alive.current) return;
        setAiOutputs(prev => settled(ai, prev));
        setPluginOutputs(prev => settled(pl, prev));
        activity.current?.();
    }, [pr]);

    const fetchPR = useCallback(async () => {
        try {
            const value = await getPR(pr);
            if (!alive.current) return;
            setPayload(loaded(value));
        } catch (e) {
            if (!alive.current) return;
            setPayload(prev => failed(e, prev));
            if (isFatal(toRpcError(e).kind)) return;
        } finally {
            activity.current?.();
        }
        // GetPR has started the automatic runs; read the outputs, and keep
        // reading for a while in case those runs haven't been marked yet.
        markRequested();
        await fetchOutputs();
    }, [pr, fetchOutputs, markRequested]);

    const fetchFeatures = useCallback(() => {
        listAIFeatures().then(
            v => alive.current && setFeatures(loaded(v)),
            e => alive.current && setFeatures(prev => failed(e, prev))
        );
    }, []);

    const fetchPlugins = useCallback(() => {
        listPlugins().then(
            v => alive.current && setPlugins(loaded(v)),
            e => alive.current && setPlugins(prev => failed(e, prev))
        );
    }, []);

    useEffect(() => {
        void fetchPR();
        fetchFeatures();
        fetchPlugins();
    }, [fetchPR, fetchFeatures, fetchPlugins, attempt]);

    usePolling(
        shouldPoll({
            aiOutputs: aiOutputs.value,
            pluginOutputs: pluginOutputs.value,
            justRequested: requests.made > requests.expired,
        }),
        fetchOutputs
    );

    const runFeature = useCallback(
        async (feature: AIFeature, force: boolean) => {
            const key = aiKey(feature.id);
            setActions(a => begin(a, key));
            try {
                const reply = await runAIFeature(pr, feature.id, { force });
                if (!alive.current) return;
                const output = reply.output;
                if (output) setAiOutputs(prev => withEntries(prev, { [feature.id]: output }));
                if (reply.okay) {
                    setActions(a => finish(a, key, runOutcomeNote(reply.outcome)));
                    markRequested();
                } else {
                    setActions(a =>
                        fail(a, key, new RpcError('rpc', reply.message || reply.outcome))
                    );
                }
            } catch (e) {
                if (alive.current) setActions(a => fail(a, key, toRpcError(e)));
            }
        },
        [pr, markRequested]
    );

    const runPlugins = useCallback(
        async (names: string[], key: string) => {
            setActions(a => begin(a, key));
            try {
                const reply = await rerunPlugins(pr, names);
                if (!alive.current) return;
                if (!reply.okay) {
                    setActions(a => fail(a, key, new RpcError('rpc', reply.message)));
                    return;
                }
                // The server clears the results and runs them in the background.
                setPluginOutputs(prev =>
                    withEntries(prev, Object.fromEntries(names.map(n => [n, PENDING_PLUGIN])))
                );
                setActions(a => finish(a, key));
                markRequested();
            } catch (e) {
                if (alive.current) setActions(a => fail(a, key, toRpcError(e)));
            }
        },
        [pr, markRequested]
    );

    const sync = useCallback(async () => {
        setActions(a => begin(a, SYNC));
        try {
            const reply = await syncPR(pr);
            if (!alive.current) return;
            setPayload(loaded(reply));
            setActions(a =>
                finish(
                    a,
                    SYNC,
                    reply.updated
                        ? 'Synced: pulled in new commits or comments.'
                        : 'Synced: already up to date with GitHub.'
                )
            );
            if (reply.updated) {
                markRequested();
                await fetchOutputs();
            }
        } catch (e) {
            if (alive.current) setActions(a => fail(a, SYNC, toRpcError(e)));
        }
    }, [pr, fetchOutputs, markRequested]);

    const handlers: PRToolsHandlers = {
        onRetryAll: () => {
            setPayload(loading());
            setFeatures(loading());
            setPlugins(loading());
            setAiOutputs(loading());
            setPluginOutputs(loading());
            setAttempt(n => n + 1);
        },
        onRetryPR: () => {
            setPayload(prev => loading(prev));
            void fetchPR();
        },
        onRetryFeatures: () => {
            setFeatures(prev => loading(prev));
            fetchFeatures();
        },
        onRetryPlugins: () => {
            setPlugins(prev => loading(prev));
            fetchPlugins();
        },
        onRetryOutputs: () => {
            setAiOutputs(prev => loading(prev));
            setPluginOutputs(prev => loading(prev));
            void fetchOutputs();
        },
        onSync: () => void sync(),
        onRunFeature: (feature, force) => void runFeature(feature, force),
        onRunPlugin: name => void runPlugins([name], pluginKey(name)),
        onRerunAll: () => void runPlugins(automaticPluginNames(plugins.value ?? []), ALL_PLUGINS),
    };

    return (
        <PRToolsView
            pr={pr}
            data={{ payload, features, plugins, aiOutputs, pluginOutputs }}
            actions={actions}
            handlers={handlers}
            now={now}
            listItem={listItem}
            extensionId={extensionId}
        />
    );
}
