package database

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

type Section struct {
	ID          int64
	SectionName string
	Priority    int
}

type Item struct {
	ID          int64
	SectionID   int64
	Identifier  string
	Status      string
	Title       string
	DetailsJSON string // JSON array of detail lines
	Tags        string // Comma-separated tags
	TTL         int64
	CreatedAt   time.Time
	Workflows   string // JSON array of workflow names that maintain this item
}

type LocalComment struct {
	ID        int64
	Owner     string // GitHub owner/org
	Repo      string // GitHub repository name
	Number    int    // PR number
	Filename  string // going to be the rel file like src/main.rs
	Position  int64
	Body      *string
	ReplyToID *int64 // ID of the comment being replied to, or nil if top-level
}

func NewDB(dbPath string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	conn, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=1&_busy_timeout=30000")
	if err != nil {
		return nil, err
	}

	db := &DB{conn: conn}
	conn.SetMaxOpenConns(1)

	// Enable WAL mode and other optimizations
	_, err = conn.Exec("PRAGMA journal_mode=WAL;")
	if err != nil {
		slog.Error("Failed to enable WAL mode", "error", err)
	}
	_, err = conn.Exec("PRAGMA synchronous=NORMAL;")
	if err != nil {
		slog.Error("Failed to set synchronous mode", "error", err)
	}

	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, err
	}

	slog.Info("Database connection established and schema initialized", "path", dbPath)
	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		section_name TEXT NOT NULL,
		priority INTEGER DEFAULT 0,
		UNIQUE(section_name)
	);

	CREATE TABLE IF NOT EXISTS items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		section_id INTEGER NOT NULL,
		identifier TEXT NOT NULL,
		status TEXT NOT NULL,
		title TEXT NOT NULL,
		details_json TEXT NOT NULL,
		tags TEXT DEFAULT '',
		ttl INTEGER DEFAULT 0,
		workflows TEXT NOT NULL DEFAULT '[]',
		UNIQUE(section_id, identifier),
		FOREIGN KEY(section_id) REFERENCES sections(id) ON DELETE CASCADE
	);

		CREATE TABLE IF NOT EXISTS LocalComment (
			id INTEGER PRIMARY KEY,
			owner TEXT NOT NULL,
			repo TEXT NOT NULL,
			number INTEGER NOT NULL,
			filename TEXT NOT NULL,
			position INTEGER NOT NULL,
			body TEXT,
			reply_to_id INTEGER
		);

		CREATE TABLE IF NOT EXISTS Feedback (
			id INTEGER PRIMARY KEY,
			owner TEXT NOT NULL,
			repo TEXT NOT NULL,
			number INTEGER NOT NULL,
			body TEXT,
			UNIQUE(owner, repo, number)
		);

	CREATE TABLE IF NOT EXISTS PullRequests (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		latest_sha TEXT NOT NULL,
		body TEXT NOT NULL,
		UNIQUE(pr_number, repo, latest_sha)
	);

	CREATE TABLE IF NOT EXISTS PRComments (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		comments_json TEXT NOT NULL,
		UNIQUE(pr_number, repo)
	);

	CREATE TABLE IF NOT EXISTS RequestedReviewers (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		reviewers_json TEXT NOT NULL,
		UNIQUE(pr_number, repo)
	);

	CREATE TABLE IF NOT EXISTS PRReviews (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		reviews_json TEXT NOT NULL,
		UNIQUE(pr_number, repo)
	);

	CREATE TABLE IF NOT EXISTS PRCommits (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		commits_json TEXT NOT NULL,
		UNIQUE(pr_number, repo)
	);

	CREATE TABLE IF NOT EXISTS PRReviewThreads (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		threads_json TEXT NOT NULL,
		UNIQUE(pr_number, repo)
	);

	CREATE TABLE IF NOT EXISTS CIStatus (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		sha TEXT NOT NULL,
		status_json TEXT NOT NULL,
		UNIQUE(pr_number, repo, sha)
	);

	CREATE TABLE IF NOT EXISTS PRMetadataCache (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		owner TEXT NOT NULL,
		metadata_json TEXT NOT NULL,
		cached_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(pr_number, repo, owner)
	);

	CREATE TABLE IF NOT EXISTS PluginResults (
		id INTEGER PRIMARY KEY,
		owner TEXT NOT NULL,
		repo TEXT NOT NULL,
		pr_number INTEGER NOT NULL,
		plugin_name TEXT NOT NULL,
		result TEXT NOT NULL,
		status TEXT DEFAULT 'success',
		sha TEXT DEFAULT '',
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(owner, repo, pr_number, plugin_name)
	);

	CREATE TABLE IF NOT EXISTS DiffFileOrderingCache (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		sha TEXT NOT NULL,
		ordering_json TEXT NOT NULL,
		review_ease TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(pr_number, repo, sha)
	);

	CREATE TABLE IF NOT EXISTS Worktrees (
		id INTEGER PRIMARY KEY,
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		owner TEXT NOT NULL,
		path TEXT NOT NULL,
		branch TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(pr_number, repo, owner)
	);

	CREATE INDEX IF NOT EXISTS idx_items_section ON items(section_id);
	CREATE INDEX IF NOT EXISTS idx_items_identifier ON items(identifier);
	CREATE INDEX IF NOT EXISTS idx_pullrequests_lookup ON PullRequests(pr_number, repo, latest_sha);
	CREATE INDEX IF NOT EXISTS idx_prcomments_lookup ON PRComments(pr_number, repo);
	CREATE INDEX IF NOT EXISTS idx_prcommits_lookup ON PRCommits(pr_number, repo);
	CREATE INDEX IF NOT EXISTS idx_localcomments_pr ON LocalComment(owner, repo, number);
	CREATE INDEX IF NOT EXISTS idx_plugin_results_pr ON PluginResults(owner, repo, pr_number);
	CREATE INDEX IF NOT EXISTS idx_prreviews_lookup ON PRReviews(pr_number, repo);

	CREATE TABLE IF NOT EXISTS WorkflowCycleLog (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		completed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS WorkflowActionLog (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		workflow_name TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		repo TEXT NOT NULL DEFAULT '',
		pr_number INTEGER NOT NULL DEFAULT 0,
		sha TEXT NOT NULL DEFAULT '',
		fields_written TEXT NOT NULL DEFAULT '',
		section_name TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS APICallStats (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		recorded_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		pr_list INTEGER NOT NULL DEFAULT 0,
		pr_specific INTEGER NOT NULL DEFAULT 0,
		comments INTEGER NOT NULL DEFAULT 0,
		issue_comments INTEGER NOT NULL DEFAULT 0,
		ci_status INTEGER NOT NULL DEFAULT 0,
		diff INTEGER NOT NULL DEFAULT 0,
		reviews INTEGER NOT NULL DEFAULT 0,
		combined_status INTEGER NOT NULL DEFAULT 0,
		check_runs INTEGER NOT NULL DEFAULT 0,
		commits INTEGER NOT NULL DEFAULT 0,
		total INTEGER NOT NULL DEFAULT 0
	);
	`

	_, err := db.conn.Exec(schema)
	if err != nil {
		return err
	}

	// Migration: Add PR columns to LocalComment table if they don't exist
	// Check if owner column exists by querying pragma_table_info
	var count int
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('LocalComment') WHERE name='owner'").Scan(&count)
	if err == nil && count == 0 {
		// Add the new columns (Legacy migration code kept for completeness)
		_, err = db.conn.Exec("ALTER TABLE LocalComment ADD COLUMN owner TEXT DEFAULT ''")
		if err != nil {
			slog.Warn("Error adding owner column to LocalComment (may already exist)", "error", err)
		}
		_, err = db.conn.Exec("ALTER TABLE LocalComment ADD COLUMN repo TEXT DEFAULT ''")
		if err != nil {
			slog.Warn("Error adding repo column to LocalComment (may already exist)", "error", err)
		}
		_, err = db.conn.Exec("ALTER TABLE LocalComment ADD COLUMN number INTEGER DEFAULT 0")
		if err != nil {
			slog.Warn("Error adding number column to LocalComment (may already exist)", "error", err)
		}
		// Update existing rows that might have NULL values
		_, err = db.conn.Exec("UPDATE LocalComment SET owner = '' WHERE owner IS NULL")
		if err != nil {
			slog.Warn("Error updating owner defaults", "error", err)
		}
		_, err = db.conn.Exec("UPDATE LocalComment SET repo = '' WHERE repo IS NULL")
		if err != nil {
			slog.Warn("Error updating repo defaults", "error", err)
		}
		_, err = db.conn.Exec("UPDATE LocalComment SET number = 0 WHERE number IS NULL")
		if err != nil {
			slog.Warn("Error updating number defaults", "error", err)
		}
	}

	// Migration: Add reply_to_id column
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('LocalComment') WHERE name='reply_to_id'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE LocalComment ADD COLUMN reply_to_id INTEGER DEFAULT NULL")
		if err != nil {
			slog.Warn("Error adding reply_to_id column to LocalComment", "error", err)
		}
	}
	// Migration: Add status/sha columns to PluginResults if they don't exist.
	// Guard with table existence check: pragma_table_info returns 0 rows for
	// non-existent tables (no error), which would incorrectly trigger ALTER TABLE.
	var pluginResultsExists int
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='PluginResults'").Scan(&pluginResultsExists)
	if pluginResultsExists > 0 {
		err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('PluginResults') WHERE name='status'").Scan(&count)
		if err == nil && count == 0 {
			_, err = db.conn.Exec("ALTER TABLE PluginResults ADD COLUMN status TEXT DEFAULT 'success'")
			if err != nil {
				slog.Warn("Error adding status column to PluginResults", "error", err)
			}
		}
		err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('PluginResults') WHERE name='sha'").Scan(&count)
		if err == nil && count == 0 {
			_, err = db.conn.Exec("ALTER TABLE PluginResults ADD COLUMN sha TEXT DEFAULT ''")
			if err != nil {
				slog.Warn("Error adding sha column to PluginResults", "error", err)
			}
		}
	}
	// Migration: Add review_ease column to DiffFileOrderingCache if it doesn't
	// exist. Same table-existence guard as the PluginResults migration above.
	var orderingCacheExists int
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='DiffFileOrderingCache'").Scan(&orderingCacheExists)
	if orderingCacheExists > 0 {
		err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('DiffFileOrderingCache') WHERE name='review_ease'").Scan(&count)
		if err == nil && count == 0 {
			_, err = db.conn.Exec("ALTER TABLE DiffFileOrderingCache ADD COLUMN review_ease TEXT NOT NULL DEFAULT ''")
			if err != nil {
				slog.Warn("Error adding review_ease column to DiffFileOrderingCache", "error", err)
			}
		}
	}
	// Migration: Add ttl column to items if it doesn't exist
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('items') WHERE name='ttl'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE items ADD COLUMN ttl INTEGER DEFAULT 0")
		if err != nil {
			slog.Warn("Error adding ttl column to items", "error", err)
		}
	}

	// Migration: Add priority column to sections if it doesn't exist
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('sections') WHERE name='priority'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE sections ADD COLUMN priority INTEGER DEFAULT 0")
		if err != nil {
			slog.Warn("Error adding priority column to sections", "error", err)
		}
	}

	// Migration: Add created_at column to items if it doesn't exist
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('items') WHERE name='created_at'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE items ADD COLUMN created_at TIMESTAMP DEFAULT NULL")
		if err != nil {
			slog.Warn("Error adding created_at column to items", "error", err)
		}
	}

	// Migration: Add release_status column to PRMetadataCache if it doesn't exist
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('PRMetadataCache') WHERE name='release_status'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE PRMetadataCache ADD COLUMN release_status TEXT DEFAULT ''")
		if err != nil {
			slog.Warn("Error adding release_status column to PRMetadataCache", "error", err)
		}
	}

	// Migration: Add base_sha column to PullRequests if it doesn't exist
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('PullRequests') WHERE name='base_sha'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE PullRequests ADD COLUMN base_sha TEXT DEFAULT ''")
		if err != nil {
			slog.Warn("Error adding base_sha column to PullRequests", "error", err)
		}
	}

	// Migration: Add workflows column to items if it doesn't exist
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('items') WHERE name='workflows'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE items ADD COLUMN workflows TEXT NOT NULL DEFAULT '[]'")
		if err != nil {
			slog.Warn("Error adding workflows column to items", "error", err)
		}
	}

	// Migration: Add rate limit columns to APICallStats
	err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('APICallStats') WHERE name='rate_limit_remaining'").Scan(&count)
	if err == nil && count == 0 {
		_, err = db.conn.Exec("ALTER TABLE APICallStats ADD COLUMN rate_limit_remaining INTEGER NOT NULL DEFAULT -1")
		if err != nil {
			slog.Warn("Error adding rate_limit_remaining column to APICallStats", "error", err)
		}
		_, err = db.conn.Exec("ALTER TABLE APICallStats ADD COLUMN rate_limit_limit INTEGER NOT NULL DEFAULT -1")
		if err != nil {
			slog.Warn("Error adding rate_limit_limit column to APICallStats", "error", err)
		}
		_, err = db.conn.Exec("ALTER TABLE APICallStats ADD COLUMN rate_limit_reset_at TEXT NOT NULL DEFAULT ''")
		if err != nil {
			slog.Warn("Error adding rate_limit_reset_at column to APICallStats", "error", err)
		}
	}

	// Migration: bring an existing WorkflowActionLog up to the current column
	// set. The CREATE TABLE above only fires for fresh databases, so a table
	// written by an earlier version of this schema keeps whatever columns it had.
	// Same table-existence guard as the migrations above: pragma_table_info
	// returns 0 rows for a missing table, which would otherwise trigger ALTERs
	// against a table that does not exist.
	var workflowActionLogExists int
	_ = db.conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='WorkflowActionLog'").Scan(&workflowActionLogExists)
	if workflowActionLogExists > 0 {
		workflowActionLogColumns := []struct {
			name string
			ddl  string
		}{
			{"workflow_name", "ALTER TABLE WorkflowActionLog ADD COLUMN workflow_name TEXT NOT NULL DEFAULT ''"},
			{"action", "ALTER TABLE WorkflowActionLog ADD COLUMN action TEXT NOT NULL DEFAULT ''"},
			{"owner", "ALTER TABLE WorkflowActionLog ADD COLUMN owner TEXT NOT NULL DEFAULT ''"},
			{"repo", "ALTER TABLE WorkflowActionLog ADD COLUMN repo TEXT NOT NULL DEFAULT ''"},
			{"pr_number", "ALTER TABLE WorkflowActionLog ADD COLUMN pr_number INTEGER NOT NULL DEFAULT 0"},
			{"sha", "ALTER TABLE WorkflowActionLog ADD COLUMN sha TEXT NOT NULL DEFAULT ''"},
			{"fields_written", "ALTER TABLE WorkflowActionLog ADD COLUMN fields_written TEXT NOT NULL DEFAULT ''"},
			{"section_name", "ALTER TABLE WorkflowActionLog ADD COLUMN section_name TEXT NOT NULL DEFAULT ''"},
			{"detail", "ALTER TABLE WorkflowActionLog ADD COLUMN detail TEXT NOT NULL DEFAULT ''"},
			// SQLite refuses to ADD COLUMN with a non-constant default, so the
			// migrated column defaults to empty rather than CURRENT_TIMESTAMP.
			// LogWorkflowAction always writes created_at explicitly, so nothing
			// depends on the column default.
			{"created_at", "ALTER TABLE WorkflowActionLog ADD COLUMN created_at TIMESTAMP NOT NULL DEFAULT ''"},
		}
		for _, col := range workflowActionLogColumns {
			err = db.conn.QueryRow("SELECT COUNT(*) FROM pragma_table_info('WorkflowActionLog') WHERE name=?", col.name).Scan(&count)
			if err == nil && count == 0 {
				if _, err = db.conn.Exec(col.ddl); err != nil {
					slog.Warn("Error adding column to WorkflowActionLog", "column", col.name, "error", err)
				}
			}
		}

		// Indexed after the column migration, not in the schema block above: on a
		// legacy table missing these columns, indexing them up front fails the
		// whole schema statement and takes NewDB down with it.
		indexes := []string{
			"CREATE INDEX IF NOT EXISTS idx_workflow_action_log_pr ON WorkflowActionLog(owner, repo, pr_number, id DESC)",
			"CREATE INDEX IF NOT EXISTS idx_workflow_action_log_created ON WorkflowActionLog(created_at)",
		}
		for _, idx := range indexes {
			if _, err = db.conn.Exec(idx); err != nil {
				slog.Warn("Error creating WorkflowActionLog index", "error", err)
			}
		}
	}

	return nil
}

func (db *DB) AddWorktree(prNumber int, repo, owner, path, branch string) error {
	_, err := db.conn.Exec(
		`INSERT INTO Worktrees (pr_number, repo, owner, path, branch)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(pr_number, repo, owner) DO UPDATE SET
			path = excluded.path,
			branch = excluded.branch,
			created_at = CURRENT_TIMESTAMP`,
		prNumber, repo, owner, path, branch,
	)
	return err
}

func (db *DB) GetWorktree(prNumber int, repo, owner string) (string, error) {
	var path string
	err := db.conn.QueryRow(
		"SELECT path FROM Worktrees WHERE pr_number = ? AND repo = ? AND owner = ?",
		prNumber, repo, owner,
	).Scan(&path)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

func (db *DB) RemoveWorktreeRecord(prNumber int, repo, owner string) error {
	_, err := db.conn.Exec(
		"DELETE FROM Worktrees WHERE pr_number = ? AND repo = ? AND owner = ?",
		prNumber, repo, owner,
	)
	return err
}

func (db *DB) UpsertPluginResult(owner, repo string, prNumber int, pluginName string, result string, status string, sha string) error {
	_, err := db.conn.Exec(
		`INSERT INTO PluginResults (owner, repo, pr_number, plugin_name, result, status, sha, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(owner, repo, pr_number, plugin_name) DO UPDATE SET
			result = excluded.result,
			status = excluded.status,
			sha = excluded.sha,
			updated_at = excluded.updated_at`,
		owner, repo, prNumber, pluginName, result, status, sha,
	)
	return err
}

type PluginResult struct {
	Result string `json:"result"`
	Status string `json:"status"`
}

func (db *DB) GetPluginResults(owner, repo string, prNumber int) (map[string]PluginResult, error) {
	rows, err := db.conn.Query(
		"SELECT plugin_name, result, status FROM PluginResults WHERE owner = ? AND repo = ? AND pr_number = ?",
		owner, repo, prNumber,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make(map[string]PluginResult)
	for rows.Next() {
		var name, content, status string
		// Handle potential NULL status for very old records if migration failed somewhat, though we set default.
		// Actually, Scan handles basic types.
		if err := rows.Scan(&name, &content, &status); err != nil {
			return nil, err
		}
		results[name] = PluginResult{
			Result: content,
			Status: status,
		}
	}
	return results, nil
}

// GetPluginResultSHA retrieves the SHA for which a plugin was last run
func (db *DB) GetPluginResultSHA(owner, repo string, prNumber int, pluginName string) (string, error) {
	var sha string
	err := db.conn.QueryRow(
		"SELECT sha FROM PluginResults WHERE owner = ? AND repo = ? AND pr_number = ? AND plugin_name = ?",
		owner, repo, prNumber, pluginName,
	).Scan(&sha)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sha, nil
}

// DeletePluginResultsForPR clears plugin results for a PR to force rerun
// If pluginName is empty, all plugin results for the PR are deleted
func (db *DB) DeletePluginResultsForPR(owner, repo string, prNumber int, pluginName string) error {
	if pluginName == "" {
		// Delete all plugin results for this PR
		_, err := db.conn.Exec(
			"DELETE FROM PluginResults WHERE owner = ? AND repo = ? AND pr_number = ?",
			owner, repo, prNumber,
		)
		return err
	}
	// Delete specific plugin result
	_, err := db.conn.Exec(
		"DELETE FROM PluginResults WHERE owner = ? AND repo = ? AND pr_number = ? AND plugin_name = ?",
		owner, repo, prNumber, pluginName,
	)
	return err
}

// GetDiffFileOrdering retrieves a cached LLM diff file ordering for a PR SHA.
// It returns an empty string (no error) when there is no cached entry.
func (db *DB) GetDiffFileOrdering(prNumber int, repo, sha string) (string, error) {
	var orderingJSON string
	err := db.conn.QueryRow(
		"SELECT ordering_json FROM DiffFileOrderingCache WHERE pr_number = ? AND repo = ? AND sha = ?",
		prNumber, repo, sha,
	).Scan(&orderingJSON)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return orderingJSON, nil
}

// UpsertDiffFileOrdering stores an LLM diff file ordering keyed by PR SHA.
func (db *DB) UpsertDiffFileOrdering(prNumber int, repo, sha, orderingJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO DiffFileOrderingCache (pr_number, repo, sha, ordering_json, updated_at)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(pr_number, repo, sha) DO UPDATE SET
			ordering_json = excluded.ordering_json,
			updated_at = excluded.updated_at`,
		prNumber, repo, sha, orderingJSON,
	)
	return err
}

// GetReviewEase retrieves a cached LLM review-ease rating for a PR SHA.
// It returns an empty string (no error) when there is no cached entry.
func (db *DB) GetReviewEase(prNumber int, repo, sha string) (string, error) {
	var ease string
	err := db.conn.QueryRow(
		"SELECT review_ease FROM DiffFileOrderingCache WHERE pr_number = ? AND repo = ? AND sha = ?",
		prNumber, repo, sha,
	).Scan(&ease)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ease, nil
}

// GetLatestReviewEase returns the most recently stored review-ease rating for
// a PR across all of its SHAs, so callers that don't know the current head SHA
// (and PRs whose newest revision hasn't been rated yet) still get the latest
// known rating. Returns an empty string (no error) when no rating is stored.
func (db *DB) GetLatestReviewEase(prNumber int, repo string) (string, error) {
	var ease string
	err := db.conn.QueryRow(
		`SELECT review_ease FROM DiffFileOrderingCache
		 WHERE pr_number = ? AND repo = ? AND review_ease != ''
		 ORDER BY updated_at DESC LIMIT 1`,
		prNumber, repo,
	).Scan(&ease)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ease, nil
}

// UpsertReviewEase stores an LLM review-ease rating keyed by PR SHA, leaving
// any cached file ordering for the same SHA untouched.
func (db *DB) UpsertReviewEase(prNumber int, repo, sha, ease string) error {
	_, err := db.conn.Exec(
		`INSERT INTO DiffFileOrderingCache (pr_number, repo, sha, ordering_json, review_ease, updated_at)
		 VALUES (?, ?, ?, '', ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(pr_number, repo, sha) DO UPDATE SET
			review_ease = excluded.review_ease,
			updated_at = excluded.updated_at`,
		prNumber, repo, sha, ease,
	)
	return err
}

func (db *DB) GetOrCreateSection(sectionName string, priority int) (*Section, error) {
	var section Section
	err := db.conn.QueryRow(
		"SELECT id, section_name, priority FROM sections WHERE section_name = ?",
		sectionName,
	).Scan(&section.ID, &section.SectionName, &section.Priority)

	if err == sql.ErrNoRows {
		result, err := db.conn.Exec(
			"INSERT INTO sections (section_name, priority) VALUES (?, ?)",
			sectionName, priority,
		)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		slog.Info("Created new section", "section", sectionName, "id", id)
		section = Section{
			ID:          id,
			SectionName: sectionName,
			Priority:    priority,
		}
		return &section, nil
	} else if err != nil {
		return nil, err
	}

	// If priority changed, update it
	if section.Priority != priority {
		_, err := db.conn.Exec("UPDATE sections SET priority = ? WHERE id = ?", priority, section.ID)
		if err != nil {
			slog.Warn("Failed to update section priority", "section", sectionName, "error", err)
		} else {
			section.Priority = priority
		}
	}

	return &section, nil
}

func (db *DB) GetSection(sectionName string) (*Section, error) {
	var section Section
	err := db.conn.QueryRow(
		"SELECT id, section_name, priority FROM sections WHERE section_name = ?",
		sectionName,
	).Scan(&section.ID, &section.SectionName, &section.Priority)

	if err != nil {
		return nil, err
	}
	return &section, nil
}

func (db *DB) GetAllSections() ([]*Section, error) {
	rows, err := db.conn.Query("SELECT id, section_name, priority FROM sections ORDER BY priority ASC, section_name ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sections []*Section
	for rows.Next() {
		var section Section
		if err := rows.Scan(&section.ID, &section.SectionName, &section.Priority); err != nil {
			return nil, err
		}
		sections = append(sections, &section)
	}
	return sections, rows.Err()
}

func (db *DB) UpsertItem(sectionID int64, identifier, status, title string, details []string, tags []string, ttl int64) (*Item, error) {
	return db.UpsertItemWithWorkflow(sectionID, identifier, status, title, details, tags, ttl, "")
}

// UpsertItemWithWorkflow upserts an item and merges workflowName into the
// item's `workflows` ownership list. Pass an empty string to leave ownership
// unchanged (compatibility with callers that don't track workflow ownership).
func (db *DB) UpsertItemWithWorkflow(sectionID int64, identifier, status, title string, details []string, tags []string, ttl int64, workflowName string) (*Item, error) {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return nil, err
	}

	tagsStr := ""
	if len(tags) > 0 {
		tagsBytes, err := json.Marshal(tags)
		if err != nil {
			return nil, err
		}
		tagsStr = string(tagsBytes)
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Read current workflows list (if any) so we can merge in workflowName.
	var existingWorkflowsJSON string
	err = tx.QueryRow(
		"SELECT workflows FROM items WHERE section_id = ? AND identifier = ?",
		sectionID, identifier,
	).Scan(&existingWorkflowsJSON)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	workflows := decodeWorkflowList(existingWorkflowsJSON)
	if workflowName != "" && !containsString(workflows, workflowName) {
		workflows = append(workflows, workflowName)
	}
	workflowsJSON, err := json.Marshal(workflows)
	if err != nil {
		return nil, err
	}

	result, err := tx.Exec(
		`INSERT INTO items (section_id, identifier, status, title, details_json, tags, ttl, workflows)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(section_id, identifier) DO UPDATE SET
			status = excluded.status,
			title = excluded.title,
			details_json = excluded.details_json,
			tags = excluded.tags,
			ttl = excluded.ttl,
			workflows = excluded.workflows`,
		sectionID, identifier, status, title, string(detailsJSON), tagsStr, ttl, string(workflowsJSON),
	)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil || id == 0 {
		var existingID int64
		err := tx.QueryRow(
			"SELECT id FROM items WHERE section_id = ? AND identifier = ?",
			sectionID, identifier,
		).Scan(&existingID)
		if err != nil {
			return nil, err
		}
		id = existingID
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	item := &Item{
		ID:          id,
		SectionID:   sectionID,
		Identifier:  identifier,
		Status:      status,
		Title:       title,
		DetailsJSON: string(detailsJSON),
		Tags:        tagsStr,
		TTL:         ttl,
		Workflows:   string(workflowsJSON),
	}

	return item, nil
}

// RemoveWorkflowFromItem removes workflowName from the item's ownership list.
// The row is left in place even if the list becomes empty; PruneOrphanedItems
// is responsible for deleting items that no workflow maintains.
func (db *DB) RemoveWorkflowFromItem(sectionID int64, identifier, workflowName string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var workflowsJSON string
	err = tx.QueryRow(
		"SELECT workflows FROM items WHERE section_id = ? AND identifier = ?",
		sectionID, identifier,
	).Scan(&workflowsJSON)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	workflows := decodeWorkflowList(workflowsJSON)
	updated := workflows[:0]
	for _, w := range workflows {
		if w != workflowName {
			updated = append(updated, w)
		}
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		"UPDATE items SET workflows = ? WHERE section_id = ? AND identifier = ?",
		string(encoded), sectionID, identifier,
	); err != nil {
		return err
	}
	return tx.Commit()
}

// PruneOrphanedItems deletes every item whose workflow ownership list is
// empty. Returns the number of rows deleted.
func (db *DB) PruneOrphanedItems() (int64, error) {
	res, err := db.conn.Exec(
		`DELETE FROM items WHERE workflows = '' OR workflows = '[]' OR workflows IS NULL`,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func decodeWorkflowList(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func (db *DB) GetItem(sectionID int64, identifier string) (*Item, error) {
	var item Item
	err := db.conn.QueryRow(
		"SELECT id, section_id, identifier, status, title, details_json, tags, ttl, COALESCE(workflows, '[]') FROM items WHERE section_id = ? AND identifier = ?",
		sectionID, identifier,
	).Scan(&item.ID, &item.SectionID, &item.Identifier, &item.Status, &item.Title, &item.DetailsJSON, &item.Tags, &item.TTL, &item.Workflows)

	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (db *DB) GetItemsBySection(sectionID int64) ([]*Item, error) {
	rows, err := db.conn.Query(
		"SELECT id, section_id, identifier, status, title, details_json, tags, ttl, COALESCE(created_at, CURRENT_TIMESTAMP), COALESCE(workflows, '[]') FROM items WHERE section_id = ? ORDER BY id",
		sectionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*Item
	for rows.Next() {
		var item Item
		var createdAtStr string
		if err := rows.Scan(&item.ID, &item.SectionID, &item.Identifier, &item.Status, &item.Title, &item.DetailsJSON, &item.Tags, &item.TTL, &createdAtStr, &item.Workflows); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		items = append(items, &item)
	}
	return items, rows.Err()
}

func (db *DB) GetAllItems() ([]*Item, error) {
	rows, err := db.conn.Query(
		"SELECT id, section_id, identifier, status, title, details_json, tags, ttl, COALESCE(created_at, CURRENT_TIMESTAMP), COALESCE(workflows, '[]') FROM items ORDER BY section_id, id",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*Item
	for rows.Next() {
		var item Item
		var createdAtStr string
		if err := rows.Scan(&item.ID, &item.SectionID, &item.Identifier, &item.Status, &item.Title, &item.DetailsJSON, &item.Tags, &item.TTL, &createdAtStr, &item.Workflows); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAtStr)
		items = append(items, &item)
	}
	return items, rows.Err()
}

func (db *DB) GetExpiredItems(sectionID int64) ([]*Item, error) {
	now := time.Now().Unix()
	rows, err := db.conn.Query(
		"SELECT id, section_id, identifier, status, title, details_json, tags, ttl, COALESCE(workflows, '[]') FROM items WHERE section_id = ? AND ttl > 0 AND ttl < ?",
		sectionID, now,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*Item
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.SectionID, &item.Identifier, &item.Status, &item.Title, &item.DetailsJSON, &item.Tags, &item.TTL, &item.Workflows); err != nil {
			return nil, err
		}
		items = append(items, &item)
	}
	return items, rows.Err()
}

func (db *DB) DeleteItem(sectionID int64, identifier string) error {
	_, err := db.conn.Exec(
		"DELETE FROM items WHERE section_id = ? AND identifier = ?",
		sectionID, identifier,
	)
	return err
}

func (db *DB) GetItemCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM items").Scan(&count)
	return count, err
}

func (db *DB) DeleteItemByIdentifier(identifier string) error {
	_, err := db.conn.Exec(
		"DELETE FROM items WHERE identifier = ?",
		identifier,
	)
	return err
}

func (db *DB) DeleteItemsNotInList(sectionID int64, identifiers []string) error {
	if len(identifiers) == 0 {
		// Delete all items in section
		_, err := db.conn.Exec("DELETE FROM items WHERE section_id = ?", sectionID)
		return err
	}

	// Build placeholders for IN clause
	placeholders := ""
	args := []interface{}{sectionID}
	for i, id := range identifiers {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, id)
	}

	query := "DELETE FROM items WHERE section_id = ? AND identifier NOT IN (" + placeholders + ")"
	_, err := db.conn.Exec(query, args...)
	return err
}

func (db *DB) InsertLocalComment(owner, repo string, number int, filename string, position int64, body *string, replyToID *int64) (LocalComment, error) {
	stmt, err := db.conn.Prepare("INSERT INTO LocalComment (owner, repo, number, filename, position, body, reply_to_id) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		slog.Error("Failed to prepare statement", "error", err)
		return LocalComment{}, err
	}
	defer stmt.Close()

	// Execute the insertion
	res, err := stmt.Exec(owner, repo, number, filename, position, body, replyToID)
	if err != nil {
		slog.Error("Failed to execute insertion", "error", err)
		return LocalComment{}, err
	}

	// Get the last inserted ID
	id, err := res.LastInsertId()
	if err != nil {
		slog.Error("Failed to get last insert ID", "error", err)
		return LocalComment{}, err
	}
	return LocalComment{
		ID: id, Owner: owner, Repo: repo, Number: number, Filename: filename, Position: position, Body: body, ReplyToID: replyToID,
	}, nil
}

func (db *DB) InsertFeedback(owner, repo string, number int, body *string) error {
	stmt, err := db.conn.Prepare(
		`INSERT INTO Feedback (owner, repo, number, body) VALUES (?, ?, ?, ?)
		 ON CONFLICT(owner, repo, number) DO UPDATE SET
			body = excluded.body`,
	)
	if err != nil {
		slog.Error("Failed to prepare feedback statement", "error", err)
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(owner, repo, number, body)
	if err != nil {
		slog.Error("Failed to execute feedback insertion", "error", err)
		return err
	}
	return nil
}

func (db *DB) GetFeedback(owner, repo string, number int) (string, error) {
	var body string
	err := db.conn.QueryRow(
		`SELECT body FROM Feedback WHERE owner = ? AND repo = ? AND number = ?`,
		owner, repo, number,
	).Scan(&body)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return body, nil
}

func (db *DB) GetAllLocalComments() ([]LocalComment, error) {
	rows, err := db.conn.Query("SELECT id, owner, repo, number, filename, position, body, reply_to_id FROM LocalComment")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []LocalComment
	for rows.Next() {
		var comment LocalComment
		if err := rows.Scan(&comment.ID, &comment.Owner, &comment.Repo, &comment.Number, &comment.Filename, &comment.Position, &comment.Body, &comment.ReplyToID); err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

func (db *DB) GetLocalCommentsForPR(owner, repo string, number int) ([]LocalComment, error) {
	rows, err := db.conn.Query("SELECT id, owner, repo, number, filename, position, body, reply_to_id FROM LocalComment WHERE owner = ? AND repo = ? AND number = ?", owner, repo, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var comments []LocalComment
	for rows.Next() {
		var comment LocalComment
		if err := rows.Scan(&comment.ID, &comment.Owner, &comment.Repo, &comment.Number, &comment.Filename, &comment.Position, &comment.Body, &comment.ReplyToID); err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

func (db *DB) DeleteAllLocalComments() error {
	_, err := db.conn.Exec("DELETE FROM LocalComment")
	return err
}

func (db *DB) DeleteLocalCommentsForPR(owner, repo string, number int) error {
	_, err := db.conn.Exec("DELETE FROM LocalComment WHERE owner = ? AND repo = ? AND number = ?", owner, repo, number)
	return err
}

func (db *DB) UpdateLocalComment(id int64, body string) error {
	_, err := db.conn.Exec("UPDATE LocalComment SET body = ? WHERE id = ?", body, id)
	return err
}

func (db *DB) DeleteLocalComment(id int64) error {
	_, err := db.conn.Exec("DELETE FROM LocalComment WHERE id = ?", id)
	return err
}

func (db *DB) GetPullRequest(prNumber int, repo string) (string, string, error) {
	var body string
	var sha string
	// A PR accumulates one row per head SHA it has ever had (the UNIQUE key
	// includes latest_sha). Order by rowid DESC so we return the most recently
	// written row — i.e. the current head SHA — rather than an arbitrary (often
	// stale) one. Without this, a bare LIMIT 1 could hand back an old SHA's diff
	// or an empty placeholder row.
	err := db.conn.QueryRow(
		"SELECT body, latest_sha FROM PullRequests WHERE pr_number = ? AND repo = ? ORDER BY rowid DESC LIMIT 1",
		prNumber, repo,
	).Scan(&body, &sha)

	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return body, sha, nil
}

// GetPullRequestSHAs returns both the head and base SHAs for a cached PR.
func (db *DB) GetPullRequestSHAs(prNumber int, repo string) (headSHA, baseSHA string, err error) {
	err = db.conn.QueryRow(
		"SELECT latest_sha, COALESCE(base_sha, '') FROM PullRequests WHERE pr_number = ? AND repo = ? ORDER BY rowid DESC LIMIT 1",
		prNumber, repo,
	).Scan(&headSHA, &baseSHA)

	if err == sql.ErrNoRows {
		return "", "", nil
	}
	return
}

func (db *DB) UpsertPullRequest(prNumber int, repo, latestSha, baseSha, body string) error {
	// Never overwrite a cached diff with an empty body. The workflow writes a
	// SHA-only placeholder row (empty body) for PRs whose section doesn't need
	// the diff, purely so CI/SHA lookups work. Clobbering the real diff with ""
	// here would silently poison the cache and force a miss on the next review
	// open, so keep the existing body when the incoming one is empty.
	_, err := db.conn.Exec(
		`INSERT INTO PullRequests (pr_number, repo, latest_sha, base_sha, body)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(pr_number, repo, latest_sha) DO UPDATE SET
			body = CASE WHEN excluded.body != '' THEN excluded.body ELSE body END,
			base_sha = excluded.base_sha`,
		prNumber, repo, latestSha, baseSha, body,
	)
	return err
}

func (db *DB) GetPRComments(prNumber int, repo string) (string, error) {
	var commentsJSON string
	err := db.conn.QueryRow(
		"SELECT comments_json FROM PRComments WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	).Scan(&commentsJSON)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return commentsJSON, nil
}

func (db *DB) UpsertPRComments(prNumber int, repo, commentsJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO PRComments (pr_number, repo, comments_json)
		 VALUES (?, ?, ?)
		 ON CONFLICT(pr_number, repo) DO UPDATE SET
			comments_json = excluded.comments_json`,
		prNumber, repo, commentsJSON,
	)
	return err
}

func (db *DB) DeletePRComments(prNumber int, repo string) error {
	_, err := db.conn.Exec(
		"DELETE FROM PRComments WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	)
	return err
}

func (db *DB) DeletePullRequests(prNumber int, repo string) error {
	_, err := db.conn.Exec(
		"DELETE FROM PullRequests WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	)
	return err
}

func (db *DB) GetRequestedReviewers(prNumber int, repo string) (string, error) {
	var reviewersJSON string
	err := db.conn.QueryRow(
		"SELECT reviewers_json FROM RequestedReviewers WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	).Scan(&reviewersJSON)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return reviewersJSON, nil
}

func (db *DB) UpsertRequestedReviewers(prNumber int, repo, reviewersJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO RequestedReviewers (pr_number, repo, reviewers_json)
		 VALUES (?, ?, ?)
		 ON CONFLICT(pr_number, repo) DO UPDATE SET
			reviewers_json = excluded.reviewers_json`,
		prNumber, repo, reviewersJSON,
	)
	return err
}

func (db *DB) GetCIStatus(prNumber int, repo string, sha string) (string, error) {
	var statusJSON string
	err := db.conn.QueryRow(
		"SELECT status_json FROM CIStatus WHERE pr_number = ? AND repo = ? AND sha = ?",
		prNumber, repo, sha,
	).Scan(&statusJSON)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return statusJSON, nil
}

func (db *DB) UpsertCIStatus(prNumber int, repo, sha, statusJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO CIStatus (pr_number, repo, sha, status_json)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(pr_number, repo, sha) DO UPDATE SET
			status_json = excluded.status_json`,
		prNumber, repo, sha, statusJSON,
	)
	return err
}

func (db *DB) GetPRMetadataCache(owner string, repo string, prNumber int) (string, error) {
	var metadataJSON string
	err := db.conn.QueryRow(
		"SELECT metadata_json FROM PRMetadataCache WHERE owner = ? AND repo = ? AND pr_number = ?",
		owner, repo, prNumber,
	).Scan(&metadataJSON)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return metadataJSON, nil
}

func (db *DB) UpsertPRMetadataCache(owner string, repo string, prNumber int, metadataJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO PRMetadataCache (owner, repo, pr_number, metadata_json, cached_at)
		 VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(pr_number, repo, owner) DO UPDATE SET
			metadata_json = excluded.metadata_json,
			cached_at = CURRENT_TIMESTAMP`,
		owner, repo, prNumber, metadataJSON,
	)
	return err
}

func (db *DB) GetReleaseStatus(owner string, repo string, prNumber int) (string, error) {
	var status string
	err := db.conn.QueryRow(
		"SELECT release_status FROM PRMetadataCache WHERE owner = ? AND repo = ? AND pr_number = ?",
		owner, repo, prNumber,
	).Scan(&status)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return status, nil
}

func (db *DB) UpsertReleaseStatus(owner string, repo string, prNumber int, releaseStatus string) error {
	_, err := db.conn.Exec(
		`UPDATE PRMetadataCache SET release_status = ? WHERE owner = ? AND repo = ? AND pr_number = ?`,
		releaseStatus, owner, repo, prNumber,
	)
	return err
}

func (db *DB) DeletePRMetadataCache(owner string, repo string, prNumber int) error {
	_, err := db.conn.Exec(
		"DELETE FROM PRMetadataCache WHERE owner = ? AND repo = ? AND pr_number = ?",
		owner, repo, prNumber,
	)
	return err
}

// GetItemWorkflowInfo returns the time the item was first added by the workflow and its section name.
// The identifier should be in the format "{repo}-{prNumber}" (e.g. "chaturbate-1234").
func (db *DB) GetItemWorkflowInfo(identifier string) (time.Time, string, error) {
	var createdAtStr string
	var sectionName string
	err := db.conn.QueryRow(
		`SELECT i.created_at, s.section_name
		 FROM items i
		 JOIN sections s ON i.section_id = s.id
		 WHERE i.identifier = ?
		 LIMIT 1`,
		identifier,
	).Scan(&createdAtStr, &sectionName)
	if err == sql.ErrNoRows {
		return time.Time{}, "", nil
	}
	if err != nil {
		return time.Time{}, "", err
	}
	// SQLite stores CURRENT_TIMESTAMP as "YYYY-MM-DD HH:MM:SS"
	t, err := time.Parse("2006-01-02 15:04:05", createdAtStr)
	if err != nil {
		// Try RFC3339 fallback
		t, err = time.Parse(time.RFC3339, createdAtStr)
		if err != nil {
			return time.Time{}, sectionName, nil
		}
	}
	return t, sectionName, nil
}

func (item *Item) GetDetails() ([]string, error) {
	var details []string
	if err := json.Unmarshal([]byte(item.DetailsJSON), &details); err != nil {
		return nil, err
	}
	return details, nil
}

// GetWorkflows returns the list of workflow names that maintain this item.
func (item *Item) GetWorkflows() []string {
	return decodeWorkflowList(item.Workflows)
}

func (item *Item) GetTags() ([]string, error) {
	if item.Tags == "" {
		return []string{}, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(item.Tags), &tags); err != nil {
		// Fallback: treat as comma-separated
		return []string{}, nil
	}
	return tags, nil
}

// Transaction support
func (db *DB) Begin() (*sql.Tx, error) {
	return db.conn.Begin()
}

func (db *DB) GetPRReviews(prNumber int, repo string) (string, error) {
	var reviewsJSON string
	err := db.conn.QueryRow(
		"SELECT reviews_json FROM PRReviews WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	).Scan(&reviewsJSON)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return reviewsJSON, nil
}

func (db *DB) UpsertPRReviews(prNumber int, repo, reviewsJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO PRReviews (pr_number, repo, reviews_json)
		 VALUES (?, ?, ?)
		 ON CONFLICT(pr_number, repo) DO UPDATE SET
			reviews_json = excluded.reviews_json`,
		prNumber, repo, reviewsJSON,
	)
	return err
}

func (db *DB) DeletePRReviews(prNumber int, repo string) error {
	_, err := db.conn.Exec(
		"DELETE FROM PRReviews WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	)
	return err
}

func (db *DB) GetPRCommits(prNumber int, repo string) (string, error) {
	var commitsJSON string
	err := db.conn.QueryRow(
		"SELECT commits_json FROM PRCommits WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	).Scan(&commitsJSON)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return commitsJSON, nil
}

func (db *DB) UpsertPRCommits(prNumber int, repo, commitsJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO PRCommits (pr_number, repo, commits_json)
		 VALUES (?, ?, ?)
		 ON CONFLICT(pr_number, repo) DO UPDATE SET
			commits_json = excluded.commits_json`,
		prNumber, repo, commitsJSON,
	)
	return err
}

func (db *DB) DeletePRCommits(prNumber int, repo string) error {
	_, err := db.conn.Exec(
		"DELETE FROM PRCommits WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	)
	return err
}

// Review-thread resolution state comes from GitHub's GraphQL API (the REST API
// does not expose it), so it gets its own cache alongside the other PR caches.

func (db *DB) GetPRReviewThreads(prNumber int, repo string) (string, error) {
	var threadsJSON string
	err := db.conn.QueryRow(
		"SELECT threads_json FROM PRReviewThreads WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	).Scan(&threadsJSON)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return threadsJSON, nil
}

func (db *DB) UpsertPRReviewThreads(prNumber int, repo, threadsJSON string) error {
	_, err := db.conn.Exec(
		`INSERT INTO PRReviewThreads (pr_number, repo, threads_json)
		 VALUES (?, ?, ?)
		 ON CONFLICT(pr_number, repo) DO UPDATE SET
			threads_json = excluded.threads_json`,
		prNumber, repo, threadsJSON,
	)
	return err
}

func (db *DB) DeletePRReviewThreads(prNumber int, repo string) error {
	_, err := db.conn.Exec(
		"DELETE FROM PRReviewThreads WHERE pr_number = ? AND repo = ?",
		prNumber, repo,
	)
	return err
}

func (db *DB) Exec(query string, args ...interface{}) (sql.Result, error) {
	return db.conn.Exec(query, args...)
}

func (db *DB) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return db.conn.Query(query, args...)
}

func (db *DB) QueryRow(query string, args ...interface{}) *sql.Row {
	return db.conn.QueryRow(query, args...)
}

// LogAPICallStats persists per-type GitHub API call counts and post-cycle rate limit
// status for a completed workflow cycle.
func (db *DB) LogAPICallStats(prList, prSpecific, comments, issueComments, ciStatus, diff, reviews, combinedStatus, checkRuns, commits int64, rateLimitRemaining, rateLimitLimit int, rateLimitResetAt string) error {
	total := prList + prSpecific + comments + issueComments + ciStatus + diff + reviews + combinedStatus + checkRuns + commits
	_, err := db.conn.Exec(
		`INSERT INTO APICallStats
			(pr_list, pr_specific, comments, issue_comments, ci_status, diff, reviews, combined_status, check_runs, commits, total, rate_limit_remaining, rate_limit_limit, rate_limit_reset_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		prList, prSpecific, comments, issueComments, ciStatus, diff, reviews, combinedStatus, checkRuns, commits, total,
		rateLimitRemaining, rateLimitLimit, rateLimitResetAt,
	)
	return err
}

// LogWorkflowCycle records a completed workflow cycle in the DB.
func (db *DB) LogWorkflowCycle() error {
	_, err := db.conn.Exec("INSERT INTO WorkflowCycleLog (completed_at) VALUES (CURRENT_TIMESTAMP)")
	return err
}

// GetLastWorkflowCycleTime returns the time of the most recently completed cycle,
// or the zero time if no cycle has been logged.
func (db *DB) GetLastWorkflowCycleTime() (time.Time, error) {
	var completedAtStr string
	err := db.conn.QueryRow(
		"SELECT completed_at FROM WorkflowCycleLog ORDER BY id DESC LIMIT 1",
	).Scan(&completedAtStr)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	t, parseErr := time.Parse("2006-01-02 15:04:05", completedAtStr)
	if parseErr != nil {
		t, parseErr = time.Parse(time.RFC3339, completedAtStr)
		if parseErr != nil {
			return time.Time{}, parseErr
		}
	}
	return t, nil
}
