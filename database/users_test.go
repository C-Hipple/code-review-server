package database

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func createTestUser(t *testing.T, db *DB, u User) {
	t.Helper()
	if err := db.CreateUser(u); err != nil {
		t.Fatalf("CreateUser(%q): %v", u.Login, err)
	}
}

func TestUserCRUD(t *testing.T) {
	db := newTestDB(t)

	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	createTestUser(t, db, User{
		Login:           "Octocat",
		TokenHash:       "hash-1",
		SectionPriority: map[string]int{"Needs Review": 1},
		SectionSorting:  map[string]string{"Needs Review": "newest_first"},
		Repos:           []string{"o/r"},
		CreatedAt:       created,
		LastSeen:        created,
	})

	// Logins match case-insensitively, like GitHub's, but come back in the
	// case the user registered with.
	for _, lookup := range []string{"Octocat", "octocat", "OCTOCAT"} {
		u, err := db.GetUser(lookup)
		if err != nil {
			t.Fatalf("GetUser(%q): %v", lookup, err)
		}
		if u.Login != "Octocat" {
			t.Errorf("GetUser(%q).Login = %q, want Octocat", lookup, u.Login)
		}
	}
	u, err := db.GetUserByTokenHash("hash-1")
	if err != nil {
		t.Fatalf("GetUserByTokenHash: %v", err)
	}
	want := User{
		Login:           "Octocat",
		TokenHash:       "hash-1",
		SectionPriority: map[string]int{"Needs Review": 1},
		SectionSorting:  map[string]string{"Needs Review": "newest_first"},
		Repos:           []string{"o/r"},
		CreatedAt:       created,
		LastSeen:        created,
	}
	if !reflect.DeepEqual(*u, want) {
		t.Errorf("round trip:\n got %+v\nwant %+v", *u, want)
	}

	if _, err := db.GetUser("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUser for an unknown login: err = %v, want ErrNotFound", err)
	}
	if _, err := db.GetUserByTokenHash("hash-unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUserByTokenHash for an unknown hash: err = %v, want ErrNotFound", err)
	}

	// A login is registered once, in any case, and a token belongs to one user.
	if err := db.CreateUser(User{Login: "OCTOCAT", TokenHash: "hash-2"}); err == nil {
		t.Error("registering a login twice, in another case, was accepted")
	}
	if err := db.CreateUser(User{Login: "hubot", TokenHash: "hash-1"}); err == nil {
		t.Error("a second user with the same token hash was accepted")
	}

	// Settings left nil are stored empty, not null, and default the
	// timestamps to now.
	before := time.Now().Add(-time.Second)
	createTestUser(t, db, User{Login: "hubot", TokenHash: "hash-2", IsAdmin: true})
	hubot, err := db.GetUser("hubot")
	if err != nil {
		t.Fatal(err)
	}
	if hubot.SectionPriority == nil || len(hubot.SectionPriority) != 0 ||
		hubot.SectionSorting == nil || len(hubot.SectionSorting) != 0 ||
		hubot.Repos == nil || len(hubot.Repos) != 0 || !hubot.IsAdmin {
		t.Errorf("a user created with no settings read back as %+v", hubot)
	}
	if hubot.CreatedAt.Before(before.Truncate(time.Second)) || hubot.LastSeen.Before(before.Truncate(time.Second)) {
		t.Errorf("timestamps did not default to now: created %v, last seen %v", hubot.CreatedAt, hubot.LastSeen)
	}
	var raw string
	if err := db.QueryRow("SELECT section_priority || section_sorting || repos FROM users WHERE login = 'hubot'").Scan(&raw); err != nil || raw != "{}{}[]" {
		t.Errorf("empty settings stored as %q (err %v), want {}{}[]", raw, err)
	}

	// UpdateUserSettings replaces the settings, and nothing else.
	if err := db.UpdateUserSettings(User{
		Login:           "octocat",
		TokenHash:       "ignored",
		SectionPriority: map[string]int{"Authored": 0},
		SectionSorting:  nil,
		Repos:           []string{"o/r", "o/s"},
		IsAdmin:         true,
	}); err != nil {
		t.Fatalf("UpdateUserSettings: %v", err)
	}
	u, err = db.GetUser("octocat")
	if err != nil {
		t.Fatal(err)
	}
	want = User{
		Login:           "Octocat",
		TokenHash:       "hash-1",
		SectionPriority: map[string]int{"Authored": 0},
		SectionSorting:  map[string]string{},
		Repos:           []string{"o/r", "o/s"},
		IsAdmin:         true,
		CreatedAt:       created,
		LastSeen:        created,
	}
	if !reflect.DeepEqual(*u, want) {
		t.Errorf("after UpdateUserSettings:\n got %+v\nwant %+v", *u, want)
	}
	if err := db.UpdateUserSettings(User{Login: "nobody"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateUserSettings for an unknown login: err = %v, want ErrNotFound", err)
	}

	users, err := db.ListUsers()
	if err != nil {
		t.Fatal(err)
	}
	var logins []string
	for _, u := range users {
		logins = append(logins, u.Login)
	}
	// Ordered by login, case-insensitively.
	if strings.Join(logins, ",") != "hubot,Octocat" {
		t.Errorf("ListUsers = %q, want [hubot Octocat]", logins)
	}
}

func TestTouchUser(t *testing.T) {
	db := newTestDB(t)
	createTestUser(t, db, User{Login: "octocat", TokenHash: "h"})

	// Stored in UTC whatever zone the caller's clock is in, so it compares
	// against a cutoff written the same way.
	seen := time.Date(2026, 10, 11, 9, 30, 15, 500_000_000, time.FixedZone("EST", -5*3600))
	if err := db.TouchUser("OctoCat", seen); err != nil {
		t.Fatalf("TouchUser: %v", err)
	}
	u, err := db.GetUser("octocat")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 11, 14, 30, 15, 0, time.UTC); !u.LastSeen.Equal(want) || u.LastSeen.Location() != time.UTC {
		t.Errorf("LastSeen = %v, want %v", u.LastSeen, want)
	}
	// The text as stored, in CURRENT_TIMESTAMP's format (concatenating keeps
	// the driver from parsing it into a time.Time on the way out).
	var raw string
	if err := db.QueryRow("SELECT last_seen || '' FROM users").Scan(&raw); err != nil || raw != "2026-10-11 14:30:15" {
		t.Errorf("last_seen stored as %q (err %v), want %q", raw, err, "2026-10-11 14:30:15")
	}
	if err := db.TouchUser("nobody", seen); !errors.Is(err, ErrNotFound) {
		t.Errorf("TouchUser for an unknown login: err = %v, want ErrNotFound", err)
	}
}

// seedUserData gives a user a workflow, a draft, feedback and a dashboard item.
func seedUserData(t *testing.T, db *DB, login string) *Section {
	t.Helper()
	if err := db.ReplaceUserWorkflows(login, []UserWorkflow{{Position: 0, Name: "review", JSON: `{"Name":"review"}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertLocalComment(login, "o", "r", 1, "main.go", 3, ptrTo(login+"'s draft"), nil); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFeedback(login, "o", "r", 1, ptrTo(login+"'s feedback")); err != nil {
		t.Fatal(err)
	}
	section, err := db.GetOrCreateSection(login, "Needs Review", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpsertItemWithWorkflow(section.ID, "o/r-1", "TODO", "a pr", []string{}, nil, 0, login+"\x1freview"); err != nil {
		t.Fatal(err)
	}
	return section
}

// hasUserData reports which of a user's rows remain, as
// "workflows drafts feedback items".
func hasUserData(t *testing.T, db *DB, login string) string {
	t.Helper()
	count := func(query string) int {
		var n int
		if err := db.QueryRow(query, login).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return strings.Join([]string{
		map[bool]string{true: "workflows", false: "-"}[count("SELECT COUNT(*) FROM user_workflows WHERE user_login = ?") > 0],
		map[bool]string{true: "drafts", false: "-"}[count("SELECT COUNT(*) FROM LocalComment WHERE user_login = ?") > 0],
		map[bool]string{true: "feedback", false: "-"}[count("SELECT COUNT(*) FROM Feedback WHERE user_login = ?") > 0],
		map[bool]string{true: "items", false: "-"}[count("SELECT COUNT(*) FROM items i JOIN sections s ON s.id = i.section_id WHERE s.user_login = ?") > 0],
	}, " ")
}

// Deleting a user takes their workflows, drafts and feedback, and leaves their
// sections and items to the workflow cycle's release and prune pass.
func TestDeleteUser(t *testing.T) {
	db := newTestDB(t)
	createTestUser(t, db, User{Login: "alice", TokenHash: "a"})
	createTestUser(t, db, User{Login: "bob", TokenHash: "b"})
	seedUserData(t, db, "alice")
	seedUserData(t, db, "bob")
	// The server's own drafts are not anyone's to delete.
	if _, err := db.InsertLocalComment("", "o", "r", 1, "main.go", 3, ptrTo("server's draft"), nil); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteUser("ALICE"); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if _, err := db.GetUser("alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUser after DeleteUser: err = %v, want ErrNotFound", err)
	}
	if got := hasUserData(t, db, "alice"); got != "- - - items" {
		t.Errorf("alice's remaining rows = %q, want only her items (%q)", got, "- - - items")
	}
	if got := hasUserData(t, db, "bob"); got != "workflows drafts feedback items" {
		t.Errorf("bob's rows after deleting alice = %q, want all of them", got)
	}
	if comments, _ := db.GetLocalCommentsForPR("", "o", "r", 1); len(comments) != 1 {
		t.Errorf("the server's drafts after deleting alice = %d, want 1", len(comments))
	}

	if err := db.DeleteUser("alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a user twice: err = %v, want ErrNotFound", err)
	}
	// "" is the server's identity, not a user.
	if err := db.DeleteUser(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteUser(\"\"): err = %v, want ErrNotFound", err)
	}
}

func TestDeleteUsersIdleSince(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-24 * time.Hour)

	for login, lastSeen := range map[string]time.Time{
		"idle":     now.Add(-48 * time.Hour),
		"boundary": cutoff,
		"active":   now,
		// Last seen just before the cutoff, from a clock in another zone.
		"elsewhere": cutoff.Add(-time.Second).In(time.FixedZone("PST", -8*3600)),
	} {
		createTestUser(t, db, User{Login: login, TokenHash: "hash-" + login})
		if err := db.TouchUser(login, lastSeen); err != nil {
			t.Fatal(err)
		}
		seedUserData(t, db, login)
	}

	deleted, err := db.DeleteUsersIdleSince(cutoff)
	if err != nil {
		t.Fatalf("DeleteUsersIdleSince: %v", err)
	}
	// Last seen exactly at the cutoff is not idle *since* it.
	if strings.Join(deleted, ",") != "elsewhere,idle" {
		t.Errorf("deleted = %q, want [elsewhere idle]", deleted)
	}
	for login, want := range map[string]string{
		"idle":      "- - - items",
		"elsewhere": "- - - items",
		"boundary":  "workflows drafts feedback items",
		"active":    "workflows drafts feedback items",
	} {
		if got := hasUserData(t, db, login); got != want {
			t.Errorf("%s's rows = %q, want %q", login, got, want)
		}
	}

	// last_seen is stored to the second, so a cutoff within the boundary
	// user's second still keeps them, and the next second sweeps them.
	if deleted, err := db.DeleteUsersIdleSince(cutoff.Add(500 * time.Millisecond)); err != nil || len(deleted) != 0 {
		t.Errorf("a cutoff within the last-seen second deleted %q (err %v), want none", deleted, err)
	}
	if deleted, err := db.DeleteUsersIdleSince(cutoff.Add(time.Second)); err != nil || strings.Join(deleted, ",") != "boundary" {
		t.Errorf("a cutoff one second later deleted %q (err %v), want [boundary]", deleted, err)
	}
	if deleted, err := db.DeleteUsersIdleSince(cutoff.Add(time.Second)); err != nil || len(deleted) != 0 {
		t.Errorf("a repeated sweep deleted %q (err %v), want none", deleted, err)
	}
	if _, err := db.GetUser("active"); err != nil {
		t.Errorf("the active user was swept: %v", err)
	}
}

func TestUserWorkflows(t *testing.T) {
	db := newTestDB(t)
	createTestUser(t, db, User{Login: "Alice", TokenHash: "a"})
	createTestUser(t, db, User{Login: "bob", TokenHash: "b"})
	createTestUser(t, db, User{Login: "carol", TokenHash: "c"})

	names := func(workflows []UserWorkflow) string {
		var out []string
		for _, wf := range workflows {
			out = append(out, wf.Name)
		}
		return strings.Join(out, ",")
	}

	// Stored and returned in the user's order, not the order of the call.
	if err := db.ReplaceUserWorkflows("alice", []UserWorkflow{
		{Position: 2, Name: "authored", JSON: `{"Name":"authored"}`},
		{Position: 0, Name: "review", JSON: `{"Name":"review"}`},
		{Position: 1, Name: "team", JSON: `{"Name":"team"}`},
	}); err != nil {
		t.Fatalf("ReplaceUserWorkflows: %v", err)
	}
	if err := db.ReplaceUserWorkflows("bob", []UserWorkflow{{Position: 0, Name: "review", JSON: `{"Name":"review"}`}}); err != nil {
		t.Fatalf("ReplaceUserWorkflows(bob): %v", err)
	}

	got, err := db.GetUserWorkflows("ALICE")
	if err != nil {
		t.Fatal(err)
	}
	if names(got) != "review,team,authored" || got[0].JSON != `{"Name":"review"}` || got[2].Position != 2 {
		t.Errorf("GetUserWorkflows(alice) = %+v, want review, team, authored", got)
	}

	all, err := db.GetAllUserWorkflows()
	if err != nil {
		t.Fatal(err)
	}
	// Keyed by the login as registered, whatever case the writer used; carol
	// has no workflows and is absent.
	if len(all) != 2 || names(all["Alice"]) != "review,team,authored" || names(all["bob"]) != "review" {
		t.Errorf("GetAllUserWorkflows = %+v", all)
	}

	// A replacement replaces the whole list.
	if err := db.ReplaceUserWorkflows("Alice", []UserWorkflow{{Position: 0, Name: "only", JSON: `{"Name":"only"}`}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.GetUserWorkflows("Alice"); names(got) != "only" {
		t.Errorf("after replacing alice's list: %q, want [only]", names(got))
	}

	// Two workflows of one name fail the whole write and keep the old list.
	err = db.ReplaceUserWorkflows("Alice", []UserWorkflow{
		{Position: 0, Name: "dup", JSON: `{}`},
		{Position: 1, Name: "dup", JSON: `{}`},
	})
	if err == nil {
		t.Error("two workflows of the same name were accepted")
	}
	if got, _ := db.GetUserWorkflows("Alice"); names(got) != "only" {
		t.Errorf("a failed replacement changed alice's list to %q", names(got))
	}

	if err := db.ReplaceUserWorkflows("nobody", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReplaceUserWorkflows for an unknown login: err = %v, want ErrNotFound", err)
	}
	if got, err := db.GetUserWorkflows("nobody"); err != nil || len(got) != 0 {
		t.Errorf("GetUserWorkflows for an unknown login = %+v (err %v), want none", got, err)
	}

	// An empty list clears it.
	if err := db.ReplaceUserWorkflows("bob", nil); err != nil {
		t.Fatal(err)
	}
	if all, _ := db.GetAllUserWorkflows(); len(all) != 1 {
		t.Errorf("GetAllUserWorkflows after clearing bob's = %+v, want only alice's", all)
	}
}
