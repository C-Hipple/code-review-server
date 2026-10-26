package database

import (
	"database/sql"
	"time"
)

// FilteredDiff is a PR's diff between two of its commits: from a filtered
// diff's baseline (BaseSHA) to the head the PR had when it was fetched.
//
// Status is the filtereddiff status the pair resolved to — ready, rebased (the
// baseline is no longer in the PR's history) or error — and Message explains
// anything but ready. The diff is GitHub's, unrendered: the server renders it
// when serving, since the file order it applies can change after the fact.
type FilteredDiff struct {
	BaseSHA   string
	HeadSHA   string
	Status    string
	Diff      string
	Message   string
	UpdatedAt time.Time
}

// UpsertFilteredDiff stores the diff for a PR between two commits and drops
// any stored for an older head: a PR's filtered diffs all end at its head, so
// those can never be served again. Like the other cache tables, repo is the
// short repo name.
func (db *DB) UpsertFilteredDiff(owner, repo string, prNumber int, d FilteredDiff) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM PRFilteredDiffs WHERE owner = ? AND repo = ? AND pr_number = ? AND head_sha != ?`,
		owner, repo, prNumber, d.HeadSHA,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO PRFilteredDiffs (owner, repo, pr_number, base_sha, head_sha, status, diff, message, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(owner, repo, pr_number, base_sha, head_sha) DO UPDATE SET
			status = excluded.status,
			diff = excluded.diff,
			message = excluded.message,
			updated_at = excluded.updated_at`,
		owner, repo, prNumber, d.BaseSHA, d.HeadSHA, d.Status, d.Diff, d.Message,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// GetFilteredDiff returns the stored diff for a PR between two commits. ok is
// false when none is stored for that pair.
func (db *DB) GetFilteredDiff(owner, repo string, prNumber int, baseSHA, headSHA string) (d FilteredDiff, ok bool, err error) {
	var updatedAt sql.NullString
	err = db.conn.QueryRow(
		`SELECT base_sha, head_sha, status, diff, message, updated_at
		 FROM PRFilteredDiffs
		 WHERE owner = ? AND repo = ? AND pr_number = ? AND base_sha = ? AND head_sha = ?`,
		owner, repo, prNumber, baseSHA, headSHA,
	).Scan(&d.BaseSHA, &d.HeadSHA, &d.Status, &d.Diff, &d.Message, &updatedAt)
	if err == sql.ErrNoRows {
		return FilteredDiff{}, false, nil
	}
	if err != nil {
		return FilteredDiff{}, false, err
	}
	d.UpdatedAt = parseSQLiteTime(updatedAt.String)
	return d, true, nil
}
