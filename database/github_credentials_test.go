package database

import (
	"errors"
	"testing"
	"time"
)

func TestUserGitHubCredential(t *testing.T) {
	db := newTestDB(t)
	createTestUser(t, db, User{Login: "Alice", TokenHash: "hash-a"})

	if sealed, err := db.GetUserGitHubCredential("alice"); err != nil || sealed != "" {
		t.Errorf("before linking: %q, %v", sealed, err)
	}
	if _, err := db.GetUserGitHubCredential("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a user who does not exist: err = %v", err)
	}

	// Stored only for the token that linked it.
	if err := db.SetUserGitHubCredential("alice", "hash-other", "v1:x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("storing under another token: err = %v, want ErrNotFound", err)
	}
	if err := db.SetUserGitHubCredential("nobody", "hash-a", "v1:x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("storing for nobody: err = %v, want ErrNotFound", err)
	}
	if err := db.SetUserGitHubCredential("ALICE", "hash-a", "v1:first"); err != nil {
		t.Fatalf("SetUserGitHubCredential: %v", err)
	}
	if sealed, _ := db.GetUserGitHubCredential("alice"); sealed != "v1:first" {
		t.Errorf("stored %q", sealed)
	}

	// Re-registering keeps the link.
	if err := db.UpdateUserWithWorkflows(User{Login: "alice"}, nil); err != nil {
		t.Fatal(err)
	}
	if sealed, _ := db.GetUserGitHubCredential("alice"); sealed != "v1:first" {
		t.Errorf("after updating the registration: %q", sealed)
	}

	// Swapped only while it is still what the caller read.
	if ok, err := db.ReplaceUserGitHubCredential("alice", "v1:stale", "v1:second"); err != nil || ok {
		t.Errorf("replacing a stale credential: ok %v, err %v", ok, err)
	}
	if ok, err := db.ReplaceUserGitHubCredential("alice", "v1:first", "v1:second"); err != nil || !ok {
		t.Errorf("replacing the current credential: ok %v, err %v", ok, err)
	}
	if ok, err := db.ReplaceUserGitHubCredential("alice", "v1:second", ""); err != nil || !ok {
		t.Errorf("clearing the current credential: ok %v, err %v", ok, err)
	}
	if sealed, _ := db.GetUserGitHubCredential("alice"); sealed != "" {
		t.Errorf("after clearing: %q", sealed)
	}
	if ok, _ := db.ReplaceUserGitHubCredential("alice", "", "v1:x"); ok {
		t.Error("an empty old value matched a cleared credential")
	}

	if err := db.SetUserGitHubCredential("alice", "hash-a", "v1:third"); err != nil {
		t.Fatal(err)
	}
	if cleared, err := db.ClearUserGitHubCredential("Alice"); err != nil || cleared != "v1:third" {
		t.Errorf("ClearUserGitHubCredential = %q, %v", cleared, err)
	}
	if cleared, err := db.ClearUserGitHubCredential("alice"); err != nil || cleared != "" {
		t.Errorf("clearing twice = %q, %v", cleared, err)
	}
	if _, err := db.ClearUserGitHubCredential("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("clearing nobody's: err = %v", err)
	}
}

// Deleting a user hands back the credential they held, for the server to
// revoke on GitHub.
func TestDeletingAUserReturnsTheirCredential(t *testing.T) {
	db := newTestDB(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for _, login := range []string{"Alice", "bob", "carol"} {
		createTestUser(t, db, User{Login: login, TokenHash: "hash-" + login})
		if err := db.TouchUser(login, now.Add(-48*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetUserGitHubCredential("Alice", "hash-Alice", "v1:alice"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserGitHubCredential("bob", "hash-bob", "v1:bob"); err != nil {
		t.Fatal(err)
	}

	deleted, err := db.DeleteUserWithCredential("alice")
	if err != nil || deleted != (DeletedUser{Login: "Alice", GitHubCredential: "v1:alice"}) {
		t.Errorf("DeleteUserWithCredential = %+v, %v", deleted, err)
	}
	if _, err := db.DeleteUserWithCredential("alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting twice: err = %v", err)
	}

	swept, err := db.DeleteUsersIdleSinceWithCredentials(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := []DeletedUser{{Login: "bob", GitHubCredential: "v1:bob"}, {Login: "carol"}}
	if len(swept) != len(want) || swept[0] != want[0] || swept[1] != want[1] {
		t.Errorf("swept %+v, want %+v", swept, want)
	}
}
