// Mermaid is most of the panel's weight, so it is imported on first use: the
// build splits it into its own chunk, and the panel opens without it.
//
// The change diagram's source is model output steered by a PR's diff, so it
// is untrusted: mermaid runs at securityLevel "strict" (labels HTML-encoded,
// no click handlers), which a directive in the source can't lift.

type Mermaid = (typeof import('mermaid'))['default'];

let loading: Promise<Mermaid> | null = null;
let renders = 0;

/** The mermaid library, loaded once. */
export function loadMermaid(): Promise<Mermaid> {
    loading ??= import('mermaid').then(
        m => m.default,
        e => {
            loading = null; // let a later call retry
            throw e;
        }
    );
    return loading;
}

// mermaid's configuration is global and a render reads it as it draws, so
// each render's initialize() waits for the render before it.
let queue: Promise<unknown> = Promise.resolve();

/**
 * Draws Mermaid source as SVG markup themed for a dark or light background.
 * Rejects with mermaid's parse error when the source isn't a valid diagram.
 */
export function renderMermaid(source: string, dark: boolean): Promise<string> {
    const svg = queue.then(async () => {
        const mermaid = await loadMermaid();
        mermaid.initialize({
            startOnLoad: false,
            securityLevel: 'strict',
            theme: dark ? 'dark' : 'default',
            // Throw on a syntax error instead of drawing mermaid's error diagram.
            suppressErrorRendering: true,
        });
        const { svg } = await mermaid.render(`crs-mermaid-${++renders}`, source);
        return svg;
    });
    queue = svg.catch(() => {});
    return svg;
}
