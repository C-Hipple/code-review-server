// Stand-in for `codereviewserver --server`, run under Bun. The real native
// host (crs_native_host) spawns it — CRS_SERVER_PATH points at a shim for this
// script — and talks to it exactly as it talks to the Go server:
// newline-delimited JSON-RPC 1.0 on stdin/stdout, as Go's net/rpc/jsonrpc
// speaks it (`{"method","params":[args],"id"}` in, `{"id","result","error"}`
// out, the error as a string). Anything written to stderr lands in the host's
// log, like the server's own logging.
//
// The specs steer it through files in CRS_E2E_STATE_DIR (see scenario.ts):
// scenario.json is re-read on every request, and every request is appended
// to calls.jsonl before it is answered. Runs (RunAIFeature, RerunPlugins) are
// kept in memory, which lives as long as the host's connection — one test.

import { appendFileSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import {
    aiOutput,
    aiRunResult,
    FEATURES,
    PLUGINS,
    PR42,
    pluginRunResult,
    pr42AIOutputs,
    pr42PluginOutputs,
    prKey,
    prPayload,
    REVIEW_ITEMS,
    type PR,
} from '../fixtures/data';
import type { AIFeatureOutput, PluginOutput } from '../../src/types';
import { CALLS_FILE, SCENARIO_FILE, STATE_DIR_VAR, type Scenario } from './scenario';

interface Request {
    method: string;
    params?: Record<string, unknown>[];
    id: number;
}

type Args = Record<string, unknown>;

const stateDir = process.env[STATE_DIR_VAR];
if (!stateDir) {
    process.stderr.write(`fake crs: ${STATE_DIR_VAR} is not set\n`);
    process.exit(2);
}

function scenario(): Scenario {
    try {
        return JSON.parse(readFileSync(join(stateDir!, SCENARIO_FILE), 'utf-8')) as Scenario;
    } catch {
        return {};
    }
}

// --- State: outputs per PR, and runs in flight --------------------------------

const aiOutputs = new Map<string, Record<string, AIFeatureOutput>>();
const pluginOutputs = new Map<string, Record<string, PluginOutput>>();
/** Runs started per PR and feature / plugin, to tell one run's result from the next. */
const runCounts = new Map<string, number>();

function prArgs(args: Args): PR {
    const owner = String(args.Owner ?? '');
    const repo = String(args.Repo ?? '');
    const number = Number(args.Number);
    if (!owner || !repo || !Number.isInteger(number) || number <= 0) {
        throw new Error(`invalid pull request ${owner}/${repo}#${String(args.Number)}`);
    }
    return { owner, repo, number };
}

function aiFor(pr: PR): Record<string, AIFeatureOutput> {
    let outputs = aiOutputs.get(prKey(pr));
    if (!outputs) {
        outputs =
            prKey(pr) === prKey(PR42)
                ? pr42AIOutputs()
                : Object.fromEntries(FEATURES.map(f => [f.id, aiOutput(f)]));
        aiOutputs.set(prKey(pr), outputs);
    }
    return outputs;
}

function pluginsFor(pr: PR): Record<string, PluginOutput> {
    let outputs = pluginOutputs.get(prKey(pr));
    if (!outputs) {
        outputs = prKey(pr) === prKey(PR42) ? pr42PluginOutputs() : {};
        pluginOutputs.set(prKey(pr), outputs);
    }
    return outputs;
}

function nextRun(pr: PR, name: string): number {
    const key = `${prKey(pr)}:${name}`;
    const n = (runCounts.get(key) ?? 0) + 1;
    runCounts.set(key, n);
    return n;
}

/** Lands the runs the scenario says are finished. */
function settleRuns(pr: PR): void {
    const finish = new Set(scenario().finishRuns ?? []);
    const ai = aiFor(pr);
    for (const [id, output] of Object.entries(ai)) {
        if (output.status === 'pending' && finish.has(id)) {
            const run = runCounts.get(`${prKey(pr)}:${id}`) ?? 1;
            ai[id] = { ...output, ...aiRunResult(id, run), stale: false };
        }
    }
    const plugins = pluginsFor(pr);
    for (const [name, output] of Object.entries(plugins)) {
        if (output.status === 'pending' && finish.has(name)) {
            plugins[name] = pluginRunResult(name, runCounts.get(`${prKey(pr)}:${name}`) ?? 1);
        }
    }
}

// --- Methods ---------------------------------------------------------------------

const handlers: Record<string, (args: Args) => unknown> = {
    'RPCHandler.GetAllReviews': () => ({
        content: '',
        items: scenario().reviews === 'empty' ? [] : REVIEW_ITEMS,
    }),

    'RPCHandler.GetPR': args => prPayload(prArgs(args)),

    'RPCHandler.SyncPR': args => ({ ...prPayload(prArgs(args)), updated: false }),

    'RPCHandler.ListPlugins': () => ({ plugins: PLUGINS }),

    'RPCHandler.GetPluginOutput': args => {
        const pr = prArgs(args);
        settleRuns(pr);
        return { output: pluginsFor(pr) };
    },

    // Like the server: clears the named plugins' results (all of them without
    // `Plugins`) and runs them in the background.
    'RPCHandler.RerunPlugins': args => {
        const pr = prArgs(args);
        const names = Array.isArray(args.Plugins)
            ? args.Plugins.map(String)
            : PLUGINS.map(p => p.Name);
        const outputs = pluginsFor(pr);
        for (const name of names) {
            nextRun(pr, name);
            outputs[name] = {
                result: '',
                status: 'pending',
                body: { body_type: 'markdown', body_content: '' },
                annotations: null,
            };
        }
        return { okay: true, message: `Rerunning ${names.length} plugin(s)`, output: {} };
    },

    'RPCHandler.ListAIFeatures': () => ({ features: FEATURES }),

    // Every enabled feature's output, or just `Feature`'s.
    'RPCHandler.GetAIOutput': args => {
        const pr = prArgs(args);
        settleRuns(pr);
        const all = aiFor(pr);
        const wanted = args.Feature
            ? FEATURES.filter(f => f.id === args.Feature)
            : FEATURES.filter(f => f.enabled);
        return { output: Object.fromEntries(wanted.map(f => [f.id, all[f.id]])) };
    },

    // The server's outcomes: unknown-feature, disabled, already-running,
    // up-to-date (a current result and no Force), else started.
    'RPCHandler.RunAIFeature': args => {
        const pr = prArgs(args);
        const feature = FEATURES.find(f => f.id === args.Feature);
        if (!feature) {
            return {
                okay: false,
                outcome: 'unknown-feature',
                message: `There is no AI feature named "${String(args.Feature)}"`,
                output: null,
            };
        }
        const outputs = aiFor(pr);
        const current = outputs[feature.id];
        if (!feature.enabled) {
            return { okay: false, outcome: 'disabled', message: 'not enabled', output: current };
        }
        if (current.status === 'pending') {
            return { okay: true, outcome: 'already-running', message: '', output: current };
        }
        if (!args.Force && current.status === 'success' && !current.stale) {
            return { okay: true, outcome: 'up-to-date', message: '', output: current };
        }
        nextRun(pr, feature.id);
        // A run in flight still carries the previous result, as the server's does.
        outputs[feature.id] = { ...current, status: 'pending' };
        return {
            okay: true,
            outcome: 'started',
            message: `Running ${feature.name} for PR ${pr.number}`,
            output: outputs[feature.id],
        };
    },
};

// --- JSON-RPC over stdio ---------------------------------------------------------

function respond(id: number, result: unknown, error: string | null): void {
    process.stdout.write(`${JSON.stringify({ id, result: error ? null : result, error })}\n`);
}

/** Set once an exitOn scenario fired: nothing after that request is read. */
let exiting = false;

function handle(req: Request): void {
    if (exiting) return;
    const args = req.params?.[0] ?? {};
    appendFileSync(
        join(stateDir!, CALLS_FILE),
        `${JSON.stringify({ method: req.method, params: args })}\n`
    );

    const exit = scenario().exitOn;
    if (exit && exit.method === req.method) {
        exiting = true;
        process.stderr.write(`${exit.stderr}\n`, () => process.exit(exit.code));
        return;
    }

    const handler = handlers[req.method];
    if (!handler) {
        respond(req.id, null, `rpc: can't find method ${req.method}`);
        return;
    }
    try {
        respond(req.id, handler(args), null);
    } catch (e) {
        respond(req.id, null, e instanceof Error ? e.message : String(e));
    }
}

process.stderr.write(`fake crs: pid ${process.pid}, args ${process.argv.slice(2).join(' ')}\n`);

let buffer = '';
process.stdin.setEncoding('utf-8');
process.stdin.on('data', (chunk: string) => {
    buffer += chunk;
    let nl = buffer.indexOf('\n');
    while (nl !== -1) {
        const line = buffer.slice(0, nl).trim();
        buffer = buffer.slice(nl + 1);
        if (line) handle(JSON.parse(line) as Request);
        nl = buffer.indexOf('\n');
    }
});
// The host closes stdin when Chrome closes the connection; the server exits.
process.stdin.on('end', () => process.exit(0));
// The host is gone: nobody is left to answer.
process.stdout.on('error', () => process.exit(0));
