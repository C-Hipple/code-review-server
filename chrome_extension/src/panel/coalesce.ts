/**
 * Wraps `run` so its calls never overlap: a call made while a run is in
 * flight asks for one more run once it ends, however many such calls there
 * are. The server announces a burst of changes at once (each plugin of a run
 * starting, then landing), and each wants the same read.
 *
 * The returned promise settles when the run it joined (or the one it asked
 * for) has ended. `run` reports its own errors.
 */
export function coalesce(run: () => Promise<unknown>): () => Promise<void> {
    let running: Promise<void> | null = null;
    let again = false;
    return () => {
        if (running) {
            again = true;
            return running;
        }
        running = (async () => {
            try {
                do {
                    again = false;
                    try {
                        await run();
                    } catch {
                        // `run` reports its own failures; keep the loop going.
                    }
                } while (again);
            } finally {
                running = null;
            }
        })();
        return running;
    };
}
