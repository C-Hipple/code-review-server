package database

import "database/sql"

// A linked user's GitHub credential: what lets a hosted server write to
// GitHub as the user themselves rather than as its own token's account. It is
// kept in users.github_token sealed by the server (package ghauth) and is
// opaque here: the database never holds a token in the clear, and a sealed
// one opens only with the server's key and for the row it was sealed for.

// SetUserGitHubCredential stores sealed as login's GitHub credential,
// provided login is still registered under tokenHash — the bearer token of
// the user who linked it. A link that completes after its user was deleted,
// or deleted and registered again by someone holding another token, is stored
// nowhere: that is ErrNotFound.
func (db *DB) SetUserGitHubCredential(login, tokenHash, sealed string) error {
	res, err := db.conn.Exec("UPDATE users SET github_token = ? WHERE login = ? AND token_hash = ?", sealed, login, tokenHash)
	return requireOneRow(res, err)
}

// GetUserGitHubCredential returns login's sealed GitHub credential, or "" when
// they have not linked one. Returns ErrNotFound when no user has that login.
func (db *DB) GetUserGitHubCredential(login string) (string, error) {
	var sealed sql.NullString
	err := db.conn.QueryRow("SELECT github_token FROM users WHERE login = ?", login).Scan(&sealed)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	return sealed.String, err
}

// ReplaceUserGitHubCredential swaps login's credential from old to new, ""
// clearing it, only while it is still old, and reports whether it did. A
// write that acts on a credential read earlier — storing its refreshed
// successor, clearing one GitHub refused — goes through it, so it never
// undoes a link made in the meantime.
func (db *DB) ReplaceUserGitHubCredential(login, old, new string) (bool, error) {
	var value any
	if new != "" {
		value = new
	}
	res, err := db.conn.Exec("UPDATE users SET github_token = ? WHERE login = ? AND github_token = ?", value, login, old)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ClearUserGitHubCredential removes login's GitHub credential and returns the
// one removed ("" when there was none), for the caller to revoke. Returns
// ErrNotFound when no user has that login.
func (db *DB) ClearUserGitHubCredential(login string) (string, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var sealed sql.NullString
	err = tx.QueryRow("SELECT github_token FROM users WHERE login = ?", login).Scan(&sealed)
	if err == sql.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec("UPDATE users SET github_token = NULL WHERE login = ?", login); err != nil {
		return "", err
	}
	return sealed.String, tx.Commit()
}
