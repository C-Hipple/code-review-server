package database

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	conn *sql.DB
}

type Section struct {
	ID          int64
	Filename    string
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

	return db, nil
}

func (db *DB) Close() error {
	return db.conn.Close()
}

func (db *DB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sections (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		filename TEXT NOT NULL,
		section_name TEXT NOT NULL,
		indent_level INTEGER NOT NULL DEFAULT 2,
		UNIQUE(filename, section_name)
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

	CREATE INDEX IF NOT EXISTS idx_items_section ON items(section_id);
	CREATE INDEX IF NOT EXISTS idx_items_identifier ON items(identifier);
	`

	_, err := db.conn.Exec(schema)
	return err
}

func (db *DB) GetOrCreateSection(filename, sectionName string, indentLevel int) (*Section, error) {
	var section Section
	err := db.conn.QueryRow(
		"SELECT id, filename, section_name, indent_level FROM sections WHERE filename = ? AND section_name = ?",
		filename, sectionName,
	).Scan(&section.ID, &section.Filename, &section.SectionName, &section.IndentLevel)

	if err == sql.ErrNoRows {
		result, err := db.conn.Exec(
			"INSERT INTO sections (filename, section_name, indent_level) VALUES (?, ?, ?)",
			filename, sectionName, indentLevel,
		)
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return nil, err
		}
		section = Section{
			ID:          id,
			Filename:    filename,
			SectionName: sectionName,
			IndentLevel: indentLevel,
		}
		return &section, nil
	} else if err != nil {
		return nil, err
	}

	return &section, nil
}

func (db *DB) GetSection(filename, sectionName string) (*Section, error) {
	var section Section
	err := db.conn.QueryRow(
		"SELECT id, filename, section_name, indent_level FROM sections WHERE filename = ? AND section_name = ?",
		filename, sectionName,
	).Scan(&section.ID, &section.Filename, &section.SectionName, &section.IndentLevel)

	if err != nil {
		return nil, err
	}
	return &section, nil
}

func (db *DB) GetAllSections() ([]*Section, error) {
	rows, err := db.conn.Query("SELECT id, filename, section_name, indent_level FROM sections ORDER BY filename, section_name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sections []*Section
	for rows.Next() {
		var section Section
		if err := rows.Scan(&section.ID, &section.Filename, &section.SectionName, &section.IndentLevel); err != nil {
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

