package database

import (
	"database/sql"
	"encoding/json"
	"time"
)

// User is one person a hosted server keeps a dashboard for: their sections,
// items, drafts and feedback carry their login as user_login, and their
// workflows live in user_workflows. Local mode has no users; it is the server's
// own identity, the login "".
//
// SectionPriority, SectionSorting and Repos are the user's counterparts of the
// config file's keys of the same names, applied to their own workflows.
type User struct {
	Login           string
	TokenHash       string // hex sha256 of the bearer token; the token itself is never stored
	SectionPriority map[string]int
	SectionSorting  map[string]string
	Repos           []string // fallback Repos for the user's workflows, like the config's root Repos
	IsAdmin         bool
	CreatedAt       time.Time
	LastSeen        time.Time
}

// UserWorkflow is one of a user's workflows. The database cannot import config
// (config holds the DB), so the workflow is the JSON of a config.RawWorkflow,
// opaque here; Name is copied out of it so a user cannot hold two workflows of
// the same name. Position is the workflow's place in the user's list.
type UserWorkflow struct {
	Position int
	Name     string
	JSON     string
}

const userColumns = "login, token_hash, section_priority, section_sorting, repos, is_admin, created_at, last_seen"

// CreateUser stores a new user. CreatedAt and LastSeen default to now. A login
// that is already registered, in any case, fails on the primary key, as does a
// token hash another user holds.
func (db *DB) CreateUser(u User) error {
	priority, sorting, repos, err := encodeUserSettings(u)
	if err != nil {
		return err
	}
	now := time.Now()
	if u.CreatedAt.IsZero() {
		u.CreatedAt = now
	}
	if u.LastSeen.IsZero() {
		u.LastSeen = now
	}
	_, err = db.conn.Exec(
		`INSERT INTO users (`+userColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		u.Login, u.TokenHash, priority, sorting, repos, u.IsAdmin,
		formatSQLiteTime(u.CreatedAt), formatSQLiteTime(u.LastSeen),
	)
	return err
}

// UpdateUserSettings replaces a user's SectionPriority, SectionSorting, Repos
// and IsAdmin with u's. The login and token never change, and CreatedAt and
// LastSeen are left alone. Returns ErrNotFound when no user has u.Login.
func (db *DB) UpdateUserSettings(u User) error {
	priority, sorting, repos, err := encodeUserSettings(u)
	if err != nil {
		return err
	}
	res, err := db.conn.Exec(
		`UPDATE users SET section_priority = ?, section_sorting = ?, repos = ?, is_admin = ?
		 WHERE login = ?`,
		priority, sorting, repos, u.IsAdmin, u.Login,
	)
	return requireOneRow(res, err)
}

// GetUser returns the user with that login, matched case-insensitively like a
// GitHub login, or ErrNotFound. The returned Login is the case the user
// registered with, which is the one their rows carry.
func (db *DB) GetUser(login string) (*User, error) {
	return db.queryUser("SELECT "+userColumns+" FROM users WHERE login = ?", login)
}

// GetUserByTokenHash returns the user whose bearer token hashes to hash, or
// ErrNotFound.
func (db *DB) GetUserByTokenHash(hash string) (*User, error) {
	return db.queryUser("SELECT "+userColumns+" FROM users WHERE token_hash = ?", hash)
}

// ListUsers returns every user, ordered by login.
func (db *DB) ListUsers() ([]User, error) {
	rows, err := db.conn.Query("SELECT " + userColumns + " FROM users ORDER BY login")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// TouchUser records that the user was active at now. last_seen is what decides
// whether the workflow cycle runs a user's workflows and when retention sweeps
// them (DeleteUsersIdleSince). Returns ErrNotFound when no user has that login,
// e.g. one deleted while still connected.
func (db *DB) TouchUser(login string, now time.Time) error {
	res, err := db.conn.Exec("UPDATE users SET last_seen = ? WHERE login = ?", formatSQLiteTime(now), login)
	return requireOneRow(res, err)
}

// DeleteUser removes a user along with their workflows, drafts and feedback.
// Their sections and items are deliberately left: once the user's workflows
// are gone, nothing backs the items' ownership any more, so the workflow
// cycle's release and prune pass removes them the way it removes a workflow
// dropped from the config. Returns ErrNotFound when no user has that login.
func (db *DB) DeleteUser(login string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var canonical string
	err = tx.QueryRow("SELECT login FROM users WHERE login = ?", login).Scan(&canonical)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := deleteUserTx(tx, canonical); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUsersIdleSince deletes, as DeleteUser does, every user last seen
// before cutoff, and returns their logins. A user last seen exactly at cutoff
// (to the second, the precision last_seen is stored at) is kept.
func (db *DB) DeleteUsersIdleSince(cutoff time.Time) ([]string, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Collected before deleting, so no cursor is open under the deletes.
	collect := func() ([]string, error) {
		rows, err := tx.Query("SELECT login FROM users WHERE last_seen < ? ORDER BY login", formatSQLiteTime(cutoff))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var logins []string
		for rows.Next() {
			var login string
			if err := rows.Scan(&login); err != nil {
				return nil, err
			}
			logins = append(logins, login)
		}
		return logins, rows.Err()
	}
	logins, err := collect()
	if err != nil {
		return nil, err
	}
	for _, login := range logins {
		if err := deleteUserTx(tx, login); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return logins, nil
}

// deleteUserTx deletes a user by the login exactly as stored. Their
// user_workflows rows go with them through the foreign key's cascade; their
// drafts and feedback have no foreign key (local mode's rows have no user to
// reference), so they are deleted here.
func deleteUserTx(tx *sql.Tx, login string) error {
	for _, stmt := range []string{
		"DELETE FROM LocalComment WHERE user_login = ?",
		"DELETE FROM Feedback WHERE user_login = ?",
		"DELETE FROM users WHERE login = ?",
	} {
		if _, err := tx.Exec(stmt, login); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceUserWorkflows replaces a user's whole workflow list with workflows,
// in one transaction, so a failed write leaves the old list in place. Two
// workflows of the same name fail it. Returns ErrNotFound when no user has
// that login.
func (db *DB) ReplaceUserWorkflows(login string, workflows []UserWorkflow) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Rows are stored under the login as the user registered it, whatever
	// case the caller used, so GetAllUserWorkflows keys them consistently.
	var canonical string
	err = tx.QueryRow("SELECT login FROM users WHERE login = ?", login).Scan(&canonical)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec("DELETE FROM user_workflows WHERE user_login = ?", canonical); err != nil {
		return err
	}
	for _, wf := range workflows {
		if _, err := tx.Exec(
			"INSERT INTO user_workflows (user_login, position, name, workflow_json) VALUES (?, ?, ?, ?)",
			canonical, wf.Position, wf.Name, wf.JSON,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetUserWorkflows returns a user's workflows in their order. A user with no
// workflows, or no such user, yields none.
func (db *DB) GetUserWorkflows(login string) ([]UserWorkflow, error) {
	var workflows []UserWorkflow
	err := db.eachUserWorkflow(func(_ string, wf UserWorkflow) {
		workflows = append(workflows, wf)
	}, "SELECT user_login, position, name, workflow_json FROM user_workflows WHERE user_login = ? ORDER BY position, name", login)
	return workflows, err
}

// GetAllUserWorkflows returns every user's workflows, keyed by login, each
// list in the user's order. Users without workflows are absent.
func (db *DB) GetAllUserWorkflows() (map[string][]UserWorkflow, error) {
	byUser := make(map[string][]UserWorkflow)
	err := db.eachUserWorkflow(func(login string, wf UserWorkflow) {
		byUser[login] = append(byUser[login], wf)
	}, "SELECT user_login, position, name, workflow_json FROM user_workflows ORDER BY user_login, position, name")
	if err != nil {
		return nil, err
	}
	return byUser, nil
}

// eachUserWorkflow runs a query selecting user_workflows' columns, in table
// order, and hands each row to fn.
func (db *DB) eachUserWorkflow(fn func(login string, wf UserWorkflow), query string, args ...any) error {
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var login string
		var wf UserWorkflow
		if err := rows.Scan(&login, &wf.Position, &wf.Name, &wf.JSON); err != nil {
			return err
		}
		fn(login, wf)
	}
	return rows.Err()
}

func (db *DB) queryUser(query string, args ...any) (*User, error) {
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	return scanUser(rows)
}

func scanUser(rows *sql.Rows) (*User, error) {
	var u User
	var priority, sorting, repos, createdAt, lastSeen string
	if err := rows.Scan(&u.Login, &u.TokenHash, &priority, &sorting, &repos, &u.IsAdmin, &createdAt, &lastSeen); err != nil {
		return nil, err
	}
	u.SectionPriority = map[string]int{}
	u.SectionSorting = map[string]string{}
	u.Repos = []string{}
	for _, field := range []struct {
		raw  string
		dest any
	}{
		{priority, &u.SectionPriority},
		{sorting, &u.SectionSorting},
		{repos, &u.Repos},
	} {
		if err := json.Unmarshal([]byte(field.raw), field.dest); err != nil {
			return nil, err
		}
	}
	u.CreatedAt = parseSQLiteTime(createdAt)
	u.LastSeen = parseSQLiteTime(lastSeen)
	return &u, nil
}

// encodeUserSettings renders a user's settings as the JSON the users table
// stores, an absent one as empty rather than null.
func encodeUserSettings(u User) (priority, sorting, repos string, err error) {
	encode := func(v any, empty string) (string, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		if s := string(b); s != "null" {
			return s, nil
		}
		return empty, nil
	}
	if priority, err = encode(u.SectionPriority, "{}"); err != nil {
		return "", "", "", err
	}
	if sorting, err = encode(u.SectionSorting, "{}"); err != nil {
		return "", "", "", err
	}
	if repos, err = encode(u.Repos, "[]"); err != nil {
		return "", "", "", err
	}
	return priority, sorting, repos, nil
}

// formatSQLiteTime renders t the way CURRENT_TIMESTAMP does, in UTC, so a
// stored timestamp compares correctly as text against a cutoff written the
// same way.
func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format(sqliteTimeLayout)
}
