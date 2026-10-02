/**
 * Mermaid diagrams as images
 *
 * A drawn diagram's Copy image and Download image make a PNG of it, to paste
 * into a chat, an issue or a doc. The SVG mermaid drew is loaded as an image
 * and painted onto a canvas: at up to twice its natural size, so it stays
 * sharp on a high-density screen; on the background the app draws it on,
 * which mermaid themed its colors for; with a margin and, for a change
 * diagram, the legend below it.
 *
 * Drawing, encoding and copying an image all cost by the pixel, so a large
 * diagram is drawn at a smaller scale, on a canvas kept in memory rather than
 * on the GPU, and each step has a time limit, so a browser that stalls on one
 * fails with a reason rather than never finishing.
 */

import { renderMermaid, svgSize, type Size } from './mermaid_utils';

/** A legend entry: a swatch drawn like the nodes it explains, and its label. */
export interface LegendItem {
    label: string;
    fill: string;
    stroke: string;
    dashed: boolean;
}

/** A diagram as mermaid drew it: `svg`, `size` large, from `source` in the dark or light theme. */
export interface DrawnDiagram {
    source: string;
    svg: string;
    size: Size;
    dark: boolean;
}

/** The most image pixels per diagram unit (a CSS pixel at 1:1). */
export const IMAGE_SCALE = 2;

/**
 * The pixels an image may have before it's drawn at less than IMAGE_SCALE,
 * down to the diagram's natural size: a ~30-node change diagram at twice its
 * size is about 10 million, which takes seconds to draw, encode and copy.
 */
export const IMAGE_PIXELS = 4_000_000;

/**
 * The most pixels an image has on a side and in all, within the largest
 * canvas each browser will draw. Only a diagram larger than these at its
 * natural size is drawn smaller than that.
 */
export const MAX_IMAGE_SIDE = 16_384;
export const MAX_IMAGE_AREA = 16_000_000;

/**
 * How long drawing an image may take, and copying one from the click (a
 * drawing still under way included), before giving up with a reason.
 */
export const DRAW_TIMEOUT_MS = 20_000;
export const COPY_TIMEOUT_MS = 30_000;

/** The margin around the diagram, in diagram units. */
export const IMAGE_MARGIN = 16;

// The legend as MermaidDiagram draws it beside the zoom buttons: 12px
// swatches with a 1.5px border and 3px corners, 4px before their 12px
// labels, 10px between entries. In the image, its row sits below the diagram.
const LEGEND_GAP = 12;
const LEGEND_HEIGHT = 16;
const SWATCH = 12;
const SWATCH_BORDER = 1.5;
const SWATCH_RADIUS = 3;
const SWATCH_LABEL_GAP = 4;
const LEGEND_ENTRY_GAP = 10;
const LEGEND_FONT_SIZE = 12;

/** The pixels per diagram unit an image `size` large (in diagram units) is drawn at. */
export function imageScale(size: Size): number {
    const area = size.width * size.height;
    const scale = Math.min(IMAGE_SCALE, Math.max(1, Math.sqrt(IMAGE_PIXELS / area)));
    return Math.min(
        scale,
        MAX_IMAGE_SIDE / Math.max(size.width, size.height),
        Math.sqrt(MAX_IMAGE_AREA / area)
    );
}

/** How wide the legend's row is, given how wide `measure` finds each label. */
export function legendWidth(
    legend: readonly LegendItem[],
    measure: (label: string) => number
): number {
    return legend.reduce(
        (width, item, i) =>
            width +
            (i > 0 ? LEGEND_ENTRY_GAP : 0) +
            SWATCH +
            SWATCH_LABEL_GAP +
            measure(item.label),
        0
    );
}

export interface Point {
    x: number;
    y: number;
}

/** Where the parts of a diagram's image go, in diagram units. */
export interface ImageLayout {
    width: number;
    height: number;
    /** The diagram's top left corner. */
    diagram: Point;
    /** The legend row's top left corner; null without a legend. */
    legend: Point | null;
}

/**
 * Lays out the image of a diagram `diagram` large: the diagram within a
 * margin, and the legend row below it when there is one, `legend` wide. A
 * diagram narrower than the legend is centered above it.
 */
export function imageLayout(diagram: Size, legend: number | null): ImageLayout {
    const inner = Math.max(diagram.width, legend ?? 0);
    const legendTop = IMAGE_MARGIN + diagram.height + LEGEND_GAP;
    return {
        width: inner + 2 * IMAGE_MARGIN,
        height:
            legend === null
                ? diagram.height + 2 * IMAGE_MARGIN
                : legendTop + LEGEND_HEIGHT + IMAGE_MARGIN,
        diagram: { x: IMAGE_MARGIN + (inner - diagram.width) / 2, y: IMAGE_MARGIN },
        legend: legend === null ? null : { x: IMAGE_MARGIN, y: legendTop },
    };
}

/**
 * A PNG of a drawn diagram, with `legend` below it when there is one. It
 * fails if drawing it takes over `timeoutMs`.
 *
 * Safari won't let a canvas be read back once an SVG with HTML in it (here,
 * mermaid's labels, in <foreignObject>s) has been drawn on it. When the
 * browser refuses like that, the diagram is drawn again with its labels as
 * SVG text, and the image is made of that. Its labels wrap mid-word, so it's
 * only the fallback.
 */
export function diagramPng(
    diagram: DrawnDiagram,
    legend: readonly LegendItem[] = [],
    timeoutMs = DRAW_TIMEOUT_MS
): Promise<Blob> {
    return withTimeout(
        drawPng(diagram, legend),
        timeoutMs,
        `drawing it took over ${timeoutMs / 1000} seconds`
    );
}

async function drawPng(diagram: DrawnDiagram, legend: readonly LegendItem[]): Promise<Blob> {
    try {
        return await rasterize(diagram.svg, diagram.size, legend);
    } catch (e) {
        if (!(e instanceof DOMException && e.name === 'SecurityError')) throw e;
        const svg = await renderMermaid(diagram.source, diagram.dark, { htmlLabels: false });
        const size = svgSize(svg);
        if (!size) throw e;
        return rasterize(svg, size, legend);
    }
}

async function rasterize(svg: string, size: Size, legend: readonly LegendItem[]): Promise<Blob> {
    const canvas = document.createElement('canvas');
    // Kept in memory rather than on the GPU, since it's only drawn to be read
    // back: on the GPU, Chrome spends seconds painting the SVG and reading the
    // pixels back, even for a small diagram.
    const ctx = canvas.getContext('2d', { willReadFrequently: true });
    if (!ctx) throw new Error('the browser has no canvas to draw it on');
    const theme = getComputedStyle(document.documentElement);
    const font = `${LEGEND_FONT_SIZE}px ${getComputedStyle(document.body).fontFamily || 'sans-serif'}`;
    ctx.font = font;
    const layout = imageLayout(
        size,
        legend.length > 0 ? legendWidth(legend, label => ctx.measureText(label).width) : null
    );
    const scale = imageScale(layout);
    // Sized to the pixels it covers, the SVG is drawn sharp rather than scaled up.
    const image = await loadImage(
        svgDataUrl(svg, { width: size.width * scale, height: size.height * scale })
    );

    // Sizing the canvas resets its context, the font included.
    canvas.width = Math.round(layout.width * scale);
    canvas.height = Math.round(layout.height * scale);
    ctx.fillStyle = theme.getPropertyValue('--bg-primary').trim() || 'white';
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    ctx.drawImage(
        image,
        layout.diagram.x * scale,
        layout.diagram.y * scale,
        size.width * scale,
        size.height * scale
    );
    if (layout.legend) {
        ctx.scale(scale, scale);
        ctx.font = font;
        drawLegend(
            ctx,
            legend,
            layout.legend,
            theme.getPropertyValue('--text-secondary').trim() || 'gray'
        );
    }
    return canvasPng(canvas);
}

/**
 * `svg` as a data URL that loads as an image `size` large.
 *
 * It's re-serialized as XML on the way: mermaid serializes the HTML in its
 * labels as HTML (`&nbsp;`, say), and an SVG image, parsed as XML, fails to
 * load over that. And it's a data URL because Chrome treats an SVG with HTML
 * in it loaded from a blob: URL as cross-origin, and won't let the canvas
 * it's drawn on be read back.
 */
function svgDataUrl(svg: string, size: Size): string {
    const root = new DOMParser().parseFromString(svg, 'text/html').querySelector('svg');
    if (!root) throw new Error('the drawing has no <svg> element');
    // Mermaid fits it to its container (width="100%" and a max-width); an
    // image needs a size of its own.
    root.setAttribute('width', String(size.width));
    root.setAttribute('height', String(size.height));
    root.style.removeProperty('max-width');
    const xml = new XMLSerializer().serializeToString(root);
    return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(xml)}`;
}

function loadImage(src: string): Promise<HTMLImageElement> {
    return new Promise((resolve, reject) => {
        const image = new Image();
        image.onload = () => resolve(image);
        image.onerror = () =>
            reject(new Error("the browser couldn't load the diagram as an image"));
        image.src = src;
    });
}

/** Draws `legend` in a row from `at`, its labels in `color`. */
function drawLegend(
    ctx: CanvasRenderingContext2D,
    legend: readonly LegendItem[],
    at: Point,
    color: string
) {
    const middle = at.y + LEGEND_HEIGHT / 2;
    // A swatch's border is drawn inside its square, as CSS draws one.
    const inset = SWATCH_BORDER / 2;
    let x = at.x;
    ctx.textBaseline = 'middle';
    ctx.lineWidth = SWATCH_BORDER;
    for (const item of legend) {
        ctx.beginPath();
        ctx.roundRect(
            x + inset,
            middle - SWATCH / 2 + inset,
            SWATCH - SWATCH_BORDER,
            SWATCH - SWATCH_BORDER,
            SWATCH_RADIUS - inset
        );
        ctx.fillStyle = item.fill;
        ctx.fill();
        ctx.setLineDash(item.dashed ? [3, 2] : []);
        ctx.strokeStyle = item.stroke;
        ctx.stroke();
        x += SWATCH + SWATCH_LABEL_GAP;
        ctx.fillStyle = color;
        ctx.fillText(item.label, x, middle);
        x += ctx.measureText(item.label).width + LEGEND_ENTRY_GAP;
    }
}

function canvasPng(canvas: HTMLCanvasElement): Promise<Blob> {
    // toBlob throws a SecurityError, which rejects, when the browser won't
    // let the canvas be read back.
    return new Promise((resolve, reject) =>
        canvas.toBlob(
            blob =>
                blob ? resolve(blob) : reject(new Error("the browser couldn't encode the image")),
            'image/png'
        )
    );
}

/**
 * Puts the PNG `png` gives on the clipboard: the image itself once it's
 * made, or until then the promise of it, which the clipboard waits for.
 * Either way the write starts within the click that asked for it, as Safari
 * requires (it refuses one that starts after an await). It fails if the copy
 * hasn't finished after `timeoutMs`.
 */
export async function copyPng(
    png: () => Blob | Promise<Blob>,
    timeoutMs = COPY_TIMEOUT_MS
): Promise<void> {
    if (typeof ClipboardItem === 'undefined' || !navigator.clipboard?.write) {
        throw new Error("this browser can't copy an image here; use Download image instead");
    }
    const image = png();
    try {
        await withTimeout(
            navigator.clipboard.write([new ClipboardItem({ 'image/png': image })]),
            timeoutMs,
            `the browser hadn't copied it after ${timeoutMs / 1000} seconds`
        );
    } catch (e) {
        // A failed drawing explains more than the write it failed.
        await image;
        throw e;
    }
}

/** `promise`, or a failure with `reason` once it has taken over `ms`. */
export function withTimeout<T>(promise: Promise<T>, ms: number, reason: string): Promise<T> {
    return new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error(reason)), ms);
        promise.finally(() => clearTimeout(timer)).then(resolve, reject);
    });
}

/** Saves `blob` as a file named `name`, as a link to it would. */
export function downloadBlob(blob: Blob, name: string): void {
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = name;
    document.body.appendChild(link);
    link.click();
    link.remove();
    // Long after the download has started from it.
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
}
