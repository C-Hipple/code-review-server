import { useEffect, useRef, useState } from 'react';
import { getAIOutput, runAIFeature } from '../api';
import {
    isPending,
    MAX_POLL_MS,
    pollDelayMs,
    shouldRunOnOpen,
    type AIFeatureOutput,
} from '../ai_utils';

interface UseAIReportOptions {
    owner: string;
    repo: string;
    number: number;
    feature: string;
    /** What the caller already has for the feature, shown until the first load lands. */
    initialOutput?: AIFeatureOutput;
    /** Every output loaded, so a count shown elsewhere stays current. */
    onOutput?: (output: AIFeatureOutput) => void;
}

export interface AIReportState {
    output: AIFeatureOutput | null;
    /** A load or run request failed. */
    error: string | null;
    /** Why the server declined to run the feature. */
    notice: string | null;
    /** Polling stopped with the run still pending. */
    gaveUp: boolean;
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
 * when nothing changed), then polls while the run is pending. The previous
 * result stays in `output` until the new one lands.
 */
export function useAIReport({
    owner,
    repo,
    number,
    feature,
    initialOutput,
    onOutput,
}: UseAIReportOptions): AIReportState {
    const [output, setOutput] = useState<AIFeatureOutput | null>(initialOutput ?? null);
    const [error, setError] = useState<string | null>(null);
    const [notice, setNotice] = useState<string | null>(null);
    const [gaveUp, setGaveUp] = useState(false);
    // Bumped by rerun(); each value is one forced run.
    const [rerunToken, setRerunToken] = useState(0);

    // Read through a ref so a parent re-render (a new callback identity)
    // doesn't restart the run.
    const onOutputRef = useRef(onOutput);
    useEffect(() => {
        onOutputRef.current = onOutput;
    }, [onOutput]);

    useEffect(() => {
        let cancelled = false;
        let timer: ReturnType<typeof setTimeout> | undefined;
        const pr = { Owner: owner, Repo: repo, Number: number, Feature: feature };
        const startedAt = Date.now();

        const publish = (o: AIFeatureOutput) => {
            setOutput(o);
            onOutputRef.current?.(o);
        };

        const poll = async (attempt: number) => {
            try {
                const o = (await getAIOutput(pr))[feature];
                if (cancelled || !o) return;
                publish(o);
                if (!isPending(o)) return;
                if (Date.now() - startedAt > MAX_POLL_MS) {
                    setGaveUp(true);
                    return;
                }
                timer = setTimeout(() => poll(attempt + 1), pollDelayMs(attempt));
            } catch (e) {
                if (!cancelled) setError(errorText(e));
            }
        };

        const start = async () => {
            setError(null);
            setNotice(null);
            setGaveUp(false);
            try {
                let current: AIFeatureOutput | undefined;
                if (rerunToken === 0) {
                    current = (await getAIOutput(pr))[feature];
                    if (cancelled) return;
                    if (current) publish(current);
                }
                if (rerunToken > 0 || shouldRunOnOpen(current)) {
                    const reply = await runAIFeature({ ...pr, Force: rerunToken > 0 });
                    if (cancelled) return;
                    if (reply.output) publish(reply.output);
                    if (!reply.okay) {
                        setNotice(reply.message);
                        return;
                    }
                    current = reply.output ?? current;
                }
                if (current && isPending(current)) {
                    timer = setTimeout(() => poll(0), pollDelayMs(0));
                }
            } catch (e) {
                if (!cancelled) setError(errorText(e));
            }
        };

        start();
        return () => {
            cancelled = true;
            if (timer) clearTimeout(timer);
        };
    }, [owner, repo, number, feature, rerunToken]);

    return {
        output,
        error,
        notice,
        gaveUp,
        pending: isPending(output),
        rerun: () => setRerunToken(t => t + 1),
    };
}
