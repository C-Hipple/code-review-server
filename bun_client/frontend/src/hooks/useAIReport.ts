import { useEffect, useRef, useState } from 'react';
import { getAIOutput, runAIFeature } from '../api';
import { isPending, shouldRunOnOpen, type AIFeatureOutput } from '../ai_utils';
import {
    AI_OUTPUT_CHANGED_EVENT,
    isChangeFor,
    parseAIOutputChanged,
    serverEvents,
    type ServerEvents,
} from '../server_events';
import { useServerChanges } from './useServerChanges';

interface UseAIReportOptions {
    owner: string;
    repo: string;
    number: number;
    feature: string;
    /** What the caller already has for the feature, shown until the first load lands. */
    initialOutput?: AIFeatureOutput;
    /** Every output loaded, so a count shown elsewhere stays current. */
    onOutput?: (output: AIFeatureOutput) => void;
    events?: ServerEvents;
}

export interface AIReportState {
    output: AIFeatureOutput | null;
    /** A load or run request failed. */
    error: string | null;
    /** Why the server declined to run the feature. */
    notice: string | null;
    pending: boolean;
    /** Runs the feature again, even if nothing changed. */
    rerun: () => void;
}

const errorText = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * One AI feature's result for a PR, kept current while the caller is mounted.
 *
 * Mounting asks the server for a run when there is no result yet or the
 * stored one no longer describes the PR (the server answers from its cache
 * when nothing changed). The server announces each run of the feature for
 * the PR as it starts and as it lands, and the output is read again on each,
 * so a pending run shows its result without polling. The previous result
 * stays in `output` until the new one lands.
 */
export function useAIReport({
    owner,
    repo,
    number,
    feature,
    initialOutput,
    onOutput,
    events = serverEvents,
}: UseAIReportOptions): AIReportState {
    const [output, setOutput] = useState<AIFeatureOutput | null>(initialOutput ?? null);
    const [error, setError] = useState<string | null>(null);
    const [notice, setNotice] = useState<string | null>(null);
    // Bumped by rerun(); each value is one forced run.
    const [rerunToken, setRerunToken] = useState(0);
    const key = `${owner}/${repo}#${number}:${feature}`;

    // Read through a ref so a parent re-render (a new callback identity)
    // doesn't restart the run.
    const onOutputRef = useRef(onOutput);
    useEffect(() => {
        onOutputRef.current = onOutput;
    }, [onOutput]);

    // Replies can arrive out of order — a run's reply, written before the
    // run landed, after the read its landing prompted — so each request takes
    // a ticket and only a reply newer than the last one shown is shown. The
    // key guards against a reply for the feature or PR just switched away
    // from.
    const lastTicket = useRef(0);
    const lastShown = useRef(0);
    const keyRef = useRef(key);
    const ticket = () => ({ seq: ++lastTicket.current, key });
    const show = (t: { seq: number; key: string }, o: AIFeatureOutput | undefined) => {
        if (!o || t.key !== keyRef.current || t.seq <= lastShown.current) return;
        lastShown.current = t.seq;
        setOutput(o);
        onOutputRef.current?.(o);
    };

    const args = { Owner: owner, Repo: repo, Number: number, Feature: feature };

    useEffect(() => {
        keyRef.current = key;
        let cancelled = false;

        const start = async () => {
            setError(null);
            setNotice(null);
            try {
                let current: AIFeatureOutput | undefined;
                if (rerunToken === 0) {
                    const t = ticket();
                    current = (await getAIOutput(args))[feature];
                    if (cancelled) return;
                    show(t, current);
                }
                if (rerunToken > 0 || shouldRunOnOpen(current)) {
                    const t = ticket();
                    const reply = await runAIFeature({ ...args, Force: rerunToken > 0 });
                    if (cancelled) return;
                    show(t, reply.output ?? undefined);
                    if (!reply.okay) setNotice(reply.message);
                }
            } catch (e) {
                if (!cancelled) setError(errorText(e));
            }
        };

        start();
        return () => {
            cancelled = true;
        };
    }, [owner, repo, number, feature, rerunToken]);

    // Read again whenever the server says this feature's output for the PR
    // changed: a run starting or landing, whoever asked for it.
    useServerChanges(
        AI_OUTPUT_CHANGED_EVENT,
        data => {
            const change = parseAIOutputChanged(data);
            return (
                !!change &&
                change.feature === feature &&
                isChangeFor(change, { owner, repo, number })
            );
        },
        async () => {
            const t = ticket();
            try {
                show(t, (await getAIOutput(args))[feature]);
            } catch (e) {
                if (t.key === keyRef.current) setError(errorText(e));
            }
        },
        { key, events }
    );

    return {
        output,
        error,
        notice,
        pending: isPending(output),
        rerun: () => setRerunToken(t => t + 1),
    };
}
