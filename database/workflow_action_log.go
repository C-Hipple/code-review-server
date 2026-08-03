package database

import (
	"database/sql"
	"strings"
	"time"
)

// Action names recorded in WorkflowActionLog.
const (
	// WorkflowActionCacheWrite is recorded when a workflow persists fetched PR
	// data into the DB caches (diff, comments, reviews, commits, CI, metadata).
	WorkflowActionCacheWrite = "CacheWrite"
	// WorkflowActionAddition / Update / Delete mirror FileChanges.ChangeType and
	// are recorded when a workflow writes the item row itself.
	WorkflowActionAddition = "Addition"
	WorkflowActionUpdate   = "Update"
	WorkflowActionDelete   = "Delete"
)

// WorkflowAction is one row of WorkflowActionLog: a single action a workflow
// took on a single PR, with the head SHA it was working from and the field
// types it wrote at that moment.
//
// The Repo field follows the cache-key convention used by every other cache
// table: the short repo name ("code-review-server"), not "owner/repo".
type WorkflowAction struct {
	ID            int64
	WorkflowName  string
	Action        string
	Owner         string
	Repo          string
	PRNumber      int
	SHA           string
	FieldsWritten []string
	SectionName   string
	Detail        string
	CreatedAt     time.Time
}

// FieldsSummary renders the written field list for logs and reports.
func (a WorkflowAction) FieldsSummary() string {
	if len(a.FieldsWritten) == 0 {
		return "(none)"
	}
	return strings.Join(a.FieldsWritten, ", ")
}

// encodeFields stores the field list as a comma-separated string. Comma
// separation (rather than JSON) keeps the column greppable from the sqlite CLI,
// matching the items.tags convention.
func encodeFields(fields []string) string {
	cleaned := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f != "" {
			cleaned = append(cleaned, f)
		}
	}
	return strings.Join(cleaned, ",")
}

func decodeFields(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// LogWorkflowAction appends an action to WorkflowActionLog and returns the new
// row id. CreatedAt is written explicitly (defaulting to now) so the row does
// not depend on the column default, which a migrated table cannot carry.
func (db *DB) LogWorkflowAction(action WorkflowAction) (int64, error) {
	createdAt := action.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	res, err := db.conn.Exec(
		`INSERT INTO WorkflowActionLog
			(workflow_name, action, owner, repo, pr_number, sha, fields_written, section_name, detail, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		action.WorkflowName, action.Action, action.Owner, action.Repo, action.PRNumber,
		action.SHA, encodeFields(action.FieldsWritten), action.SectionName, action.Detail,
		createdAt.UTC().Format(sqliteTimeLayout),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateWorkflowAction overwrites an existing log row in place. Used when an
// action's outcome is only fully known after the row was written (e.g. more
// fields landed for the same PR in the same pass).
func (db *DB) UpdateWorkflowAction(id int64, action WorkflowAction) error {
	_, err := db.conn.Exec(
		`UPDATE WorkflowActionLog
		 SET workflow_name = ?, action = ?, owner = ?, repo = ?, pr_number = ?,
			 sha = ?, fields_written = ?, section_name = ?, detail = ?
		 WHERE id = ?`,
		action.WorkflowName, action.Action, action.Owner, action.Repo, action.PRNumber,
		action.SHA, encodeFields(action.FieldsWritten), action.SectionName, action.Detail, id,
	)
	return err
}

// DeleteWorkflowAction removes a single log row.
func (db *DB) DeleteWorkflowAction(id int64) error {
	_, err := db.conn.Exec("DELETE FROM WorkflowActionLog WHERE id = ?", id)
	return err
}

// DeleteWorkflowActionsForPR removes every log row for one PR and returns how
// many were deleted.
func (db *DB) DeleteWorkflowActionsForPR(owner, repo string, prNumber int) (int64, error) {
	res, err := db.conn.Exec(
		"DELETE FROM WorkflowActionLog WHERE owner = ? AND repo = ? AND pr_number = ?",
		owner, repo, prNumber,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneWorkflowActionsBefore deletes log rows older than the cutoff. Every
// workflow cycle writes a row per PR it touches, so without pruning the table
// grows without bound.
func (db *DB) PruneWorkflowActionsBefore(cutoff time.Time) (int64, error) {
	res, err := db.conn.Exec(
		"DELETE FROM WorkflowActionLog WHERE created_at < ?",
		cutoff.UTC().Format(sqliteTimeLayout),
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetLatestWorkflowAction returns the most recent action logged for a PR, or
// nil when the workflows have never touched it. This is what the cache miss log
// reads to explain why a field was missing.
func (db *DB) GetLatestWorkflowAction(owner, repo string, prNumber int) (*WorkflowAction, error) {
	actions, err := db.GetWorkflowActions(owner, repo, prNumber, 1)
	if err != nil {
		return nil, err
	}
	if len(actions) == 0 {
		return nil, nil
	}
	return &actions[0], nil
}

// GetLatestWorkflowActionWithField returns the most recent action that wrote a
// specific field (e.g. "reviews"), or nil if no logged action ever wrote it. It
// answers the question the cache miss log actually asks: when did a workflow
// last write the thing we just missed?
func (db *DB) GetLatestWorkflowActionWithField(owner, repo string, prNumber int, field string) (*WorkflowAction, error) {
	if field == "" {
		return nil, nil
	}
	// Match on a whole comma-separated token by padding both sides with commas,
	// so looking for "reviews" cannot match a longer field that merely contains
	// it. Filtering in SQL (rather than scanning every row in Go) lets the
	// per-PR index stop at the first match.
	rows, err := db.conn.Query(
		`SELECT id, workflow_name, action, owner, repo, pr_number, sha, fields_written, section_name, detail, created_at
		 FROM WorkflowActionLog
		 WHERE owner = ? AND repo = ? AND pr_number = ?
		   AND ',' || fields_written || ',' LIKE '%,' || ? || ',%'
		 ORDER BY id DESC
		 LIMIT 1`,
		owner, repo, prNumber, field,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, rows.Err()
	}
	action, err := scanWorkflowAction(rows)
	if err != nil {
		return nil, err
	}
	return &action, nil
}

// GetWorkflowActions returns a PR's log rows, newest first. A limit <= 0 means
// no limit.
func (db *DB) GetWorkflowActions(owner, repo string, prNumber int, limit int) ([]WorkflowAction, error) {
	query := `SELECT id, workflow_name, action, owner, repo, pr_number, sha, fields_written, section_name, detail, created_at
		 FROM WorkflowActionLog
		 WHERE owner = ? AND repo = ? AND pr_number = ?
		 ORDER BY id DESC`
	args := []any{owner, repo, prNumber}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var actions []WorkflowAction
	for rows.Next() {
		action, err := scanWorkflowAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func scanWorkflowAction(rows *sql.Rows) (WorkflowAction, error) {
	var a WorkflowAction
	var fields string
	var createdAt string
	err := rows.Scan(&a.ID, &a.WorkflowName, &a.Action, &a.Owner, &a.Repo, &a.PRNumber,
		&a.SHA, &fields, &a.SectionName, &a.Detail, &createdAt)
	if err != nil {
		return a, err
	}
	a.FieldsWritten = decodeFields(fields)
	a.CreatedAt = parseSQLiteTime(createdAt)
	return a, nil
}

// sqliteTimeLayout is the format SQLite's CURRENT_TIMESTAMP writes, in UTC.
const sqliteTimeLayout = "2006-01-02 15:04:05"

// parseSQLiteTime parses a timestamp column, falling back to RFC3339 for rows
// written by callers that supplied their own value. Returns the zero time when
// neither layout matches, so callers can treat it as "unknown".
func parseSQLiteTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(sqliteTimeLayout, raw); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
