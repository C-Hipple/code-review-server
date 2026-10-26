// The PR tools view's data: GetPR first (it fills the server's caches for a
// PR in no list and starts the PR's automatic plugins and applied AI
// features, as opening a PR in any client does), the feature and plugin
// lists alongside it, then the outputs — read again whenever the server
// announces that one changed, never polled.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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
import { coalesce } from './coalesce';
import {
    automaticPluginNames,
    PENDING_PLUGIN,
    rerunsStillAwaited,
    withAwaitedReruns,
} from './plugin_utils';
import { PRToolsView, type PRToolsHandlers } from './PRToolsView';
import { failed, isFatal, loaded, loading, settled, toRpcError, type Resource } from './resource';
import { useNow } from './useNow';
import { usePRChanges } from './usePRChanges';

type Outputs<T> = Resource<Record<string, T>>;

function withEntries<T>(prev: Outputs<T>, entries: Record<string, T>): Outputs<T> {
    return { ...prev, value: { ...(prev.value ?? {}), ...entries } };
}

export interface PRToolsProps {
    pr: PullRef;
    /** An AI feature id or plugin name to bring into view, from a chip on the page. */
    focus?: string | null;
    /** The PR's review-list entry, when the list is loaded and has it. */
    listItem?: ReviewItem | null;
    extensionId?: string;
    /** Called after calls settle, so the header's connection dot catches up. */
    onActivity?: () => void;
}

export function PRTools({ pr, focus, listItem, extensionId, onActivity }: PRToolsProps) {
    const [payload, setPayload] = useState<Resource<PRPayload>>(() => loading());
    const [features, setFeatures] = useState<Resource<AIFeature[]>>(() => loading());
    const [plugins, setPlugins] = useState<Resource<PluginConfig[]>>(() => loading());
    const [aiOutputs, setAiOutputs] = useState<Outputs<AIFeatureOutput>>(() => loading());
    const [pluginOutputs, setPluginOutputs] = useState<Outputs<PluginOutput>>(() => loading());
    const [actions, setActions] = useState<ActionState>(NO_ACTIONS);
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

    // Reads of each kind of output, counted so only the latest one's reply is
    // applied (replies can arrive out of order), and so a run's reply isn't
    // applied over a read that started after the run did.
    const aiReads = useRef(0);
    const pluginReads = useRef(0);
    // Plugin reruns accepted whose result hasn't been read yet.
    const awaiting = useRef<Set<string>>(new Set());

    const fetchAIOutputs = useCallback(async () => {
        const read = ++aiReads.current;
        const [ai] = await Promise.allSettled([getAIOutput(pr)]);
        if (!alive.current || read !== aiReads.current) return;
        setAiOutputs(prev => settled(ai, prev));
        activity.current?.();
    }, [pr]);

    const fetchPluginOutputs = useCallback(async () => {
        const read = ++pluginReads.current;
        let [pl] = await Promise.allSettled([getPluginOutput(pr)]);
        if (!alive.current || read !== pluginReads.current) return;
        if (pl.status === 'fulfilled') {
            awaiting.current = rerunsStillAwaited(awaiting.current, pl.value);
            pl = { status: 'fulfilled', value: withAwaitedReruns(pl.value, awaiting.current) };
        }
        setPluginOutputs(prev => settled(pl, prev));
        activity.current?.();
    }, [pr]);

    const fetchOutputs = useCallback(async () => {
        await Promise.all([fetchAIOutputs(), fetchPluginOutputs()]);
    }, [fetchAIOutputs, fetchPluginOutputs]);

    // What the server's change events ask for: one read of a kind at a time,
    // however many arrive while it's in flight.
    const refreshAI = useMemo(() => coalesce(fetchAIOutputs), [fetchAIOutputs]);
    const refreshPlugins = useMemo(() => coalesce(fetchPluginOutputs), [fetchPluginOutputs]);

    usePRChanges(pr, change => {
        switch (change.kind) {
            case 'plugins':
                void refreshPlugins();
                return;
            case 'ai':
                void refreshAI();
                return;
            case 'resync':
                // A new server ran nothing the old one was running.
                awaiting.current = new Set();
                void refreshAI();
                void refreshPlugins();
        }
    });

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
        // GetPR has started the automatic runs; read the outputs. Each run
        // is announced as it starts and lands, and read again then.
        await fetchOutputs();
    }, [pr, fetchOutputs]);

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

    const runFeature = useCallback(
        async (feature: AIFeature, force: boolean) => {
            const key = aiKey(feature.id);
            setActions(a => begin(a, key));
            const reads = aiReads.current;
            try {
                const reply = await runAIFeature(pr, feature.id, { force });
                if (!alive.current) return;
                const output = reply.output;
                // A read that started since (the run's own start, announced)
                // has the newer word.
                if (output && reads === aiReads.current) {
                    setAiOutputs(prev => withEntries(prev, { [feature.id]: output }));
                }
                if (reply.okay) {
                    setActions(a => finish(a, key, runOutcomeNote(reply.outcome)));
                } else {
                    setActions(a =>
                        fail(a, key, new RpcError('rpc', reply.message || reply.outcome))
                    );
                }
            } catch (e) {
                if (alive.current) setActions(a => fail(a, key, toRpcError(e)));
            }
        },
        [pr]
    );

    const runPlugins = useCallback(
        async (names: string[], key: string) => {
            setActions(a => begin(a, key));
            const reads = pluginReads.current;
            try {
                const reply = await rerunPlugins(pr, names);
                if (!alive.current) return;
                if (!reply.okay) {
                    setActions(a => fail(a, key, new RpcError('rpc', reply.message)));
                    return;
                }
                // The server cleared the results and runs the plugins in the
                // background, announcing each as it starts and lands. Until a
                // read shows one landed, it reads as running.
                for (const name of names) awaiting.current.add(name);
                if (reads === pluginReads.current) {
                    setPluginOutputs(prev =>
                        withEntries(prev, Object.fromEntries(names.map(n => [n, PENDING_PLUGIN])))
                    );
                }
                setActions(a => finish(a, key));
            } catch (e) {
                if (alive.current) setActions(a => fail(a, key, toRpcError(e)));
            }
        },
        [pr]
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
            if (reply.updated) await fetchOutputs();
        } catch (e) {
            if (alive.current) setActions(a => fail(a, SYNC, toRpcError(e)));
        }
    }, [pr, fetchOutputs]);

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
            focus={focus}
            listItem={listItem}
            extensionId={extensionId}
        />
    );
}
