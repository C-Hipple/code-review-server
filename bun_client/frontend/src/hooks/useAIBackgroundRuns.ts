import { useEffect, useRef, useState } from 'react';
import { getAIOutput, rpcErrorMessage, runAIFeature } from '../api';
import {
    hasResult,
    isPending,
    pollDelayMs,
    type AIFeatureInfo,
    type AIFeatureOutput,
} from '../ai_utils';
import type { StatusVariant } from '../design';

interface UseAIBackgroundRunsOptions {
    owner: string;
    repo: string;
    number: number;
    features: AIFeatureInfo[];
    /** The caller's latest output for each feature. */
    outputs: Record<string, AIFeatureOutput>;
    /** Merges the outputs this hook loads into the caller's. */
    onOutputs: (outputs: Record<string, AIFeatureOutput>) => void;
    /** The feature whose report is open; its modal polls for itself. */
    openFeature: string | null;
    onToast: (message: string, variant?: StatusVariant) => void;
}

export interface AIBackgroundRuns {
    /** Whether the feature is working out its first result for the PR. */
    generating: (feature: string) => boolean;
    /** Asks for the feature's first result without opening its report. */
    generate: (feature: AIFeatureInfo) => void;
}

/**
 * The AI runs the review toolbar follows in the background.
 *
 * A feature with no result for the PR has no report worth opening, so its
 * button asks for a run instead (`generate`) and spins until it lands, then
 * says so with a toast. Any feature whose run is in flight — asked for here,
 * or started automatically by the server — is polled, so the toolbar's
 * spinners and counts stay current without a report open.
 */
export function useAIBackgroundRuns({
    owner,
    repo,
    number,
    features,
    outputs,
    onOutputs,
    openFeature,
    onToast,
}: UseAIBackgroundRunsOptions): AIBackgroundRuns {
    const pr = `${owner}/${repo}#${number}`;
    // Features whose RunAIFeature request is in flight, before its reply
    // reads pending.
    const [starting, setStarting] = useState<Set<string>>(new Set());
    // Features generated from the toolbar, to announce once each lands.
    const requested = useRef<Set<string>>(new Set());
    // The PR shown now: a run reply for the one just navigated away from is dropped.
    const prRef = useRef(pr);

    // Read through refs so a parent re-render (new callback identities)
    // doesn't restart the polling.
    const onOutputsRef = useRef(onOutputs);
    const onToastRef = useRef(onToast);
    useEffect(() => {
        onOutputsRef.current = onOutputs;
        onToastRef.current = onToast;
    }, [onOutputs, onToast]);

    useEffect(() => {
        prRef.current = pr;
        requested.current = new Set();
        setStarting(new Set());
    }, [pr]);

    // The features to poll for: every run in flight but the open report's.
    const polling = features
        .filter(f => f.id !== openFeature && isPending(outputs[f.id]))
        .map(f => f.id)
        .join(',');

    useEffect(() => {
        if (!polling) return;
        let cancelled = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        const poll = async (attempt: number) => {
            try {
                const loaded = await getAIOutput({ Owner: owner, Repo: repo, Number: number });
                if (cancelled) return;
                onOutputsRef.current(loaded);
            } catch (e) {
                if (cancelled) return;
                console.error('Failed to poll AI outputs:', e);
            }
            // Stops once nothing is pending: `polling` empties and this
            // effect is cleaned up.
            timer = setTimeout(() => poll(attempt + 1), pollDelayMs(attempt));
        };
        timer = setTimeout(() => poll(0), pollDelayMs(0));
        return () => {
            cancelled = true;
            if (timer) clearTimeout(timer);
        };
    }, [owner, repo, number, polling]);

    useEffect(() => {
        for (const id of requested.current) {
            const output = outputs[id];
            if (isPending(output) || !hasResult(output)) continue;
            requested.current.delete(id);
            const name = features.find(f => f.id === id)?.name ?? id;
            if (output.status === 'success') {
                onToastRef.current(`✦ ${name} is ready`, 'success');
            } else {
                onToastRef.current(`✦ ${name} could not finish — open it for details`, 'warning');
            }
        }
    }, [outputs, features]);

    const generate = async (feature: AIFeatureInfo) => {
        const id = feature.id;
        const startedFor = prRef.current;
        requested.current.add(id);
        setStarting(prev => new Set(prev).add(id));
        try {
            const reply = await runAIFeature({
                Owner: owner,
                Repo: repo,
                Number: number,
                Feature: id,
            });
            if (prRef.current !== startedFor) return;
            if (!reply.okay) {
                requested.current.delete(id);
                onToastRef.current(reply.message || `${feature.name} did not start`, 'warning');
            }
            if (reply.output) onOutputsRef.current({ [id]: reply.output });
        } catch (e) {
            if (prRef.current !== startedFor) return;
            requested.current.delete(id);
            onToastRef.current(`Could not start ${feature.name}: ${rpcErrorMessage(e)}`, 'danger');
        } finally {
            if (prRef.current === startedFor) {
                setStarting(prev => {
                    const next = new Set(prev);
                    next.delete(id);
                    return next;
                });
            }
        }
    };

    return {
        generating: id => starting.has(id) || (isPending(outputs[id]) && !hasResult(outputs[id])),
        generate,
    };
}
