import { expect, test, describe } from 'bun:test';
import { teamChips, teamRelationship, teamSearchTerms, teamVariant } from './team_utils';
import type { RequiredTeam } from './team_utils';

function team(overrides: Partial<RequiredTeam> = {}): RequiredTeam {
    return {
        name: 'Platform',
        slug: 'platform',
        org: 'acme',
        status: 'pending',
        mine: false,
        personal: false,
        ...overrides,
    };
}

describe('teamRelationship', () => {
    test('separates a direct request from a team one', () => {
        expect(teamRelationship(team({ personal: true, mine: true }))).toBe('personal');
        expect(teamRelationship(team({ mine: true }))).toBe('mine');
        expect(teamRelationship(team())).toBe('theirs');
    });
});

describe('teamVariant', () => {
    test('a decision colors the same whoever made it', () => {
        for (const mine of [true, false]) {
            expect(teamVariant(team({ status: 'approved', mine }))).toBe('success');
            expect(teamVariant(team({ status: 'changes_requested', mine }))).toBe('danger');
            expect(teamVariant(team({ status: 'reviewed', mine }))).toBe('info');
        }
    });

    test('only a pending review distinguishes your teams from the rest', () => {
        expect(teamVariant(team({ status: 'pending', mine: true }))).toBe('warning');
        expect(teamVariant(team({ status: 'pending', personal: true }))).toBe('warning');
        expect(teamVariant(team({ status: 'pending' }))).toBe('neutral');
    });

    test('an unrecognized status stays neutral rather than mis-coloring', () => {
        expect(teamVariant(team({ status: 'something_new', mine: true }))).toBe('neutral');
    });
});

describe('teamChips', () => {
    test('returns nothing when the backend has not resolved teams yet', () => {
        expect(teamChips(undefined)).toEqual([]);
        expect(teamChips([])).toEqual([]);
    });

    test('labels a personal request with the handle', () => {
        const [chip] = teamChips([
            team({ name: 'erin', slug: '', org: '', personal: true, mine: true }),
        ]);
        expect(chip.label).toBe('@erin');
        expect(chip.relationship).toBe('personal');
        expect(chip.title).toBe('Requested from you directly — awaiting review');
    });

    test('spells out both dimensions in the tooltip', () => {
        const [mine, theirs] = teamChips([
            team({ mine: true, status: 'changes_requested' }),
            team({ name: 'Security', slug: 'security', status: 'approved' }),
        ]);
        expect(mine.title).toBe('Platform (your team) — changes requested');
        expect(theirs.title).toBe('Security — approved');
    });

    test('preserves the backend order and keys chips by org/slug', () => {
        const chips = teamChips([
            team({ name: 'erin', slug: '', org: '', personal: true, mine: true }),
            team({ name: 'Security', slug: 'security' }),
            team(),
        ]);
        expect(chips.map(c => c.label)).toEqual(['@erin', 'Security', 'Platform']);
        expect(chips.map(c => c.key)).toEqual(['@erin', 'acme/security', 'acme/platform']);
    });

    test('drops entries with no name instead of rendering a blank chip', () => {
        expect(teamChips([team({ name: '' })])).toEqual([]);
    });
});

describe('teamSearchTerms', () => {
    test('offers both the display name and the slug to the filter box', () => {
        expect(teamSearchTerms([team({ name: 'Platform Eng', slug: 'platform-eng' })])).toEqual([
            'Platform Eng',
            'platform-eng',
        ]);
        expect(teamSearchTerms(undefined)).toEqual([]);
    });
});
