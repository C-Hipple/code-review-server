package database

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

type Section struct {
	ID          int64
	SectionName string
	IndentLevel int
}

type Item struct {
	ID          int64
	SectionID   int64
	Identifier  string
	Status      string
	Title       string
	DetailsJSON string // JSON array of detail lines
	Tags        string // Comma-separated tags
	Archived    bool
}

type LocalComment struct {
	ID        int64
	Owner     string    // GitHub owner/org
	Repo      string    // GitHub repository name
	Number    int       // PR number
	Filename  string    // going to be the rel file like src/main.rs
	Position  int64
	Body      *string
	ReplyToID *int64    // ID of the comment being replied to, or nil if top-level
}

func NewDB(dbPath string) (*DB, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	conn, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=1")
	if err != nil {
		return nil, err
	}

	db := &DB{conn: conn}
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
		indent_level INTEGER NOT NULL DEFAULT 2,
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
		archived INTEGER DEFAULT 0,
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
			body TEXT
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

	CREATE TABLE IF NOT EXISTS CIStatus (
		pr_number INTEGER NOT NULL,
		repo TEXT NOT NULL,
		sha TEXT NOT NULL,
		status_json TEXT NOT NULL,
		UNIQUE(pr_number, repo, sha)
	);

	CREATE INDEX IF NOT EXISTS idx_items_section ON items(section_id);
	CREATE INDEX IF NOT EXISTS idx_items_identifier ON items(identifier);
	CREATE INDEX IF NOT EXISTS idx_pullrequests_lookup ON PullRequests(pr_number, repo, latest_sha);
	CREATE INDEX IF NOT EXISTS idx_prcomments_lookup ON PRComments(pr_number, repo);
	CREATE INDEX IF NOT EXISTS idx_localcomments_pr ON LocalComment(owner, repo, number);
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

	return nil
}

func (db *DB) GetOrCreateSection(sectionName string, indentLevel int) (*Section, error) {
	var section Section
	err := db.conn.QueryRow(
		"SELECT id, section_name, indent_level FROM sections WHERE section_name = ?",
		sectionName,
	).Scan(&section.ID, &section.SectionName, &section.IndentLevel)

	if err == sql.ErrNoRows {
		result, err := db.conn.Exec(
			"INSERT INTO sections (section_name, indent_level) VALUES (?, ?)",
			sectionName, indentLevel,
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
			IndentLevel: indentLevel,
		}
		return &section, nil
	} else if err != nil {
		return nil, err
	}

	return &section, nil
}

func (db *DB) GetSection(sectionName string) (*Section, error) {
	var section Section
	err := db.conn.QueryRow(
		"SELECT id, section_name, indent_level FROM sections WHERE section_name = ?",
		sectionName,
	).Scan(&section.ID, &section.SectionName, &section.IndentLevel)

	if err != nil {
		return nil, err
	}
	return &section, nil
}

func (db *DB) GetAllSections() ([]*Section, error) {
	rows, err := db.conn.Query("SELECT id, section_name, indent_level FROM sections ORDER BY section_name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sections []*Section
	for rows.Next() {
		var section Section
		if err := rows.Scan(&section.ID, &section.SectionName, &section.IndentLevel); err != nil {
			return nil, err
		}
		sections = append(sections, &section)
	}
	return sections, rows.Err()
}

func (db *DB) UpsertItem(sectionID int64, identifier, status, title string, details []string, tags []string, archived bool) (*Item, error) {
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

	archivedInt := 0
	if archived {
		archivedInt = 1
	}

	result, err := db.conn.Exec(
		`INSERT INTO items (section_id, identifier, status, title, details_json, tags, archived)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(section_id, identifier) DO UPDATE SET
			status = excluded.status,
			title = excluded.title,
			details_json = excluded.details_json,
			tags = excluded.tags,
			archived = excluded.archived`,
		sectionID, identifier, status, title, string(detailsJSON), tagsStr, archivedInt,
	)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		// If update happened, get the existing ID
		var existingID int64
		err := db.conn.QueryRow(
			"SELECT id FROM items WHERE section_id = ? AND identifier = ?",
			sectionID, identifier,
		).Scan(&existingID)
		if err != nil {
			return nil, err
		}
		id = existingID
	}

	item := &Item{
		ID:          id,
		SectionID:   sectionID,
		Identifier:  identifier,
		Status:      status,
		Title:       title,
		DetailsJSON: string(detailsJSON),
		Tags:        tagsStr,
		Archived:    archived,
	}

	return item, nil
}

func (db *DB) GetItem(sectionID int64, identifier string) (*Item, error) {
	var item Item
	err := db.conn.QueryRow(
		"SELECT id, section_id, identifier, status, title, details_json, tags, archived FROM items WHERE section_id = ? AND identifier = ?",
		sectionID, identifier,
	).Scan(&item.ID, &item.SectionID, &item.Identifier, &item.Status, &item.Title, &item.DetailsJSON, &item.Tags, &item.Archived)

	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (db *DB) GetItemsBySection(sectionID int64) ([]*Item, error) {
	rows, err := db.conn.Query(
		"SELECT id, section_id, identifier, status, title, details_json, tags, archived FROM items WHERE section_id = ? ORDER BY id",
		sectionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []*Item
	for rows.Next() {
		var item Item
		var archivedInt int
		if err := rows.Scan(&item.ID, &item.SectionID, &item.Identifier, &item.Status, &item.Title, &item.DetailsJSON, &item.Tags, &archivedInt); err != nil {
			return nil, err
		}
		item.Archived = archivedInt == 1
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

func (db *DB) InsertLocalComment(owner, repo string, number int, filename string, position int64, body *string, replyToID *int64) LocalComment {
	stmt, err := db.conn.Prepare("INSERT INTO LocalComment (owner, repo, number, filename, position, body, reply_to_id) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		slog.Error(err.Error())
	}
	defer stmt.Close()

	// Execute the insertion
	res, err := stmt.Exec(owner, repo, number, filename, position, body, replyToID)
	if err != nil {
		slog.Error(err.Error())
	}

	// Get the last inserted ID
	id, err := res.LastInsertId()
	if err != nil {
		slog.Error(err.Error())
	}
	return LocalComment{
		ID: id, Owner: owner, Repo: repo, Number: number, Filename: filename, Position: position, Body: body, ReplyToID: replyToID,
	}
}

func (db *DB) InsertFeedback(owner, repo string, number int, body *string) {
	stmt, err := db.conn.Prepare(
		`INSERT INTO Feedback (owner, repo, number, body) VALUES (?, ?, ?, ?)
		 ON CONFLICT(pr_number, repo) DO UPDATE SET
			body = excluded.body`,
	)
	if err != nil {
		slog.Error(err.Error())
	}
	defer stmt.Close()

	_, err = stmt.Exec(owner, repo, number, body)
	if err != nil {
		slog.Error(err.Error())
	}
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

func (db *DB) GetPullRequest(prNumber int, repo string) (string, error) {
	var body string
	err := db.conn.QueryRow(
		"SELECT body FROM PullRequests WHERE pr_number = ? AND repo = ? LIMIT 1",
		prNumber, repo,
	).Scan(&body)

	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return body, nil
}

func (db *DB) UpsertPullRequest(prNumber int, repo, latestSha, body string) error {
	_, err := db.conn.Exec(
		`INSERT INTO PullRequests (pr_number, repo, latest_sha, body)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(pr_number, repo, latest_sha) DO UPDATE SET
			body = excluded.body`,
		prNumber, repo, latestSha, body,
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

func (item *Item) GetDetails() ([]string, error) {
	var details []string
	if err := json.Unmarshal([]byte(item.DetailsJSON), &details); err != nil {
		return nil, err
	}
	return details, nil
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

func (db *DB) Exec(query string, args ...interface{}) (sql.Result, error) {
	return db.conn.Exec(query, args...)
}

func (db *DB) Query(query string, args ...interface{}) (*sql.Rows, error) {
	return db.conn.Query(query, args...)
}

func (db *DB) QueryRow(query string, args ...interface{}) *sql.Row {
	return db.conn.QueryRow(query, args...)
}
