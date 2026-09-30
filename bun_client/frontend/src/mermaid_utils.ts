/**
 * Mermaid diagrams
 *
 * The change-diagram AI feature serves raw Mermaid source, which this client
 * draws with the mermaid library. The library is large, so it is loaded the
 * first time a diagram is drawn rather than with the app.
 *
 * The source is model output steered by a PR's diff, so it is untrusted.
 * mermaid runs at securityLevel "strict", which encodes HTML in labels and
 * disables click handlers, and a directive in the source can't lift it:
 * mermaid only lets initialize() set securityLevel. The server also strips
 * clicks and directives before it stores a diagram.
 */

type Mermaid = (typeof import('mermaid'))['default'];

let loading: Promise<Mermaid> | null = null;
let renders = 0;

function loadMermaid(): Promise<Mermaid> {
    loading ??= import('mermaid').then(
        m => m.default,
        e => {
            // Let the next diagram try again, e.g. after a flaky chunk load.
            loading = null;
            throw e;
        }
    );
    return loading;
}

/**
 * Draws Mermaid source as SVG markup, themed for a dark or light background.
 * Rejects with mermaid's parse error when the source isn't a valid diagram.
 */
export async function renderMermaid(source: string, dark: boolean): Promise<string> {
    const mermaid = await loadMermaid();
    mermaid.initialize({
        startOnLoad: false,
        securityLevel: 'strict',
        theme: dark ? 'dark' : 'default',
        // Throw on a syntax error rather than drawing mermaid's error diagram.
        suppressErrorRendering: true,
    });
    const { svg } = await mermaid.render(`crs-mermaid-${++renders}`, source);
    return svg;
}

export interface Size {
    width: number;
    height: number;
}

/** The size a rendered diagram's SVG is drawn at, read from its root viewBox. */
export function svgSize(svg: string): Size | null {
    const root = svg.match(/<svg\b[^>]*>/);
    const viewBox = root?.[0].match(/viewBox\s*=\s*"([^"]*)"/);
    if (!viewBox) return null;
    const parts = viewBox[1]
        .trim()
        .split(/[\s,]+/)
        .map(Number);
    if (parts.length !== 4 || parts.some(n => !Number.isFinite(n))) return null;
    const [, , width, height] = parts;
    return width > 0 && height > 0 ? { width, height } : null;
}

/** The most "fit" enlarges a small diagram: beyond it, a few boxes fill the screen. */
export const MAX_FIT_SCALE = 1.5;

/** The scale that fits a diagram of size `diagram` in `box`, at most MAX_FIT_SCALE. */
export function fitScale(diagram: Size, box: Size): number {
    const scale = Math.min(box.width / diagram.width, box.height / diagram.height);
    return Math.max(Math.min(scale, MAX_FIT_SCALE), 0.05);
}

/** The steps the zoom buttons move through. */
export const ZOOM_STEPS = [0.1, 0.25, 0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4];

/** The next zoom step in `direction` from `scale`, which may lie between steps. */
export function stepZoom(scale: number, direction: 1 | -1): number {
    if (direction > 0) {
        return ZOOM_STEPS.find(s => s > scale + 0.001) ?? ZOOM_STEPS[ZOOM_STEPS.length - 1];
    }
    return [...ZOOM_STEPS].reverse().find(s => s < scale - 0.001) ?? ZOOM_STEPS[0];
}

/**
 * Whether a CSS color (#rgb, #rrggbb or rgb()/rgba()) is dark; null when it
 * can't be read.
 */
export function isDarkColor(color: string): boolean | null {
    const c = color.trim().toLowerCase();
    let rgb: number[] | null = null;
    const hex = c.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/);
    if (hex) {
        const h = hex[1].length === 3 ? [...hex[1]].map(d => d + d).join('') : hex[1];
        rgb = [0, 2, 4].map(i => parseInt(h.slice(i, i + 2), 16));
    } else {
        const fn = c.match(/^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/);
        if (fn) rgb = fn.slice(1, 4).map(Number);
    }
    if (!rgb || rgb.some(n => !Number.isFinite(n))) return null;
    // Relative luminance, ITU-R BT.709 weights, in 0–255.
    const [r, g, b] = rgb;
    return 0.2126 * r + 0.7152 * g + 0.0722 * b < 128;
}

/** Whether the app's current theme is dark, read from its background color. */
export function appThemeIsDark(): boolean {
    if (typeof document === 'undefined') return true;
    const bg = getComputedStyle(document.documentElement).getPropertyValue('--bg-primary');
    return isDarkColor(bg) ?? true;
}
