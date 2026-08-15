/**
 * Display rules for the required-team chips on a review row.
 *
 * The backend resolves which teams a PR needs a review from — including the
 * ones GitHub has already cleared, which is why a team that approved is still
 * listed — and hands each one a status plus whether it is one of the current
 * user's teams. This module turns that into what a chip actually shows, so the
 * two dimensions the colors encode (where the review stands, and whether it is
 * on you) stay in one testable place.
 */

import type { StatusVariant } from './design';

/** Matches the TeamReviewStatus struct from the Go backend. */
export interface RequiredTeam {
    /** Team display name, or the user's own login on a personal request. */
    name: string;
    slug: string;
    org: string;
    /** "pending" | "approved" | "changes_requested" | "reviewed". */
    status: string;
    /** The current user is on this team (always true for a personal request). */
    mine: boolean;
    /** GitHub asked the current user directly rather than through a team. */
    personal: boolean;
}

/** How a required reviewer relates to the person reading the list. */
export type TeamRelationship = 'personal' | 'mine' | 'theirs';

export interface TeamChip {
    key: string;
    label: string;
    /** Drives the chip's color through the shared status palette. */
    variant: StatusVariant;
    relationship: TeamRelationship;
    /** Tooltip spelling out both dimensions the color compresses. */
    title: string;
}

const STATUS_LABELS: Record<string, string> = {
    pending: 'awaiting review',
    approved: 'approved',
    changes_requested: 'changes requested',
    // The backend's fallback: GitHub cleared the request, so somebody reviewed,
    // but the reviewer could not be tied back to the team (usually a token that
    // can't read the org's team membership).
    reviewed: 'reviewed',
};

export function teamRelationship(team: RequiredTeam): TeamRelationship {
    if (team.personal) return 'personal';
    return team.mine ? 'mine' : 'theirs';
}

/**
 * The chip's color. Status leads — approved is green and changes-requested is
 * red no matter whose team it is — and only a still-pending review looks at the
 * relationship, because "waiting" is worth flagging when it is waiting on you
 * and is just context when it is waiting on someone else.
 */
export function teamVariant(team: RequiredTeam): StatusVariant {
    switch (team.status) {
        case 'approved':
            return 'success';
        case 'changes_requested':
            return 'danger';
        case 'reviewed':
            return 'info';
        case 'pending':
            return team.mine || team.personal ? 'warning' : 'neutral';
        default:
            return 'neutral';
    }
}

function teamTitle(team: RequiredTeam, relationship: TeamRelationship): string {
    const status = STATUS_LABELS[team.status] ?? team.status ?? '';
    if (relationship === 'personal') {
        return `Requested from you directly — ${status}`;
    }
    const who = relationship === 'mine' ? ' (your team)' : '';
    return `${team.name}${who} — ${status}`;
}

/**
 * Build the chips for one row. Entries with no name are dropped rather than
 * rendered blank, and the backend's order is preserved: it already leads with
 * the request that is on you.
 */
export function teamChips(teams: RequiredTeam[] | undefined): TeamChip[] {
    return (teams || [])
        .filter(team => team && team.name)
        .map(team => {
            const relationship = teamRelationship(team);
            return {
                key: team.personal ? `@${team.name}` : `${team.org}/${team.slug}` || team.name,
                label: team.personal ? `@${team.name}` : team.name,
                variant: teamVariant(team),
                relationship,
                title: teamTitle(team, relationship),
            };
        });
}

/** Team names on a row, for free-text search over the list. */
export function teamSearchTerms(teams: RequiredTeam[] | undefined): string[] {
    const terms: string[] = [];
    for (const team of teams || []) {
        if (team.name) terms.push(team.name);
        if (team.slug) terms.push(team.slug);
    }
    return terms;
}
