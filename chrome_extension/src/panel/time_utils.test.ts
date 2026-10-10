import { describe, expect, test } from 'bun:test';
import { absoluteTime, relativeTime } from './time_utils';

const NOW = Date.parse('2026-10-10T12:00:00Z');
const before = (seconds: number) => new Date(NOW - seconds * 1000).toISOString();

describe('relativeTime', () => {
    test.each([
        [10, 'just now'],
        [-30, 'just now'], // clock skew
        [50, '1 min ago'],
        [5 * 60, '5 min ago'],
        [59 * 60 + 59, '59 min ago'],
        [60 * 60, '1 hour ago'],
        [3 * 3600 + 120, '3 hours ago'],
        [26 * 3600, 'yesterday'],
        [5 * 86400, '5 days ago'],
        [45 * 86400, '1 month ago'],
        [200 * 86400, '6 months ago'],
        [800 * 86400, '2 years ago'],
    ])('%i s ago → %s', (seconds, expected) => {
        expect(relativeTime(before(seconds), NOW)).toBe(expected);
    });

    test('empty or unreadable', () => {
        expect(relativeTime('', NOW)).toBe('');
        expect(relativeTime('yesterday', NOW)).toBe('');
        expect(absoluteTime('nope')).toBe('');
        expect(absoluteTime(before(10))).not.toBe('');
    });
});
