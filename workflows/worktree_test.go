package workflows

import (
	"crs/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGitOrFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupPRRepo lays out RepoLocation the way handleWorktreeChange expects it:
// a clone of the repo at <location>/<repo>, whose origin has the PR branch
// feature/thing. Returns the location and a clone that can push to origin.
func setupPRRepo(t *testing.T, repo string) (location, pusher string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	runGitOrFail(t, base, "init", "--bare", "-b", "main", origin)

	pusher = filepath.Join(base, "pusher")
	runGitOrFail(t, base, "clone", origin, pusher)
	runGitOrFail(t, pusher, "config", "user.email", "test@test")
	runGitOrFail(t, pusher, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(pusher, "f.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitOrFail(t, pusher, "add", ".")
	runGitOrFail(t, pusher, "commit", "-m", "init")
	runGitOrFail(t, pusher, "push", "origin", "HEAD:main")
	runGitOrFail(t, pusher, "checkout", "-b", "feature/thing")
	if err := os.WriteFile(filepath.Join(pusher, "f.txt"), []byte("a\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitOrFail(t, pusher, "commit", "-am", "feat")
	runGitOrFail(t, pusher, "push", "origin", "feature/thing")

	location = filepath.Join(base, "src")
	if err := os.MkdirAll(location, 0755); err != nil {
		t.Fatal(err)
	}
	runGitOrFail(t, location, "clone", "--branch", "main", origin, repo)
	return location, pusher
}

// The workflow caches write the short repo name (see "Cache Key Convention"
// in CLAUDE.md), while item identifiers carry owner/repo — and owners and
// repos both contain dashes. handleWorktreeChange has to cope with both, or
// no worktree is ever created and diff-lsp reads the main checkout instead.
func TestHandleWorktreeChangeCreatesAndUpdatesWorktree(t *testing.T) {
	cases := []struct{ owner, repo string }{
		{"acme", "widgets"},
		{"C-Hipple", "code-review-server"},
	}
	for _, c := range cases {
		t.Run(c.owner+"/"+c.repo, func(t *testing.T) {
			location, pusher := setupPRRepo(t, c.repo)
			db := warmTestDB(t)
			config.SetC(config.Config{DB: db, RepoLocation: location, AutoWorktree: true})
			t.Cleanup(func() { config.SetC(config.Config{}) })

			const number = 7
			// What fetchAuxDataForPR persists before the changes are applied.
			cacheHead := func(sha string) {
				t.Helper()
				if err := db.UpsertPullRequest(number, c.repo, sha, "", "diff"); err != nil {
					t.Fatal(err)
				}
			}
			firstHead := runGitOrFail(t, pusher, "rev-parse", "HEAD")
			cacheHead(firstHead)
			if err := db.UpsertPRMetadataCache(c.owner, c.repo, number, `{"head_ref":"feature/thing"}`); err != nil {
				t.Fatal(err)
			}
			change := func(changeType string) SerializedFileChange {
				return SerializedFileChange{FileChange: &FileChanges{
					ChangeType: changeType,
					Identifier: c.owner + "/" + c.repo + "-7",
				}}
			}

			handleWorktreeChange(db, change("Addition"))
			path, err := db.GetWorktree(number, c.repo, c.owner)
			if err != nil || path == "" {
				t.Fatalf("no worktree recorded for the PR (path=%q, err=%v)", path, err)
			}
			if got := runGitOrFail(t, path, "rev-parse", "HEAD"); got != firstHead {
				t.Fatalf("worktree HEAD = %s, want the PR head %s", got, firstHead)
			}

			// A push to the PR branch.
			if err := os.WriteFile(filepath.Join(pusher, "f.txt"), []byte("a\nb\nc\n"), 0644); err != nil {
				t.Fatal(err)
			}
			runGitOrFail(t, pusher, "commit", "-am", "more")
			runGitOrFail(t, pusher, "push", "origin", "feature/thing")
			secondHead := runGitOrFail(t, pusher, "rev-parse", "HEAD")

			// Until the cached diff moves to the new head, neither does the
			// worktree: the two have to describe the same commit.
			handleWorktreeChange(db, change("Update"))
			if got := runGitOrFail(t, path, "rev-parse", "HEAD"); got != firstHead {
				t.Fatalf("worktree HEAD = %s, want %s (the head the cached diff is for)", got, firstHead)
			}

			cacheHead(secondHead)
			handleWorktreeChange(db, change("Update"))
			if got := runGitOrFail(t, path, "rev-parse", "HEAD"); got != secondHead {
				t.Fatalf("worktree HEAD = %s after the push, want %s", got, secondHead)
			}
		})
	}
}

// SyncPR refetches the PR, caching its new head, and then moves the worktree
// there too, so the diff it hands back and the files diff-lsp reads match.
func TestSyncPRWorktreeMovesToCachedHead(t *testing.T) {
	const (
		owner  = "C-Hipple"
		repo   = "code-review-server"
		number = 7
	)
	location, pusher := setupPRRepo(t, repo)
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db, RepoLocation: location, AutoWorktree: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	if err := db.UpsertPullRequest(number, repo, runGitOrFail(t, pusher, "rev-parse", "HEAD"), "", "diff"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPRMetadataCache(owner, repo, number, `{"head_ref":"feature/thing"}`); err != nil {
		t.Fatal(err)
	}
	handleWorktreeChange(db, SerializedFileChange{FileChange: &FileChanges{
		ChangeType: "Addition", Identifier: owner + "/" + repo + "-7",
	}})
	path, _ := db.GetWorktree(number, repo, owner)
	if path == "" {
		t.Fatal("no worktree created")
	}

	if err := os.WriteFile(filepath.Join(pusher, "f.txt"), []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitOrFail(t, pusher, "commit", "-am", "more")
	runGitOrFail(t, pusher, "push", "origin", "feature/thing")
	newHead := runGitOrFail(t, pusher, "rev-parse", "HEAD")
	if err := db.UpsertPullRequest(number, repo, newHead, "", "diff"); err != nil {
		t.Fatal(err)
	}

	SyncPRWorktree(db, owner, repo, number)
	if got := runGitOrFail(t, path, "rev-parse", "HEAD"); got != newHead {
		t.Fatalf("worktree HEAD = %s after SyncPRWorktree, want %s", got, newHead)
	}
}

// A Delete means one workflow stopped claiming the PR, not that the PR is
// gone: the worktree stays while any other section still lists it.
func TestHandleWorktreeChangeKeepsWorktreeWhileClaimedElsewhere(t *testing.T) {
	const (
		owner      = "C-Hipple"
		repo       = "code-review-server"
		number     = 7
		identifier = owner + "/" + repo + "-7"
	)
	location, _ := setupPRRepo(t, repo)
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db, RepoLocation: location, AutoWorktree: true})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	if err := db.UpsertPullRequest(number, repo, "sha1", "", "diff"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPRMetadataCache(owner, repo, number, `{"head_ref":"feature/thing"}`); err != nil {
		t.Fatal(err)
	}

	needsReview := seedItem(t, db, "Needs Review", identifier, "needs_review")
	seedItem(t, db, "Waiting on Author", identifier, "waiting_on_author")
	handleWorktreeChange(db, SerializedFileChange{FileChange: &FileChanges{
		ChangeType: "Addition", Identifier: identifier,
		SectionID: needsReview.ID, WorkflowName: "needs_review",
	}})
	path, _ := db.GetWorktree(number, repo, owner)
	if path == "" {
		t.Fatal("no worktree created")
	}

	release := SerializedFileChange{FileChange: &FileChanges{
		ChangeType: "Delete", Identifier: identifier,
		SectionID: needsReview.ID, WorkflowName: "needs_review",
	}}
	handleWorktreeChange(db, release)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree removed while Waiting on Author still lists the PR: %v", err)
	}

	// Once nothing else claims it, the release removes the worktree.
	if err := db.RemoveWorkflowFromItem(needsReview.ID, identifier, "needs_review"); err != nil {
		t.Fatal(err)
	}
	waiting, _ := db.GetSection("Waiting on Author")
	if err := db.RemoveWorkflowFromItem(waiting.ID, identifier, "waiting_on_author"); err != nil {
		t.Fatal(err)
	}
	handleWorktreeChange(db, release)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("worktree still on disk after the last claim was released (err=%v)", err)
	}
	if got, _ := db.GetWorktree(number, repo, owner); got != "" {
		t.Fatalf("worktree record %q left behind", got)
	}
}
