package database

import (
	"database/sql"
	"time"
)

// AIResult is the stored outcome of one AI feature for one PR.
//
// SHA and InputHash are the two halves of the key the result was computed
// for: the PR's head SHA and the digest of its discussion (comments, review
// states, thread resolution). A result only answers for the PR as it is now
// when both still match, so readers compare them against the current values
// rather than trusting the row on its own.
//
// Status is always a terminal one (success, error, insufficient-input). A run
// in flight is tracked in memory by the runner, not here, so a server restart
// cannot leave a row claiming to be pending forever.
type AIResult struct {
	Result    string
	Status    string
	SHA       string
	InputHash string
	UpdatedAt time.Time
}

// UpsertAIResult stores a feature's result for a PR, replacing any earlier one.
// Like the other cache tables, repo is the short repo name.
func (db *DB) UpsertAIResult(owner, repo string, prNumber int, featureName, result, status, sha, inputHash string) error {
	_, err := db.conn.Exec(
		`INSERT INTO AIResults (owner, repo, pr_number, feature_name, result, status, sha, input_hash, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(owner, repo, pr_number, feature_name) DO UPDATE SET
			result = excluded.result,
			status = excluded.status,
			sha = excluded.sha,
			input_hash = excluded.input_hash,
			updated_at = excluded.updated_at`,
		owner, repo, prNumber, featureName, result, status, sha, inputHash,
	)
	return err
}

// GetAIResults returns every stored AI feature result for a PR, keyed by
// feature name. A PR no feature has run for yields an empty map.
func (db *DB) GetAIResults(owner, repo string, prNumber int) (map[string]AIResult, error) {
	rows, err := db.conn.Query(
		`SELECT feature_name, result, status, sha, input_hash, updated_at
		 FROM AIResults WHERE owner = ? AND repo = ? AND pr_number = ?`,
		owner, repo, prNumber,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make(map[string]AIResult)
	for rows.Next() {
		var name string
		res, err := scanAIResult(rows, &name)
		if err != nil {
			return nil, err
		}
		results[name] = res
	}
	return results, rows.Err()
}

// GetAIResult returns one feature's stored result for a PR. ok is false when
// the feature has never produced a result for it.
func (db *DB) GetAIResult(owner, repo string, prNumber int, featureName string) (res AIResult, ok bool, err error) {
	row := db.conn.QueryRow(
		`SELECT feature_name, result, status, sha, input_hash, updated_at
		 FROM AIResults WHERE owner = ? AND repo = ? AND pr_number = ? AND feature_name = ?`,
		owner, repo, prNumber, featureName,
	)
	var name string
	res, err = scanAIResult(row, &name)
	if err == sql.ErrNoRows {
		return AIResult{}, false, nil
	}
	if err != nil {
		return AIResult{}, false, err
	}
	return res, true, nil
}

// scanAIResult reads one AIResults row selected in the column order the
// queries above use.
func scanAIResult(s interface{ Scan(...any) error }, name *string) (AIResult, error) {
	var res AIResult
	var updatedAt sql.NullString
	if err := s.Scan(name, &res.Result, &res.Status, &res.SHA, &res.InputHash, &updatedAt); err != nil {
		return AIResult{}, err
	}
	res.UpdatedAt = parseSQLiteTime(updatedAt.String)
	return res, nil
}
