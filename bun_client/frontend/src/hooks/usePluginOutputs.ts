import { useEffect, useMemo, useRef, useState } from 'react';
import { rpcCall } from '../api';
import {
    MAX_RERUN_WAIT_MS,
    rerunsStillAwaited,
    withAwaitedReruns,
    type PluginResult,
} from '../plugin_utils';
import {
    isChangeFor,
    parsePluginOutputChanged,
    PLUGIN_OUTPUT_CHANGED_EVENT,
    serverEvents,
    type ServerEvents,
} from '../server_events';
import { useServerChanges } from './useServerChanges';

export interface PluginOutputs {
    /** Each plugin's latest output; a re-run whose result is yet to land reads as pending. */
    outputs: Record<string, PluginResult>;
    /** Whether a load someone asked for is in flight. Re-reads the server asked for don't count. */
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
 * first automatic run has a result to hand back right away. Instead the
 * server announces every change to a PR's plugin output — a plugin marked
 * pending, a result landing, a re-run clearing one — and the output is read
 * again when an announcement names this PR. Nothing is polled.
 */
export function usePluginOutputs(
    owner: string,
    repo: string,
    number: number,
    events: ServerEvents = serverEvents
): PluginOutputs {
    const pr = `${owner}/${repo}#${number}`;
    // The output as the server last served it, and the re-runs it doesn't
    // show the result of yet.
    const [stored, setStored] = useState<Record<string, PluginResult>>({});
    const [awaiting, setAwaiting] = useState<Set<string>>(new Set());
    const [executing, setExecuting] = useState<Set<string>>(new Set());
    const [loading, setLoading] = useState(false);
    // Only the latest read's reply is applied, so a read sent before a re-run
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

    // Read again whenever the server says this PR's output changed.
    useServerChanges(
        PLUGIN_OUTPUT_CHANGED_EVENT,
        data => {
            const change = parsePluginOutputChanged(data);
            return !!change && isChangeFor(change, { owner, repo, number });
        },
        () => read(true),
        { key: pr, events }
    );

    const outputs = useMemo(() => withAwaitedReruns(stored, awaiting), [stored, awaiting]);

    // A re-run that never lands (a server restart lost it, or it never
    // started) is announced by nothing, so it stops reading as pending once
    // the server would have stopped it. Restarts as re-runs start or land.
    useEffect(() => {
        if (awaiting.size === 0) return;
        const timer = setTimeout(() => setAwaiting(new Set()), MAX_RERUN_WAIT_MS);
        return () => clearTimeout(timer);
    }, [awaiting]);

    const execute = async (name: string) => {
        const startedFor = prRef.current;
        setExecuting(prev => new Set(prev).add(name));
        try {
            await rpcCall('RPCHandler.RerunPlugins', [
                { Owner: owner, Repo: repo, Number: number, Plugins: [name] },
            ]);
            if (prRef.current !== startedFor) return;
            // The server has cleared the old result; the plugin reads as
            // pending until the server announces the new one.
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
