package database

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// preUserSchema is the tables migratePerUserColumns changes, plus items (whose
// foreign key into sections is what makes the sections rebuild dangerous), as
// the version before per-user dashboards created them.
const preUserSchema = `
CREATE TABLE sections (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	section_name TEXT NOT NULL,
	priority INTEGER DEFAULT 0,
	UNIQUE(section_name)
);

CREATE TABLE items (
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

CREATE TABLE LocalComment (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL,
	number INTEGER NOT NULL,
	filename TEXT NOT NULL,
	position INTEGER NOT NULL,
	body TEXT,
	reply_to_id INTEGER
);

CREATE TABLE Feedback (
	id INTEGER PRIMARY KEY,
	owner TEXT NOT NULL,
	repo TEXT NOT NULL,
	number INTEGER NOT NULL,
	body TEXT,
	UNIQUE(owner, repo, number)
);

CREATE INDEX idx_items_section ON items(section_id);
CREATE INDEX idx_items_identifier ON items(identifier);
CREATE INDEX idx_localcomments_pr ON LocalComment(owner, repo, number);
`

// writePreUserDB creates a database file the way the version before per-user
// dashboards left it, with foreign keys on as NewDB opens it, runs the given
// statements against it, and returns its path.
func writePreUserDB(t *testing.T, statements ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=1")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(preUserSchema); err != nil {
		t.Fatalf("creating the old schema: %v", err)
	}
	for _, stmt := range statements {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("seeding the old database: %q: %v", stmt, err)
		}
	}
	return path
}

func openDB(t *testing.T, path string) *DB {
	t.Helper()
	db, err := NewDB(path)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// tableShape renders a table's columns and unique constraints, for comparing a
// migrated table with a freshly created one.
func tableShape(t *testing.T, db *DB, table string) string {
	t.Helper()
	var sb strings.Builder
	rows, err := db.Query("SELECT name, type, \"notnull\", COALESCE(dflt_value, 'NULL'), pk FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, typ, dflt string
		var notNull, pk int
		if err := rows.Scan(&name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "column %s %s notnull=%d default=%s pk=%d\n", name, typ, notNull, dflt, pk)
	}
	rows.Close()

	// Unique constraints, by their columns: the autoindex names differ
	// between a fresh table and a renamed one.
	idx, err := db.Query(
		`SELECT (SELECT group_concat(name, ',') FROM pragma_index_info(il.name))
		 FROM pragma_index_list(?) il WHERE il."unique" = 1 ORDER BY 1`, table)
	if err != nil {
		t.Fatal(err)
	}
	for idx.Next() {
		var cols string
		if err := idx.Scan(&cols); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "unique(%s)\n", cols)
	}
	idx.Close()
	return sb.String()
}

// The migration that matters most: rebuilding sections must not take items
// down with it through ON DELETE CASCADE, and every id must survive, since
// items point at sections by id.
func TestPerUserMigrationKeepsEveryRow(t *testing.T) {
	path := writePreUserDB(t,
		`INSERT INTO sections (section_name, priority) VALUES ('Needs Review', 1), ('Waiting on Author', 2), ('Retired', 3)`,
		// AUTOINCREMENT remembers id 3 after the row is gone.
		`DELETE FROM sections WHERE section_name = 'Retired'`,
		`INSERT INTO items (section_id, identifier, status, title, details_json, workflows) VALUES
			(1, 'o/r-1', 'TODO', 'first', '[]', '["review_requests"]'),
			(1, 'o/r-2', 'TODO', 'second', '[]', '["review_requests","team_reviews"]'),
			(2, 'o/r-3', 'TODO', 'third', '[]', '["waiting"]')`,
		`INSERT INTO LocalComment (id, owner, repo, number, filename, position, body, reply_to_id) VALUES
			(10, 'o', 'r', 1, 'main.go', 3, 'top level', NULL),
			(11, 'o', 'r', 1, 'main.go', 3, 'a reply', 10)`,
		`INSERT INTO Feedback (id, owner, repo, number, body) VALUES (5, 'o', 'r', 1, 'looks good')`,
	)

	// Twice: the second open must find nothing left to migrate and change
	// nothing.
	for run := 1; run <= 2; run++ {
		db, err := NewDB(path)
		if err != nil {
			t.Fatalf("run %d: NewDB: %v", run, err)
		}

		sections, err := db.GetAllSections()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range sections {
			got = append(got, fmt.Sprintf("%d %q %q %d", s.ID, s.UserLogin, s.SectionName, s.Priority))
		}
		want := []string{`1 "" "Needs Review" 1`, `2 "" "Waiting on Author" 2`}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("run %d: sections = %q, want %q", run, got, want)
		}

		wantItems := map[int64][]string{
			1: {`o/r-1 ["review_requests"]`, `o/r-2 ["review_requests","team_reviews"]`},
			2: {`o/r-3 ["waiting"]`},
		}
		for sectionID, want := range wantItems {
			items, err := db.GetItemsBySection(sectionID)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, item := range items {
				got = append(got, item.Identifier+" "+item.Workflows)
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("run %d: items in section %d = %q, want %q", run, sectionID, got, want)
			}
		}
		// The join from items back to sections still lands on the right row.
		var name string
		err = db.QueryRow("SELECT s.section_name FROM items i JOIN sections s ON s.id = i.section_id WHERE i.identifier = 'o/r-3'").Scan(&name)
		if err != nil || name != "Waiting on Author" {
			t.Errorf("run %d: o/r-3 joins to section %q (err %v), want Waiting on Author", run, name, err)
		}

		comments, err := db.GetLocalCommentsForPR("", "o", "r", 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(comments) != 2 || comments[0].ID != 10 || comments[1].ID != 11 ||
			comments[1].ReplyToID == nil || *comments[1].ReplyToID != 10 || *comments[1].Body != "a reply" {
			t.Errorf("run %d: local comments did not survive intact: %+v", run, comments)
		}
		if feedback, err := db.GetFeedback("", "o", "r", 1); err != nil || feedback != "looks good" {
			t.Errorf("run %d: feedback = %q (err %v), want %q", run, feedback, err, "looks good")
		}
		var feedbackID int64
		if err := db.QueryRow("SELECT id FROM Feedback WHERE owner = 'o'").Scan(&feedbackID); err != nil || feedbackID != 5 {
			t.Errorf("run %d: feedback id = %d (err %v), want 5", run, feedbackID, err)
		}

		db.Close()
	}

	db := openDB(t, path)

	// Foreign keys are back on for the connection the rebuild borrowed (the
	// pool has only the one).
	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("PRAGMA foreign_keys = %d (err %v) after the migration, want 1", foreignKeys, err)
	}

	// The high-water mark carried across: the deleted section's id 3 is not
	// handed out again.
	alice, err := db.GetOrCreateSection("alice", "Needs Review", 0)
	if err != nil {
		t.Fatalf("a second user's section of the same name: %v", err)
	}
	if alice.ID != 4 {
		t.Errorf("new section id = %d, want 4 (AUTOINCREMENT must not reuse a deleted id)", alice.ID)
	}
	if _, err := db.Exec("INSERT INTO sections (user_login, section_name) VALUES ('', 'Needs Review')"); err == nil {
		t.Error("a second section of the same name for the same user was accepted")
	}
	if _, err := db.Exec("INSERT INTO sections (user_login, section_name) VALUES ('alice', 'Needs Review')"); err == nil {
		t.Error("a second section of the same name for alice was accepted")
	}
	if _, err := db.Exec("INSERT INTO Feedback (user_login, owner, repo, number, body) VALUES ('', 'o', 'r', 1, 'again')"); err == nil {
		t.Error("a second feedback row for the same user and PR was accepted")
	}
	if err := db.InsertFeedback("alice", "o", "r", 1, ptrTo("alice's")); err != nil {
		t.Errorf("another user's feedback on the same PR: %v", err)
	}

	// The cascade works again: deleting a section takes its items.
	if _, err := db.Exec("DELETE FROM sections WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	if items, _ := db.GetItemsBySection(2); len(items) != 0 {
		t.Errorf("deleting a section left %d items behind: foreign keys are off", len(items))
	}
	if n, _ := db.GetItemCount(); n != 2 {
		t.Errorf("item count = %d after deleting section 2, want 2", n)
	}
}

// A migrated table must be the same table a fresh database gets, so nothing
// depends on which one a server started from.
func TestPerUserMigrationMatchesFreshSchema(t *testing.T) {
	fresh := newTestDB(t)
	migrated := openDB(t, writePreUserDB(t))

	for _, table := range []string{"sections", "Feedback", "LocalComment", "items"} {
		want := tableShape(t, fresh, table)
		got := tableShape(t, migrated, table)
		if got != want {
			t.Errorf("%s after migration:\n%s\nwant (fresh):\n%s", table, got, want)
		}
	}
}

// Rows left dangling by a database written with foreign keys off are not the
// rebuild's doing; refusing to start over them would strand the database.
func TestPerUserMigrationToleratesExistingDanglingItems(t *testing.T) {
	path := writePreUserDB(t,
		`INSERT INTO sections (section_name) VALUES ('Needs Review')`,
		`INSERT INTO items (section_id, identifier, status, title, details_json) VALUES (1, 'o/r-1', 'TODO', 'kept', '[]')`,
		`PRAGMA foreign_keys=OFF`,
		`INSERT INTO items (section_id, identifier, status, title, details_json) VALUES (99, 'o/r-2', 'TODO', 'dangling', '[]')`,
	)

	db := openDB(t, path)
	if n, _ := db.GetItemCount(); n != 2 {
		t.Errorf("item count = %d after the migration, want 2", n)
	}
	if ok, _ := db.hasColumn("sections", "user_login"); !ok {
		t.Error("sections was not migrated")
	}
}

func ptrTo(s string) *string { return &s }

// Two users may both have a "Needs Review": each gets their own section, and
// each dashboard reads only its own.
func TestSectionsArePerUser(t *testing.T) {
	db := newTestDB(t)

	server, err := db.GetOrCreateSection("", "Needs Review", 1)
	if err != nil {
		t.Fatal(err)
	}
	alice, err := db.GetOrCreateSection("alice", "Needs Review", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetOrCreateSection("alice", "Authored", 0); err != nil {
		t.Fatal(err)
	}
	bob, err := db.GetOrCreateSection("bob", "Needs Review", 1)
	if err != nil {
		t.Fatal(err)
	}
	if server.ID == alice.ID || alice.ID == bob.ID || server.ID == bob.ID {
		t.Fatalf("same-named sections share an id: server %d, alice %d, bob %d", server.ID, alice.ID, bob.ID)
	}

	again, err := db.GetOrCreateSection("alice", "Needs Review", 1)
	if err != nil || again.ID != alice.ID {
		t.Errorf("GetOrCreateSection for an existing section = %+v (err %v), want id %d", again, err, alice.ID)
	}
	got, err := db.GetSection("bob", "Needs Review")
	if err != nil || got.ID != bob.ID || got.UserLogin != "bob" {
		t.Errorf("GetSection(bob) = %+v (err %v), want bob's section %d", got, err, bob.ID)
	}
	if _, err := db.GetSection("carol", "Needs Review"); err != sql.ErrNoRows {
		t.Errorf("GetSection for a user without the section: err = %v, want sql.ErrNoRows", err)
	}

	sections, err := db.GetSectionsForUser("alice")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range sections {
		if s.UserLogin != "alice" {
			t.Errorf("GetSectionsForUser(alice) returned %q's section %q", s.UserLogin, s.SectionName)
		}
		names = append(names, s.SectionName)
	}
	// Priority order, as the dashboard shows them.
	if strings.Join(names, ",") != "Authored,Needs Review" {
		t.Errorf("alice's sections = %q, want [Authored Needs Review]", names)
	}
	if all, _ := db.GetAllSections(); len(all) != 4 {
		t.Errorf("GetAllSections returned %d sections, want all 4", len(all))
	}

	// The same PR on both dashboards is two items.
	for _, section := range []*Section{server, alice, bob} {
		if _, err := db.UpsertItemWithWorkflow(section.ID, "o/r-1", "TODO", "a pr", []string{}, nil, 0, "review"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.UpsertItemWithWorkflow(bob.ID, "o/r-2", "TODO", "bob only", []string{}, nil, 0, "review"); err != nil {
		t.Fatal(err)
	}
	for user, want := range map[string][]string{
		"":      {"o/r-1"},
		"alice": {"o/r-1"},
		"bob":   {"o/r-1", "o/r-2"},
		"carol": nil,
	} {
		items, err := db.GetItemsForUser(user)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, item := range items {
			section := map[int64]string{server.ID: "", alice.ID: "alice", bob.ID: "bob"}[item.SectionID]
			if section != user {
				t.Errorf("GetItemsForUser(%q) returned an item from %q's section", user, section)
			}
			got = append(got, item.Identifier)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("GetItemsForUser(%q) = %q, want %q", user, got, want)
		}
	}
}

// Workflow names collide across users, so an ownership entry only survives in
// the section its key maps to: the same user's section of the same name.
func TestReleaseStaleWorkflowOwnershipPerUser(t *testing.T) {
	db := newTestDB(t)

	// Keys are opaque to the database; config.OwnershipKey builds the real
	// ones.
	const (
		serverKey = "review"
		aliceKey  = "alice\x1freview"
		bobKey    = "bob\x1freview"
	)
	serverSection, _ := db.GetOrCreateSection("", "Needs Review", 0)
	aliceSection, _ := db.GetOrCreateSection("alice", "Needs Review", 0)
	bobSection, _ := db.GetOrCreateSection("bob", "Needs Review", 0)

	seed := func(section *Section, identifier string, owners ...string) {
		t.Helper()
		for _, owner := range owners {
			if _, err := db.UpsertItemWithWorkflow(section.ID, identifier, "TODO", "a pr", []string{}, nil, 0, owner); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed(serverSection, "o/r-1", serverKey)
	seed(aliceSection, "o/r-1", aliceKey)
	seed(bobSection, "o/r-1", bobKey)
	// Claims that name the right section but the wrong user's dashboard.
	seed(aliceSection, "o/r-2", aliceKey, serverKey, bobKey)

	// bob is gone: his workflows are no longer in the owned set.
	released, err := db.ReleaseStaleWorkflowOwnership(map[string]SectionRef{
		serverKey: {User: "", Name: "Needs Review"},
		aliceKey:  {User: "alice", Name: "Needs Review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if released != 2 {
		t.Errorf("released = %d, want 2 (bob's item and alice's co-owned one)", released)
	}

	for _, tt := range []struct {
		section    *Section
		identifier string
		want       []string
	}{
		{serverSection, "o/r-1", []string{serverKey}},
		{aliceSection, "o/r-1", []string{aliceKey}},
		{bobSection, "o/r-1", []string{}},
		{aliceSection, "o/r-2", []string{aliceKey}},
	} {
		item, err := db.GetItem(tt.section.ID, tt.identifier)
		if err != nil {
			t.Fatal(err)
		}
		if got := item.GetWorkflows(); strings.Join(got, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%q's %s owners = %q, want %q", tt.section.UserLogin, tt.identifier, got, tt.want)
		}
	}
}

// keep spares only the named user's section: another user's empty section of
// the same name is retired.
func TestDeleteEmptySectionsNotInPerUser(t *testing.T) {
	db := newTestDB(t)

	for _, ref := range []SectionRef{
		{"", "Needs Review"},
		{"alice", "Needs Review"},
		{"alice", "Retired"},
		{"bob", "Needs Review"},
		{"bob", "Busy"},
	} {
		if _, err := db.GetOrCreateSection(ref.User, ref.Name, 0); err != nil {
			t.Fatal(err)
		}
	}
	busy, _ := db.GetSection("bob", "Busy")
	if _, err := db.UpsertItemWithWorkflow(busy.ID, "o/r-1", "TODO", "a pr", []string{}, nil, 0, "bob\x1fbusy"); err != nil {
		t.Fatal(err)
	}

	deleted, err := db.DeleteEmptySectionsNotIn([]SectionRef{
		{User: "", Name: "Needs Review"},
		{User: "alice", Name: "Needs Review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2 (alice's Retired and bob's Needs Review)", deleted)
	}
	for _, tt := range []struct {
		ref  SectionRef
		want bool
	}{
		{SectionRef{"", "Needs Review"}, true},
		{SectionRef{"alice", "Needs Review"}, true},
		{SectionRef{"alice", "Retired"}, false},
		{SectionRef{"bob", "Needs Review"}, false},
		{SectionRef{"bob", "Busy"}, true}, // not empty
	} {
		_, err := db.GetSection(tt.ref.User, tt.ref.Name)
		if exists := err == nil; exists != tt.want {
			t.Errorf("%+v exists = %v, want %v", tt.ref, exists, tt.want)
		}
	}
}

// Drafts are one user's business: no one else sees, edits or deletes them.
func TestLocalCommentsArePerUser(t *testing.T) {
	db := newTestDB(t)

	alice, err := db.InsertLocalComment("alice", "o", "r", 1, "main.go", 3, ptrTo("alice's draft"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if alice.UserLogin != "alice" {
		t.Errorf("InsertLocalComment returned UserLogin %q, want alice", alice.UserLogin)
	}
	if _, err := db.InsertLocalComment("alice", "o", "r", 1, "main.go", 7, ptrTo("another"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertLocalComment("bob", "o", "r", 1, "main.go", 3, ptrTo("bob's draft"), nil); err != nil {
		t.Fatal(err)
	}

	bodies := func(user string) []string {
		t.Helper()
		comments, err := db.GetLocalCommentsForPR(user, "o", "r", 1)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range comments {
			if c.UserLogin != user {
				t.Errorf("GetLocalCommentsForPR(%q) returned %q's comment", user, c.UserLogin)
			}
			out = append(out, *c.Body)
		}
		return out
	}
	if got := bodies("alice"); strings.Join(got, "|") != "alice's draft|another" {
		t.Errorf("alice's drafts = %q", got)
	}
	if got := bodies("bob"); strings.Join(got, "|") != "bob's draft" {
		t.Errorf("bob's drafts = %q", got)
	}
	if got := bodies(""); len(got) != 0 {
		t.Errorf("the server's identity sees %q, want no drafts", got)
	}

	// bob holding alice's id gets nowhere, and learns nothing.
	if err := db.UpdateLocalComment("bob", alice.ID, "defaced"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateLocalComment on another user's draft: err = %v, want ErrNotFound", err)
	}
	if err := db.DeleteLocalComment("bob", alice.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteLocalComment on another user's draft: err = %v, want ErrNotFound", err)
	}
	if err := db.UpdateLocalComment("", alice.ID, "defaced"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateLocalComment as the server's identity: err = %v, want ErrNotFound", err)
	}
	if got := bodies("alice"); strings.Join(got, "|") != "alice's draft|another" {
		t.Errorf("alice's drafts after bob's attempts = %q, want them unchanged", got)
	}

	if err := db.UpdateLocalComment("alice", alice.ID, "edited"); err != nil {
		t.Errorf("UpdateLocalComment on her own draft: %v", err)
	}
	// Saving an unchanged body still finds the row.
	if err := db.UpdateLocalComment("alice", alice.ID, "edited"); err != nil {
		t.Errorf("UpdateLocalComment with an unchanged body: %v", err)
	}
	if got := bodies("alice"); strings.Join(got, "|") != "edited|another" {
		t.Errorf("alice's drafts after her edit = %q", got)
	}
	if err := db.DeleteLocalComment("alice", alice.ID); err != nil {
		t.Errorf("DeleteLocalComment on her own draft: %v", err)
	}
	if err := db.DeleteLocalComment("alice", alice.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a draft twice: err = %v, want ErrNotFound", err)
	}

	// Discarding a PR's drafts discards only the caller's.
	if err := db.DeleteLocalCommentsForPR("bob", "o", "r", 1); err != nil {
		t.Fatal(err)
	}
	if got := bodies("bob"); len(got) != 0 {
		t.Errorf("bob's drafts after discarding them = %q", got)
	}
	if got := bodies("alice"); strings.Join(got, "|") != "another" {
		t.Errorf("alice's drafts after bob discarded his = %q, want [another]", got)
	}
}

func TestFeedbackIsPerUser(t *testing.T) {
	db := newTestDB(t)

	for user, body := range map[string]string{"": "server's", "alice": "alice's"} {
		if err := db.InsertFeedback(user, "o", "r", 1, ptrTo(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.InsertFeedback("alice", "o", "r", 1, ptrTo("alice's, revised")); err != nil {
		t.Fatal(err)
	}

	for user, want := range map[string]string{"": "server's", "alice": "alice's, revised", "bob": ""} {
		got, err := db.GetFeedback(user, "o", "r", 1)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("GetFeedback(%q) = %q, want %q", user, got, want)
		}
	}
}
