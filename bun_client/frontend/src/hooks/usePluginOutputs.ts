import { useEffect, useMemo, useRef, useState } from 'react';
import { rpcCall } from '../api';
import {
    MAX_PLUGIN_POLL_MS,
    PLUGIN_POLL_MS,
    pendingPluginsKey,
    rerunsStillAwaited,
    withAwaitedReruns,
    type PluginResult,
} from '../plugin_utils';

export interface PluginOutputs {
    /** Each plugin's latest output; a re-run whose result is yet to land reads as pending. */
    outputs: Record<string, PluginResult>;
    /** Whether a load someone asked for is in flight. Polls don't count. */
    loading: boolean;
    /** Plugins whose re-run request is in flight. */
    executing: Set<string>;
    /** Reads every plugin's output again. */
    load: () => void;
    /** Re-runs (or, for a deferred plugin, runs) one plugin. */
    execute: (name: string) => void;
}

/**
 * A PR's plugin output, kept current while plugins run.
 *
 * The server runs a plugin in the background, so neither a re-run nor the
 * first automatic run has a result to hand back right away. While any plugin
 * is pending the output is read again every two seconds, one read at a time,
 * until each has landed — or until a plugin has been pending for longer than
 * the server lets one run.
 */
export function usePluginOutputs(owner: string, repo: string, number: number): PluginOutputs {
    const pr = `${owner}/${repo}#${number}`;
    // The output as the server last served it, and the re-runs it doesn't
    // show the result of yet.
    const [stored, setStored] = useState<Record<string, PluginResult>>({});
    const [awaiting, setAwaiting] = useState<Set<string>>(new Set());
    const [executing, setExecuting] = useState<Set<string>>(new Set());
    const [loading, setLoading] = useState(false);
    // Only the latest read's reply is applied, so a poll sent before a re-run
    // was accepted can't report the result the re-run replaced, and a reply
    // for the PR just navigated away from can't paint onto this one.
    const latestRead = useRef(0);
    const prRef = useRef(pr);

    const read = async (quiet: boolean) => {
        const seq = ++latestRead.current;
        if (!quiet) setLoading(true);
        try {
            const res = await rpcCall<{ output: Record<string, PluginResult> }>(
                'RPCHandler.GetPluginOutput',
                [{ Owner: owner, Repo: repo, Number: number }]
            );
            if (seq !== latestRead.current) return;
            const output = res.output || {};
            setStored(output);
            setAwaiting(prev => {
                const next = rerunsStillAwaited(prev, output);
                return next.size === prev.size ? prev : next;
            });
        } catch (e) {
            if (seq !== latestRead.current) return;
            console.error('Failed to load plugin output:', e);
        } finally {
            if (!quiet) setLoading(false);
        }
    };

    useEffect(() => {
        prRef.current = pr;
        setStored({});
        setAwaiting(new Set());
        setExecuting(new Set());
        read(false);
    }, [pr]);

    const outputs = useMemo(() => withAwaitedReruns(stored, awaiting), [stored, awaiting]);
    const polling = pendingPluginsKey(outputs);

    // Restarts whenever the set of pending plugins changes, so the time limit
    // runs from the last plugin to start or finish.
    useEffect(() => {
        if (!polling) return;
        const startedAt = Date.now();
        let cancelled = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        const poll = async () => {
            if (Date.now() - startedAt > MAX_PLUGIN_POLL_MS) {
                // Pending for longer than the server lets a plugin run: the
                // run was lost (a server restart) or never started. Stop
                // standing in for a result that isn't coming.
                setAwaiting(new Set());
                return;
            }
            await read(true);
            // Stops once nothing is pending: `polling` empties and this effect
            // is cleaned up.
            if (!cancelled) timer = setTimeout(poll, PLUGIN_POLL_MS);
        };
        timer = setTimeout(poll, PLUGIN_POLL_MS);
        return () => {
            cancelled = true;
            if (timer) clearTimeout(timer);
        };
    }, [pr, polling]);

    const execute = async (name: string) => {
        const startedFor = prRef.current;
        setExecuting(prev => new Set(prev).add(name));
        try {
            await rpcCall('RPCHandler.RerunPlugins', [
                { Owner: owner, Repo: repo, Number: number, Plugins: [name] },
            ]);
            if (prRef.current !== startedFor) return;
            // The server has cleared the old result; the plugin reads as
            // pending, and is polled for, until the new one lands.
            setAwaiting(prev => new Set(prev).add(name));
            await read(true);
        } catch (e) {
            console.error('Failed to execute plugin:', e);
        } finally {
            if (prRef.current === startedFor) {
                setExecuting(prev => {
                    const next = new Set(prev);
                    next.delete(name);
                    return next;
                });
            }
        }
    };

    return { outputs, loading, executing, load: () => read(false), execute };
}
