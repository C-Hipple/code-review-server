import { afterEach, describe, expect, test } from 'bun:test';
import {
    copyPng,
    IMAGE_MARGIN,
    IMAGE_SCALE,
    imageLayout,
    imageScale,
    legendWidth,
    MAX_IMAGE_AREA,
    MAX_IMAGE_SIDE,
    type LegendItem,
} from './mermaid_image';

const legend: LegendItem[] = [
    { label: 'Added', fill: '#dcfce7', stroke: '#16a34a', dashed: false },
    { label: 'Removed', fill: '#fee2e2', stroke: '#dc2626', dashed: true },
];

describe('imageScale', () => {
    test('draws a diagram at twice its size', () => {
        expect(imageScale({ width: 812, height: 430 })).toBe(IMAGE_SCALE);
    });

    test('draws one too long for a canvas at the scale that fits it', () => {
        expect(imageScale({ width: 20_000, height: 300 })).toBe(MAX_IMAGE_SIDE / 20_000);
    });

    test('draws one too large in all at the scale that fits it', () => {
        const scale = imageScale({ width: 6000, height: 5000 });
        expect(scale).toBeLessThan(IMAGE_SCALE);
        expect(6000 * scale * 5000 * scale).toBeCloseTo(MAX_IMAGE_AREA);
    });
});

describe('legendWidth', () => {
    test("adds each entry's swatch and label, with a gap between entries", () => {
        // A 12px swatch 4px before its label; 10px between entries.
        expect(legendWidth(legend, label => label.length * 10)).toBe(
            12 + 4 + 50 + 10 + (12 + 4 + 70)
        );
    });

    test('is nothing without entries', () => {
        expect(legendWidth([], () => 100)).toBe(0);
    });
});

describe('imageLayout', () => {
    test('puts a margin around a diagram without a legend', () => {
        expect(imageLayout({ width: 300, height: 200 }, null)).toEqual({
            width: 300 + 2 * IMAGE_MARGIN,
            height: 200 + 2 * IMAGE_MARGIN,
            diagram: { x: IMAGE_MARGIN, y: IMAGE_MARGIN },
            legend: null,
        });
    });

    test('puts the legend below the diagram', () => {
        // The legend's 16px row is 12px below the diagram.
        expect(imageLayout({ width: 300, height: 200 }, 150)).toEqual({
            width: 300 + 2 * IMAGE_MARGIN,
            height: IMAGE_MARGIN + 200 + 12 + 16 + IMAGE_MARGIN,
            diagram: { x: IMAGE_MARGIN, y: IMAGE_MARGIN },
            legend: { x: IMAGE_MARGIN, y: IMAGE_MARGIN + 200 + 12 },
        });
    });

    test('centers a diagram narrower than the legend above it', () => {
        const layout = imageLayout({ width: 100, height: 50 }, 200);
        expect(layout.width).toBe(200 + 2 * IMAGE_MARGIN);
        expect(layout.diagram).toEqual({ x: IMAGE_MARGIN + 50, y: IMAGE_MARGIN });
        expect(layout.legend).toEqual({ x: IMAGE_MARGIN, y: IMAGE_MARGIN + 50 + 12 });
    });
});

describe('copyPng', () => {
    // The browser's ClipboardItem and clipboard, which bun has neither of.
    class FakeClipboardItem {
        constructor(readonly items: Record<string, Promise<Blob>>) {}
    }
    const writes: FakeClipboardItem[][] = [];

    function fakeClipboard(write: (items: FakeClipboardItem[]) => Promise<void>) {
        Object.assign(globalThis, { ClipboardItem: FakeClipboardItem });
        Object.defineProperty(navigator, 'clipboard', {
            configurable: true,
            value: {
                write: (items: FakeClipboardItem[]) => {
                    writes.push(items);
                    return write(items);
                },
            },
        });
    }

    afterEach(() => {
        Reflect.deleteProperty(globalThis, 'ClipboardItem');
        Reflect.deleteProperty(navigator, 'clipboard');
        writes.length = 0;
    });

    test("refuses without drawing when the browser can't copy an image", async () => {
        let drew = false;
        const png = () => {
            drew = true;
            return Promise.resolve(new Blob());
        };
        await expect(copyPng(png)).rejects.toThrow("this browser can't copy an image here");
        expect(drew).toBe(false);
    });

    test("hands the clipboard the image while it's still being drawn", async () => {
        // Like a browser, the write waits for the image.
        fakeClipboard(async ([item]) => void (await item.items['image/png']));
        let finish = (_: Blob) => {};
        const drawing = new Promise<Blob>(resolve => (finish = resolve));

        const copied = copyPng(() => drawing);
        // Asked for within the click, before the image is drawn.
        expect(writes).toHaveLength(1);
        expect(writes[0][0].items['image/png']).toBe(drawing);

        finish(new Blob(['png'], { type: 'image/png' }));
        await copied;
    });

    test('reports why the drawing failed rather than the write it failed', async () => {
        fakeClipboard(async ([item]) => {
            await item.items['image/png'].catch(() => {
                throw new DOMException('Failed to write to the clipboard.', 'NotAllowedError');
            });
        });
        const png = () => Promise.reject(new Error("the browser couldn't load the diagram"));
        await expect(copyPng(png)).rejects.toThrow("the browser couldn't load the diagram");
    });

    test("reports the clipboard's refusal of a drawn image", async () => {
        fakeClipboard(async () => {
            throw new DOMException('Document is not focused.', 'NotAllowedError');
        });
        await expect(copyPng(() => Promise.resolve(new Blob()))).rejects.toThrow(
            'Document is not focused.'
        );
    });
});
