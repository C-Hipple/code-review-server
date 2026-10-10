// Mermaid is most of the panel's weight, so it is imported on first use: the
// build splits it into its own chunk, and the panel opens without it.

type Mermaid = (typeof import('mermaid'))['default'];

let loading: Promise<Mermaid> | null = null;

/**
 * The mermaid library, initialised for the current color scheme. Call it
 * before each render: the theme follows `prefers-color-scheme`, which can
 * change while the panel is open.
 */
export async function loadMermaid(): Promise<Mermaid> {
    loading ??= import('mermaid').then(
        m => m.default,
        e => {
            loading = null; // let a later call retry
            throw e;
        }
    );
    const mermaid = await loading;
    mermaid.initialize({
        startOnLoad: false,
        // Diagrams come from model output: no HTML labels, no click handlers.
        securityLevel: 'strict',
        theme: prefersDark() ? 'dark' : 'default',
    });
    return mermaid;
}

export function prefersDark(): boolean {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
}
