package git_tools

import (
	"os"
	"path/filepath"
	"testing"
)

// gitOrFail runs git in dir and fails the test on error.
func gitOrFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := runGit(dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return out
}

// setupOriginAndClone builds a bare origin with a main branch and a
// feature/thing branch, then clones only main locally — mirroring a repo
// where the PR branch was never fetched or checked out.
func setupOriginAndClone(t *testing.T) (originDir, localDir string) {
	t.Helper()
	if _, err := runGit(t.TempDir(), "version"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()

	originDir = filepath.Join(base, "origin.git")
	gitOrFail(t, base, "init", "--bare", "-b", "main", originDir)

	seed := filepath.Join(base, "seed")
	gitOrFail(t, base, "clone", originDir, seed)
	gitOrFail(t, seed, "config", "user.email", "test@test")
	gitOrFail(t, seed, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(seed, "f.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitOrFail(t, seed, "add", ".")
	gitOrFail(t, seed, "commit", "-m", "init")
	gitOrFail(t, seed, "push", "origin", "HEAD:main")
	gitOrFail(t, seed, "checkout", "-b", "feature/thing")
	if err := os.WriteFile(filepath.Join(seed, "f.txt"), []byte("a\nb\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitOrFail(t, seed, "commit", "-am", "feat")
	gitOrFail(t, seed, "push", "origin", "feature/thing")

	localDir = filepath.Join(base, "local")
	gitOrFail(t, base, "clone", "--branch", "main", "--single-branch", originDir, localDir)
	gitOrFail(t, localDir, "config", "user.email", "test@test")
	gitOrFail(t, localDir, "config", "user.name", "test")
	return originDir, localDir
}

func headOf(t *testing.T, dir, ref string) string {
	t.Helper()
	return gitOrFail(t, dir, "rev-parse", ref)
}

func TestCreateWorktreeFetchesUnknownBranch(t *testing.T) {
	_, local := setupOriginAndClone(t)

	// The single-branch clone has no knowledge of feature/thing at all.
	wt := filepath.Join(filepath.Dir(local), "wt_feature")
	if err := CreateWorktree(local, "feature/thing", wt); err != nil {
		t.Fatalf("CreateWorktree failed for unfetched branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "f.txt")); err != nil {
		t.Fatalf("worktree missing checked out file: %v", err)
	}
	if headOf(t, wt, "HEAD") != headOf(t, local, "origin/feature/thing") {
		t.Fatal("worktree is not at the PR head")
	}
}

func TestCreateWorktreeBranchCheckedOutInMainRepo(t *testing.T) {
	_, local := setupOriginAndClone(t)

	// Check the PR branch out in the main repo; a plain worktree add of the
	// same branch fails, so we expect the detached fallback to kick in.
	gitOrFail(t, local, "fetch", "origin", "+refs/heads/feature/thing:refs/remotes/origin/feature/thing")
	gitOrFail(t, local, "checkout", "-b", "feature/thing", "origin/feature/thing")

	wt := filepath.Join(filepath.Dir(local), "wt_checked_out")
	if err := CreateWorktree(local, "feature/thing", wt); err != nil {
		t.Fatalf("CreateWorktree failed when branch is checked out elsewhere: %v", err)
	}
	if headOf(t, wt, "HEAD") != headOf(t, local, "origin/feature/thing") {
		t.Fatal("detached worktree is not at the PR head")
	}
}

func TestUpdateWorktreeFollowsNewCommits(t *testing.T) {
	origin, local := setupOriginAndClone(t)

	wt := filepath.Join(filepath.Dir(local), "wt_update")
	if err := CreateWorktree(local, "feature/thing", wt); err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Push a new commit to the PR branch from another clone.
	pusher := filepath.Join(filepath.Dir(local), "pusher")
	gitOrFail(t, filepath.Dir(local), "clone", "--branch", "feature/thing", origin, pusher)
	gitOrFail(t, pusher, "config", "user.email", "test@test")
	gitOrFail(t, pusher, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(pusher, "f.txt"), []byte("a\nb\nc\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitOrFail(t, pusher, "commit", "-am", "more")
	gitOrFail(t, pusher, "push", "origin", "feature/thing")

	if err := UpdateWorktree(local, "feature/thing", wt); err != nil {
		t.Fatalf("UpdateWorktree failed: %v", err)
	}
	if headOf(t, wt, "HEAD") != headOf(t, pusher, "HEAD") {
		t.Fatal("worktree was not updated to the new PR head")
	}
}

func TestUpdateWorktreeLeavesDirtyWorktreeAlone(t *testing.T) {
	_, local := setupOriginAndClone(t)

	wt := filepath.Join(filepath.Dir(local), "wt_dirty")
	if err := CreateWorktree(local, "feature/thing", wt); err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	before := headOf(t, wt, "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("local edit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateWorktree(local, "feature/thing", wt); err != nil {
		t.Fatalf("UpdateWorktree failed: %v", err)
	}
	if headOf(t, wt, "HEAD") != before {
		t.Fatal("dirty worktree should not have been moved")
	}
	content, err := os.ReadFile(filepath.Join(wt, "f.txt"))
	if err != nil || string(content) != "local edit\n" {
		t.Fatalf("local edit was clobbered: %q err=%v", content, err)
	}
}
