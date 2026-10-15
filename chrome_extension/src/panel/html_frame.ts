// The document an HTML plugin / AI body is shown in: the body inside a
// sandboxed iframe's srcdoc, with a stylesheet that gives it the panel's
// theme so it doesn't render as a white box in dark mode.

/** Panel theme variables the frame copies, read from the panel's :root. */
export const FRAME_VARS = [
    '--bg',
    '--bg-subtle',
    '--fg',
    '--muted',
    '--border',
    '--accent',
    '--font',
    '--mono',
] as const;

export type FrameVars = Partial<Record<(typeof FRAME_VARS)[number], string>>;

/** The current values of FRAME_VARS on the panel's root element. */
export function readFrameVars(): FrameVars {
    if (typeof document === 'undefined' || typeof getComputedStyle !== 'function') return {};
    const styles = getComputedStyle(document.documentElement);
    const vars: FrameVars = {};
    for (const name of FRAME_VARS) {
        const value = styles.getPropertyValue(name).trim();
        if (value) vars[name] = value;
    }
    return vars;
}

/**
 * The srcdoc for `html`. Links open in a new tab (the frame may open popups
 * but runs no script); `vars` carry the panel's colors and fonts in.
 */
export function buildFrameDocument(html: string, vars: FrameVars): string {
    const declarations = Object.entries(vars)
        .map(([name, value]) => `${name}:${value.replace(/[<>{};]/g, '')};`)
        .join('');
    return `<!doctype html><html><head><meta charset="utf-8"><meta name="referrer" content="no-referrer"><base target="_blank"><style>
:root{color-scheme:light dark;${declarations}}
html,body{margin:0;padding:0;background:transparent;color:var(--fg);font-family:var(--font,system-ui,sans-serif);font-size:14px;line-height:1.5;overflow-wrap:anywhere;}
body>:first-child{margin-top:0}body>:last-child{margin-bottom:0}
h1,h2,h3,h4{margin:1em 0 .5em;line-height:1.25;font-weight:600}
h1{font-size:1.4em}h2{font-size:1.2em}h3{font-size:1.05em}
p{margin:.5em 0}ul,ol{margin:.5em 0;padding-left:1.5em}li{margin:.2em 0}
a{color:var(--accent)}
code{font-family:var(--mono,monospace);font-size:12px;background:var(--bg-subtle);padding:.15em .35em;border-radius:4px}
pre{font-family:var(--mono,monospace);font-size:12px;background:var(--bg-subtle);border:1px solid var(--border);padding:10px 12px;border-radius:6px;overflow:auto}
pre code{background:none;padding:0}
blockquote{margin:.5em 0;padding-left:1em;border-left:3px solid var(--border);color:var(--muted)}
hr{border:none;border-top:1px solid var(--border);margin:1em 0}
table{border-collapse:collapse;margin:.5em 0}th,td{border:1px solid var(--border);padding:4px 10px;text-align:left}th{background:var(--bg-subtle)}
img{max-width:100%}
</style></head><body>${html}</body></html>`;
}
