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

/**
 * How a review-ease rating should be displayed
 */
export interface ReviewEaseDisplay {
    label: string;
    variant: StatusVariant;
}

/**
 * Map the backend's review-ease rating to a display label and badge variant.
 *
 * The backend currently rates review ease as "easy" | "medium" | "hard", but
 * the rating scheme may change (e.g. to a numeric 0-100 score). Keep all
 * interpretation of the raw rating here so a scheme change only needs to be
 * handled in this one place. Returns null when there is no usable rating
 * (feature disabled, or not yet computed), in which case nothing should be
 * rendered.
 */
export function mapReviewEase(ease: string | undefined): ReviewEaseDisplay | null {
    switch ((ease || '').toLowerCase()) {
        case 'easy':
            return { label: 'EASY', variant: 'success' };
        case 'medium':
            return { label: 'MEDIUM', variant: 'warning' };
        case 'hard':
            return { label: 'HARD', variant: 'danger' };
        default:
            return null;
    }
}

/**
 * Combine class names, filtering out falsy values
 */
export function cn(...classNames: (string | undefined | null | false)[]): string {
    return classNames.filter(Boolean).join(' ');
}
