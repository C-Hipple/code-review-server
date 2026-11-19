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
	return db.CreateUserWithWorkflows(u, nil)
}

// CreateUserWithWorkflows stores a new user together with their workflows, in
// one transaction: registering is creating both, and a failure partway — a
// login registered meanwhile, two workflows of one name — must not leave a user
// behind who exists but has no dashboard, or whose token then fails to match a
// second attempt. It fails as CreateUser does.
//
// Drafts and feedback still under the login are deleted with it: they belong
// to someone who held it before. Deleting a user deletes theirs, but a draft
// admitted just before the delete can be written just after it — the method
// policy checks the user when it reads a request, and the handler runs later.
func (db *DB) CreateUserWithWorkflows(u User, workflows []UserWorkflow) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := insertUserTx(tx, u); err != nil {
		return err
	}
	for _, stmt := range []string{
		"DELETE FROM LocalComment WHERE user_login = ? COLLATE NOCASE",
		"DELETE FROM Feedback WHERE user_login = ? COLLATE NOCASE",
	} {
		if _, err := tx.Exec(stmt, u.Login); err != nil {
			return err
		}
	}
	if err := insertUserWorkflowsTx(tx, u.Login, workflows); err != nil {
		return err
	}
	return tx.Commit()
}

func insertUserTx(tx *sql.Tx, u User) error {
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
	_, err = tx.Exec(
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

// UpdateUserWithWorkflows does what UpdateUserSettings and ReplaceUserWorkflows
// do, in one transaction, so a user's settings and the workflows validated
// against them — a workflow naming no Repos falls back to the user's — are
// never stored half from one update and half from another. Returns ErrNotFound
// when no user has u.Login.
func (db *DB) UpdateUserWithWorkflows(u User, workflows []UserWorkflow) error {
	priority, sorting, repos, err := encodeUserSettings(u)
	if err != nil {
		return err
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	canonical, err := canonicalLoginTx(tx, u.Login)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE users SET section_priority = ?, section_sorting = ?, repos = ?, is_admin = ?
		 WHERE login = ?`,
		priority, sorting, repos, u.IsAdmin, canonical,
	); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM user_workflows WHERE user_login = ?", canonical); err != nil {
		return err
	}
	if err := insertUserWorkflowsTx(tx, canonical, workflows); err != nil {
		return err
	}
	return tx.Commit()
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

// DeletedUser is a user just deleted, and the sealed GitHub credential their
// row held ("" if they had not linked one), which the server revokes on
// GitHub: deleting the row only forgets the token, and GitHub would go on
// honoring it.
type DeletedUser struct {
	Login            string
	GitHubCredential string
}

// DeleteUser removes a user along with their workflows, drafts and feedback,
// and revokes their token (see DeleteUserWithCredential).
// Their sections and items are deliberately left: once the user's workflows
// are gone, nothing backs the items' ownership any more, so the workflow
// cycle's release and prune pass removes them the way it removes a workflow
// dropped from the config. Returns ErrNotFound when no user has that login.
func (db *DB) DeleteUser(login string) error {
	_, err := db.DeleteUserWithCredential(login)
	return err
}

// DeleteUserWithCredential deletes a user as DeleteUser does and returns the
// GitHub credential they held, read in the same transaction so a link stored
// a moment before is not deleted unseen. It also revokes their bearer token
// (IsTokenRevoked): this is an administrator's delete, and the client of a
// user an administrator removed must not register the login again with it.
func (db *DB) DeleteUserWithCredential(login string) (DeletedUser, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return DeletedUser{}, err
	}
	defer tx.Rollback()

	canonical, err := canonicalLoginTx(tx, login)
	if err != nil {
		return DeletedUser{}, err
	}
	if _, err := tx.Exec(
		"INSERT OR IGNORE INTO revoked_tokens (token_hash, login) SELECT token_hash, login FROM users WHERE login = ?",
		canonical,
	); err != nil {
		return DeletedUser{}, err
	}
	deleted, err := deleteUserTx(tx, canonical)
	if err != nil {
		return DeletedUser{}, err
	}
	return deleted, tx.Commit()
}

// IsTokenRevoked reports whether hash is the token hash of a user an
// administrator deleted.
func (db *DB) IsTokenRevoked(hash string) (bool, error) {
	var n int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM revoked_tokens WHERE token_hash = ?", hash).Scan(&n)
	return n > 0, err
}

// DeleteUsersIdleSince deletes, as DeleteUser does but leaving their tokens
// unrevoked, every user last seen before cutoff, and returns their logins. A user last seen exactly at cutoff
// (to the second, the precision last_seen is stored at) is kept.
func (db *DB) DeleteUsersIdleSince(cutoff time.Time) ([]string, error) {
	deleted, err := db.DeleteUsersIdleSinceWithCredentials(cutoff)
	if err != nil {
		return nil, err
	}
	logins := make([]string, 0, len(deleted))
	for _, d := range deleted {
		logins = append(logins, d.Login)
	}
	return logins, nil
}

// DeleteUsersIdleSinceWithCredentials deletes the users DeleteUsersIdleSince
// does and returns each with the GitHub credential they held, in login order.
func (db *DB) DeleteUsersIdleSinceWithCredentials(cutoff time.Time) ([]DeletedUser, error) {
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
	deleted := make([]DeletedUser, 0, len(logins))
	for _, login := range logins {
		d, err := deleteUserTx(tx, login)
		if err != nil {
			return nil, err
		}
		deleted = append(deleted, d)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return deleted, nil
}

// deleteUserTx deletes a user by the login exactly as stored, and returns
// them with the GitHub credential the row held. Their user_workflows rows go
// with them through the foreign key's cascade; their drafts and feedback have
// no foreign key (local mode's rows have no user to reference), so they are
// deleted here.
func deleteUserTx(tx *sql.Tx, login string) (DeletedUser, error) {
	var credential sql.NullString
	if err := tx.QueryRow("SELECT github_token FROM users WHERE login = ?", login).Scan(&credential); err != nil {
		return DeletedUser{}, err
	}
	for _, stmt := range []string{
		"DELETE FROM LocalComment WHERE user_login = ?",
		"DELETE FROM Feedback WHERE user_login = ?",
		"DELETE FROM users WHERE login = ?",
	} {
		if _, err := tx.Exec(stmt, login); err != nil {
			return DeletedUser{}, err
		}
	}
	return DeletedUser{Login: login, GitHubCredential: credential.String}, nil
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

	canonical, err := canonicalLoginTx(tx, login)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM user_workflows WHERE user_login = ?", canonical); err != nil {
		return err
	}
	if err := insertUserWorkflowsTx(tx, canonical, workflows); err != nil {
		return err
	}
	return tx.Commit()
}

// canonicalLoginTx returns login as the user registered it, or ErrNotFound.
// Rows are stored under that case, whatever case the caller used, so
// GetAllUserWorkflows keys them consistently.
func canonicalLoginTx(tx *sql.Tx, login string) (string, error) {
	var canonical string
	err := tx.QueryRow("SELECT login FROM users WHERE login = ?", login).Scan(&canonical)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return canonical, err
}

// insertUserWorkflowsTx adds workflows to login's list; login is the case the
// user registered with.
func insertUserWorkflowsTx(tx *sql.Tx, login string, workflows []UserWorkflow) error {
	for _, wf := range workflows {
		if _, err := tx.Exec(
			"INSERT INTO user_workflows (user_login, position, name, workflow_json) VALUES (?, ?, ?, ?)",
			login, wf.Position, wf.Name, wf.JSON,
		); err != nil {
			return err
		}
	}
	return nil
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
