import { describe, expect, test } from 'bun:test';
import {
    fitScale,
    isDarkColor,
    MAX_FIT_SCALE,
    stepZoom,
    svgSize,
    ZOOM_STEPS,
} from './mermaid_utils';

describe('svgSize', () => {
    test("reads the root svg's viewBox", () => {
        const svg =
            '<svg id="crs-mermaid-1" width="100%" style="max-width: 812px;" viewBox="-8 -8 812.5 430" role="graphics-document">' +
            '<svg viewBox="0 0 10 10"></svg></svg>';
        expect(svgSize(svg)).toEqual({ width: 812.5, height: 430 });
    });

    test('accepts commas between the numbers', () => {
        expect(svgSize('<svg viewBox="0,0,20,10">')).toEqual({ width: 20, height: 10 });
    });

    test('is null without a usable viewBox', () => {
        expect(svgSize('<svg width="10"></svg>')).toBeNull();
        expect(svgSize('<svg viewBox="0 0 0 10"></svg>')).toBeNull();
        expect(svgSize('<svg viewBox="0 0 wide 10"></svg>')).toBeNull();
        expect(svgSize('not svg')).toBeNull();
    });
});

describe('fitScale', () => {
    test('shrinks a large diagram to the tighter dimension', () => {
        expect(fitScale({ width: 2000, height: 500 }, { width: 1000, height: 1000 })).toBe(0.5);
        expect(fitScale({ width: 500, height: 2000 }, { width: 1000, height: 1000 })).toBe(0.5);
    });

    test('enlarges a small diagram only so far', () => {
        expect(fitScale({ width: 100, height: 100 }, { width: 1000, height: 1000 })).toBe(
            MAX_FIT_SCALE
        );
        expect(fitScale({ width: 800, height: 800 }, { width: 1000, height: 1000 })).toBe(1.25);
    });
});

describe('stepZoom', () => {
    test('moves to the neighbouring step', () => {
        expect(stepZoom(1, 1)).toBe(1.25);
        expect(stepZoom(1, -1)).toBe(0.75);
    });

    test('moves from a fitted scale between steps to the next one', () => {
        expect(stepZoom(0.9, 1)).toBe(1);
        expect(stepZoom(0.9, -1)).toBe(0.75);
    });

    test('stops at the ends', () => {
        expect(stepZoom(ZOOM_STEPS[ZOOM_STEPS.length - 1], 1)).toBe(
            ZOOM_STEPS[ZOOM_STEPS.length - 1]
        );
        expect(stepZoom(ZOOM_STEPS[0], -1)).toBe(ZOOM_STEPS[0]);
        expect(stepZoom(0.01, -1)).toBe(ZOOM_STEPS[0]);
    });
});

describe('isDarkColor', () => {
    test('reads hex and rgb colors', () => {
        expect(isDarkColor('#0f1115')).toBe(true);
        expect(isDarkColor(' #FFFFFF ')).toBe(false);
        expect(isDarkColor('#fff')).toBe(false);
        expect(isDarkColor('#222')).toBe(true);
        expect(isDarkColor('rgb(246, 248, 250)')).toBe(false);
        expect(isDarkColor('rgba(22, 27, 34, 0.9)')).toBe(true);
    });

    test('is null for a color it cannot read', () => {
        expect(isDarkColor('')).toBeNull();
        expect(isDarkColor('var(--bg)')).toBeNull();
        expect(isDarkColor('#12345')).toBeNull();
    });
});
