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

// mermaid's configuration is global, and a render reads it as it draws, so
// each render's initialize() waits until the render before it is done.
let rendering: Promise<unknown> = Promise.resolve();

export interface RenderOptions {
    /**
     * Draw the labels as HTML, in a <foreignObject> (true, mermaid's default,
     * which wraps them between words), or as SVG text.
     */
    htmlLabels?: boolean;
}

/**
 * Draws Mermaid source as SVG markup, themed for a dark or light background.
 * Rejects with mermaid's parse error when the source isn't a valid diagram.
 */
export function renderMermaid(
    source: string,
    dark: boolean,
    { htmlLabels = true }: RenderOptions = {}
): Promise<string> {
    const svg = rendering.then(async () => {
        const mermaid = await loadMermaid();
        mermaid.initialize({
            startOnLoad: false,
            securityLevel: 'strict',
            theme: dark ? 'dark' : 'default',
            htmlLabels,
            // Throw on a syntax error rather than drawing mermaid's error diagram.
            suppressErrorRendering: true,
        });
        const { svg } = await mermaid.render(`crs-mermaid-${++renders}`, source);
        return svg;
    });
    rendering = svg.catch(() => {});
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
 * A CSS color (#rgb, #rrggbb, rgb()/rgba() or hsl()/hsla()) as 0–255 rgb, or
 * null when it can't be read. `white` and `black` are understood too, since
 * some Prism styles name them.
 */
export function parseCssColor(color: string): number[] | null {
    const c = color.trim().toLowerCase();
    if (c === 'white') return [255, 255, 255];
    if (c === 'black') return [0, 0, 0];
    const hex = c.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/);
    if (hex) {
        const h = hex[1].length === 3 ? [...hex[1]].map(d => d + d).join('') : hex[1];
        return [0, 2, 4].map(i => parseInt(h.slice(i, i + 2), 16));
    }
    const fn = c.match(/^rgba?\(\s*([\d.]+)[\s,]+([\d.]+)[\s,]+([\d.]+)/);
    if (fn) return fn.slice(1, 4).map(Number);
    const hsl = c.match(/^hsla?\(\s*([\d.]+)(?:deg)?[\s,]+([\d.]+)%[\s,]+([\d.]+)%/);
    if (hsl) return hslToRgb(Number(hsl[1]), Number(hsl[2]) / 100, Number(hsl[3]) / 100);
    return null;
}

/** Whether a CSS color is dark; null when it can't be read. */
export function isDarkColor(color: string): boolean | null {
    const rgb = parseCssColor(color);
    if (!rgb || rgb.some(n => !Number.isFinite(n))) return null;
    // Relative luminance, ITU-R BT.709 weights, in 0–255.
    const [r, g, b] = rgb;
    return 0.2126 * r + 0.7152 * g + 0.0722 * b < 128;
}

/** hsl (hue in degrees, saturation and lightness in 0–1) as 0–255 rgb. */
function hslToRgb(hue: number, s: number, l: number): number[] {
    const h = ((hue % 360) + 360) % 360;
    const c = (1 - Math.abs(2 * l - 1)) * s;
    const x = c * (1 - Math.abs(((h / 60) % 2) - 1));
    const m = l - c / 2;
    const [r, g, b] =
        h < 60
            ? [c, x, 0]
            : h < 120
              ? [x, c, 0]
              : h < 180
                ? [0, c, x]
                : h < 240
                  ? [0, x, c]
                  : h < 300
                    ? [x, 0, c]
                    : [c, 0, x];
    return [r, g, b].map(v => Math.round((v + m) * 255));
}

/** Whether the app's current theme is dark, read from its background color. */
export function appThemeIsDark(): boolean {
    if (typeof document === 'undefined') return true;
    const bg = getComputedStyle(document.documentElement).getPropertyValue('--bg-primary');
    return isDarkColor(bg) ?? true;
}
