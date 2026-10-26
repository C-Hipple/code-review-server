// The contract between the specs and the fake server (fake_crs.ts).
//
// The fake is started by the real native host, which Chrome starts, so a spec
// can't reach its stdin. Instead each test gets a state directory, named to
// the fake by CRS_E2E_STATE_DIR (set in the host's env file):
//
//   scenario.json  written by the spec (FakeCrs.update), re-read by the fake
//                  on every request, so a spec can switch scenarios mid-test
//   calls.jsonl    appended by the fake: one line per request it received,
//                  before it answers

export const STATE_DIR_VAR = 'CRS_E2E_STATE_DIR';
export const SCENARIO_FILE = 'scenario.json';
export const CALLS_FILE = 'calls.jsonl';

export interface Scenario {
    /** GetAllReviews serves the fixture list, or no PRs at all. Default `fixtures`. */
    reviews?: 'fixtures' | 'empty';
    /**
     * AI feature ids and plugin names whose runs in flight land, by
     * themselves and at once, announced as the server announces its runs. A
     * run reads `pending` until its name is listed here.
     */
    finishRuns?: string[];
    /**
     * AI feature ids and plugin names the server starts a run of for PR42 on
     * its own (a push, a workflow cycle), once each: nothing in the browser
     * asks for them.
     */
    serverRuns?: string[];
    /**
     * Exit with `code`, after writing `stderr` to stderr, on receiving
     * `method` (without answering it) — a server dying under the host.
     */
    exitOn?: { method: string; code: number; stderr: string };
}

/** One request the fake received: the method and its (unwrapped) args object. */
export interface RecordedCall {
    method: string;
    params: Record<string, unknown>;
}
