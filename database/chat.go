package database

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ChatSession is one review chat: a conversation a reviewer holds with a local
// agent (the chat package's backends) about one PR.
//
// BackendSessionID is the agent's own handle on the conversation (Claude
// Code's session ID), which the next turn resumes; the agent keeps its full
// transcript itself. HeadSHA is the PR head whose description and diff the
// first message carried, so a client can say when the PR has moved on since.
//
// A PR can have any number of chats, each its own conversation with the agent
// (a fresh context): the reviewer starts a new one to clear the context and
// goes back to an older one to pick it up again. ForkedFrom names the chat a
// fork copied its messages from (ForkChatSession), empty for one started
// afresh.
//
// Whether a turn is running is tracked in memory by the chat manager, not
// here, so a restart can never leave a row claiming to be busy.
type ChatSession struct {
	ID               string
	Owner            string
	Repo             string // the short repo name, as the other caches key it
	Number           int
	Backend          string
	Model            string
	BackendSessionID string
	HeadSHA          string
	Title            string
	CostUSD          float64
	ForkedFrom       string
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// Read from the chat's messages, for a list of chats; CreateChatSession
	// ignores them. Turns counts the reviewer's messages, and Preview is the
	// start of the agent's last reply, else of the reviewer's last message.
	Turns   int
	Preview string
}

// ChatMessage is one entry in a chat: what the reviewer wrote, what the agent
// answered, a tool the agent used, or an error. Meta is a JSON object whose
// fields depend on the role; the chat package owns its shape.
type ChatMessage struct {
	ID        int64
	SessionID string
	Role      string
	Content   string
	Meta      string
	CreatedAt time.Time
}

// CreateChatSession stores a new chat. Its timestamps are set here.
func (db *DB) CreateChatSession(s ChatSession) error {
	return insertChatSession(db.conn, s)
}

// execer is what an insert needs: the connection, or a transaction.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertChatSession(e execer, s ChatSession) error {
	_, err := e.Exec(
		`INSERT INTO ChatSessions (id, owner, repo, pr_number, backend, model, backend_session_id, head_sha, title, cost_usd, forked_from)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Owner, s.Repo, s.Number, s.Backend, s.Model, s.BackendSessionID, s.HeadSHA, s.Title, s.CostUSD, s.ForkedFrom,
	)
	return err
}

// chatSessionColumns reads a chat (as s) with what a list shows of its
// messages: the reviewer's turns, and up to 300 characters of the last reply
// (a client cuts it to fit).
const chatSessionColumns = `s.id, s.owner, s.repo, s.pr_number, s.backend, s.model, s.backend_session_id, s.head_sha,
	s.title, s.cost_usd, s.forked_from, s.created_at, s.updated_at,
	(SELECT COUNT(*) FROM ChatMessages m WHERE m.session_id = s.id AND m.role = 'user'),
	COALESCE(
		(SELECT substr(m.content, 1, 300) FROM ChatMessages m WHERE m.session_id = s.id AND m.role = 'assistant' ORDER BY m.id DESC LIMIT 1),
		(SELECT substr(m.content, 1, 300) FROM ChatMessages m WHERE m.session_id = s.id AND m.role = 'user' ORDER BY m.id DESC LIMIT 1),
		'')`

func scanChatSession(s interface{ Scan(...any) error }) (ChatSession, error) {
	var cs ChatSession
	var created, updated sql.NullString
	if err := s.Scan(&cs.ID, &cs.Owner, &cs.Repo, &cs.Number, &cs.Backend, &cs.Model, &cs.BackendSessionID,
		&cs.HeadSHA, &cs.Title, &cs.CostUSD, &cs.ForkedFrom, &created, &updated, &cs.Turns, &cs.Preview); err != nil {
		return ChatSession{}, err
	}
	cs.CreatedAt = parseSQLiteTime(created.String)
	cs.UpdatedAt = parseSQLiteTime(updated.String)
	return cs, nil
}

// GetChatSession returns the chat with the given ID; ok is false when there is
// none.
func (db *DB) GetChatSession(id string) (ChatSession, bool, error) {
	row := db.conn.QueryRow(`SELECT `+chatSessionColumns+` FROM ChatSessions s WHERE s.id = ?`, id)
	cs, err := scanChatSession(row)
	if err == sql.ErrNoRows {
		return ChatSession{}, false, nil
	}
	if err != nil {
		return ChatSession{}, false, err
	}
	return cs, true, nil
}

// ListChatSessions returns a PR's chats, the most recently active first.
// Owner and repo match whatever their case, as GitHub's names do.
func (db *DB) ListChatSessions(owner, repo string, number int) ([]ChatSession, error) {
	rows, err := db.conn.Query(
		`SELECT `+chatSessionColumns+` FROM ChatSessions s
		 WHERE s.owner = ? COLLATE NOCASE AND s.repo = ? COLLATE NOCASE AND s.pr_number = ?
		 ORDER BY s.updated_at DESC, s.rowid DESC`,
		owner, repo, number,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []ChatSession
	for rows.Next() {
		cs, err := scanChatSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, cs)
	}
	return sessions, rows.Err()
}

// RecordChatTurn notes what a finished turn left behind: the agent's session
// handle, when it reported one (an empty one leaves the stored handle alone),
// and what the turn cost, added to the chat's total.
func (db *DB) RecordChatTurn(id, backendSessionID string, costUSD float64) error {
	_, err := db.conn.Exec(
		`UPDATE ChatSessions SET
			backend_session_id = CASE WHEN ? != '' THEN ? ELSE backend_session_id END,
			cost_usd = cost_usd + ?,
			updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		backendSessionID, backendSessionID, costUSD, id,
	)
	return err
}

// RenameChatSession sets a chat's title. A chat isn't more recently active
// for it, so its place in the list stays.
func (db *DB) RenameChatSession(id, title string) error {
	_, err := db.conn.Exec(`UPDATE ChatSessions SET title = ? WHERE id = ?`, title, id)
	return err
}

// SetChatHeadSHA records the PR head a chat's conversation attached, when the
// chat opens a new conversation with the agent after its first.
func (db *DB) SetChatHeadSHA(id, sha string) error {
	_, err := db.conn.Exec(`UPDATE ChatSessions SET head_sha = ? WHERE id = ?`, sha, id)
	return err
}

// ForkChatSession stores fork, a new chat, with a copy of the messages of the
// chat it names in ForkedFrom whose role is one of roles, in their order and
// with their times. Both happen or neither does.
func (db *DB) ForkChatSession(fork ChatSession, roles []string) error {
	if fork.ForkedFrom == "" {
		return fmt.Errorf("a fork names the chat it copies")
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertChatSession(tx, fork); err != nil {
		return err
	}
	if len(roles) > 0 {
		args := []any{fork.ID, fork.ForkedFrom}
		for _, r := range roles {
			args = append(args, r)
		}
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(roles)), ", ")
		if _, err := tx.Exec(
			`INSERT INTO ChatMessages (session_id, role, content, meta, created_at)
			 SELECT ?, role, content, meta, created_at FROM ChatMessages
			 WHERE session_id = ? AND role IN (`+marks+`) ORDER BY id`,
			args...,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteChatSession removes a chat and its messages.
func (db *DB) DeleteChatSession(id string) error {
	_, err := db.conn.Exec(`DELETE FROM ChatSessions WHERE id = ?`, id)
	return err
}

// AddChatMessage appends a message to a chat and returns its ID, which also
// orders the chat's messages. The chat counts as active as of now.
func (db *DB) AddChatMessage(m ChatMessage) (int64, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO ChatMessages (session_id, role, content, meta) VALUES (?, ?, ?, ?)`,
		m.SessionID, m.Role, m.Content, m.Meta,
	)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE ChatSessions SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`, m.SessionID); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateChatMessage rewrites a stored message's content and meta, e.g. a tool
// call once its result is in.
func (db *DB) UpdateChatMessage(id int64, content, meta string) error {
	_, err := db.conn.Exec(`UPDATE ChatMessages SET content = ?, meta = ? WHERE id = ?`, content, meta, id)
	return err
}

// GetChatMessages returns a chat's messages in the order they were added.
func (db *DB) GetChatMessages(sessionID string) ([]ChatMessage, error) {
	rows, err := db.conn.Query(
		`SELECT id, session_id, role, content, meta, created_at FROM ChatMessages
		 WHERE session_id = ? ORDER BY id`,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var created sql.NullString
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &m.Content, &m.Meta, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = parseSQLiteTime(created.String)
		messages = append(messages, m)
	}
	return messages, rows.Err()
}
