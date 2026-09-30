/**
 * Style Utility Functions
 * Helpers for common styling patterns
 */

import { colors } from './tokens';

export type StatusVariant = 'success' | 'danger' | 'warning' | 'info' | 'neutral';

/**
 * Get background color for status variant
 */
export function getStatusColor(variant: StatusVariant): string {
    switch (variant) {
        case 'success':
            return colors.success;
        case 'danger':
            return colors.danger;
        case 'warning':
            return colors.warning;
        case 'info':
            return colors.accent;
        case 'neutral':
            return colors.textSecondary;
        default:
            return colors.textSecondary;
    }
}

/**
 * The dim palette for a status variant: a readable foreground over a tinted
 * background, plus the matching border. Use it where a solid `getStatusColor`
 * fill would shout — inline badges and callouts sitting inside content.
 */
export interface StatusTone {
    fg: string;
    bg: string;
    border: string;
}

export function getStatusTone(variant: StatusVariant): StatusTone {
    switch (variant) {
        case 'success':
            return {
                fg: colors.textSuccess,
                bg: colors.bgSuccessDim,
                border: colors.borderSuccessDim,
            };
        case 'danger':
            return {
                fg: colors.textDanger,
                bg: colors.bgDangerDim,
                border: colors.borderDangerDim,
            };
        case 'warning':
            return {
                fg: colors.textWarning,
                bg: colors.bgWarningDim,
                border: colors.borderWarningDim,
            };
        case 'info':
            return { fg: colors.accent, bg: colors.bgInfoDim, border: colors.borderInfoDim };
        default:
            return { fg: colors.textSecondary, bg: colors.bgTertiary, border: colors.border };
    }
}

/**
 * Map common status strings to variants
 */
export function mapStatusToVariant(status: string): StatusVariant {
    const statusLower = status.toLowerCase();

    if (statusLower === 'success' || statusLower === 'done' || statusLower === 'passed') {
        return 'success';
    }
    if (
        statusLower === 'error' ||
        statusLower === 'failed' ||
        statusLower === 'failure' ||
        statusLower === 'cancelled'
    ) {
        return 'danger';
    }
    if (
        statusLower === 'warning' ||
        statusLower === 'pending' ||
        statusLower === 'progress' ||
        statusLower === 'todo'
    ) {
        return 'warning';
    }
    if (statusLower === 'info') {
        return 'info';
    }

    return 'neutral';
}

/** The levels a review-ease rating falls into. */
export type ReviewEaseLevel = 'easy' | 'medium' | 'hard';

/** Every review-ease level, easiest first. */
export const REVIEW_EASE_LEVELS: readonly ReviewEaseLevel[] = ['easy', 'medium', 'hard'];

/**
 * How a review-ease rating should be displayed
 */
export interface ReviewEaseDisplay {
    /** The level the rating falls into, which the review list filters on. */
    level: ReviewEaseLevel;
    label: string;
    variant: StatusVariant;
}

const REVIEW_EASE_DISPLAY: Record<ReviewEaseLevel, ReviewEaseDisplay> = {
    easy: { level: 'easy', label: 'EASY', variant: 'success' },
    medium: { level: 'medium', label: 'MEDIUM', variant: 'warning' },
    hard: { level: 'hard', label: 'HARD', variant: 'danger' },
};

/**
 * Map the backend's review-ease rating to its level, display label and badge
 * variant.
 *
 * The backend currently rates review ease as "easy" | "medium" | "hard", but
 * the rating scheme may change (e.g. to a numeric 0-100 score). Keep all
 * interpretation of the raw rating here so a scheme change only needs to be
 * handled in this one place. Returns null when there is no usable rating
 * (feature disabled, or not yet computed), in which case nothing should be
 * rendered.
 */
export function mapReviewEase(ease: string | undefined): ReviewEaseDisplay | null {
    // Today a rating is its level's own name.
    const rating = (ease || '').toLowerCase();
    const level = REVIEW_EASE_LEVELS.find(l => l === rating);
    return level ? REVIEW_EASE_DISPLAY[level] : null;
}

/** How a level is displayed, for a control that offers every level. */
export function reviewEaseDisplay(level: ReviewEaseLevel): ReviewEaseDisplay {
    return REVIEW_EASE_DISPLAY[level];
}

/**
 * Combine class names, filtering out falsy values
 */
export function cn(...classNames: (string | undefined | null | false)[]): string {
    return classNames.filter(Boolean).join(' ');
}
