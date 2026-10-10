// The panel's reading of ListAIFeatures / GetAIOutput replies: which features
// get a card, what a card's status chip and button say, and the reports the
// panel draws itself (change-diagram's Mermaid, file-ordering's list).

import {
    CHANGE_DIAGRAM,
    FILE_ORDERING,
    REVIEW_EASE,
    type AIFeature,
    type AIFeatureOutput,
    type PRMetadata,
    type ReviewEase,
} from '../types';
import type { Tone } from './list_utils';

/** Features with a report card: enabled, and not applied by the server itself. */
export function reportFeatures(features: readonly AIFeature[]): AIFeature[] {
    return features.filter(f => f.enabled && !f.applied);
}

/** Enabled features whose result the server applies (file order, review ease). */
export function appliedFeatures(features: readonly AIFeature[]): AIFeature[] {
    return features.filter(f => f.enabled && f.applied);
}

export function disabledFeatures(features: readonly AIFeature[]): AIFeature[] {
    return features.filter(f => !f.enabled);
}

export function isAIPending(output: AIFeatureOutput | null | undefined): boolean {
    return output?.status === 'pending';
}

/** A run has finished for the PR at some point, so there is something to show. */
export function hasResult(output: AIFeatureOutput | null | undefined): boolean {
    return !!output && output.status !== 'not-run' && output.updated_at !== '';
}

export function aiStatusLabel(status: string): string {
    switch (status) {
        case 'not-run':
            return 'Not run';
        case 'pending':
            return 'Running';
        case 'success':
            return 'Success';
        case 'error':
            return 'Failed';
        case 'insufficient-input':
            return 'Insufficient input';
        default:
            return status || 'Unknown';
    }
}

export function aiStatusTone(status: string): Tone {
    switch (status) {
        case 'pending':
            return 'accent';
        case 'success':
            return 'success';
        case 'error':
            return 'danger';
        case 'insufficient-input':
            return 'attention';
        default:
            return 'neutral';
    }
}

export interface RunAction {
    label: 'Run' | 'Re-run' | 'Running…';
    /** RunAIFeature's Force: rerun even when the stored result covers the inputs. */
    force: boolean;
    disabled: boolean;
}

/**
 * The card's button. **Run** (no force) when there is nothing current to
 * show — never run, stale, or failed (the server retries a failed run on
 * request) — so an up-to-date result answers without a model call; **Re-run**
 * (forced) when a current result is on screen; nothing to press while a run
 * is in flight.
 */
export function aiRunAction(output: AIFeatureOutput | null | undefined): RunAction {
    if (isAIPending(output)) return { label: 'Running…', force: false, disabled: true };
    if (!output || output.status === 'not-run' || output.stale || output.status === 'error') {
        return { label: 'Run', force: false, disabled: false };
    }
    return { label: 'Re-run', force: true, disabled: false };
}

/** What to tell the user about a RunAIFeature outcome; empty when the chip says it. */
export function runOutcomeNote(outcome: string): string {
    switch (outcome) {
        case 'up-to-date':
            return 'Already up to date: nothing the feature reads has changed since its last run.';
        case 'already-running':
            return 'A run is already in progress.';
        default:
            return '';
    }
}

/** change-diagram's Mermaid source, or null when the output carries none. */
export function changeDiagramSource(output: AIFeatureOutput | null | undefined): string | null {
    if (!output || output.feature !== CHANGE_DIAGRAM) return null;
    const report = output.report as { mermaid?: unknown } | null;
    const source = report?.mermaid;
    return typeof source === 'string' && source.trim() !== '' ? source : null;
}

export type ChangeClass = 'added' | 'changed' | 'removed';

const CHANGE_CLASSES: ChangeClass[] = ['added', 'changed', 'removed'];

/**
 * The change classes a flowchart puts nodes in — `A:::added`, or
 * `class A,B changed` — in legend order. The server defines all three in
 * every change diagram, so only use counts.
 */
export function changeClassesUsed(source: string): ChangeClass[] {
    const used = new Set<string>();
    for (const m of source.matchAll(/:::([\w-]+)/g)) used.add(m[1]);
    for (const m of source.matchAll(/^\s*class\s+\S+\s+([\w-]+)\s*;?\s*$/gm)) used.add(m[1]);
    return CHANGE_CLASSES.filter(c => used.has(c));
}

/** file-ordering's suggested order, or null when the output carries none. */
export function fileOrderingFiles(output: AIFeatureOutput | null | undefined): string[] | null {
    if (!output || output.feature !== FILE_ORDERING) return null;
    const report = output.report as { files?: unknown } | null;
    const files = report?.files;
    if (!Array.isArray(files)) return null;
    const paths = files.filter((f): f is string => typeof f === 'string' && f !== '');
    return paths.length > 0 ? paths : null;
}

function isEase(value: unknown): value is ReviewEase {
    return value === 'easy' || value === 'medium' || value === 'hard';
}

/**
 * The PR's review-ease rating: GetPR's metadata, else the review-ease
 * output's report; empty when neither has one.
 */
export function reviewEaseOf(
    metadata: PRMetadata | null | undefined,
    outputs: Record<string, AIFeatureOutput> | null | undefined
): ReviewEase | '' {
    if (isEase(metadata?.review_ease)) return metadata.review_ease;
    const report = outputs?.[REVIEW_EASE]?.report as { rating?: unknown } | null | undefined;
    return isEase(report?.rating) ? report.rating : '';
}

/**
 * A one-line taste of a markdown body for a collapsed card: its first line
 * of prose, without markdown syntax, cut to `max` characters.
 */
export function bodyPreview(markdown: string, max = 160): string {
    let inFence = false;
    for (const raw of markdown.split('\n')) {
        const line = raw.trim();
        if (line.startsWith('```') || line.startsWith('~~~')) {
            inFence = !inFence;
            continue;
        }
        if (inFence || !line || /^(#{1,6}\s|[-*_]{3,}$|\||<)/.test(line)) continue;
        const text = line
            .replace(/^([-*+]|\d+\.)\s+/, '')
            .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
            .replace(/[*_`~]/g, '')
            .trim();
        if (!text) continue;
        return text.length > max ? `${text.slice(0, max - 1).trimEnd()}…` : text;
    }
    return '';
}

export interface Size {
    width: number;
    height: number;
}

/** The size a rendered diagram's SVG is drawn at, from its root viewBox; null when unreadable. */
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
