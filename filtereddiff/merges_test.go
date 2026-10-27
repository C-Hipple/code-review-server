package filtereddiff

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestMergedAfterFindsTheBaseCommitsAMergeBroughtIn(t *testing.T) {
	// c1 <- c2 (baseline) <- c3 <- m (merges main's x2) <- c4, with c1 forked
	// from main's x1.
	pr := []Commit{
		{SHA: "c1", Parents: []string{"x1"}},
		{SHA: "c2", Parents: []string{"c1"}},
		{SHA: "c3", Parents: []string{"c2"}},
		{SHA: "m", Parents: []string{"c3", "x2"}},
		{SHA: "c4", Parents: []string{"m"}},
	}
	in := Inputs{HeadSHA: "c4", Commits: pr}

	if merged, known := in.MergedAfter("c2"); !known || len(merged) != 1 || merged[0] != "x2" {
		t.Fatalf("MergedAfter(c2) = %v, %v; want [x2] (the fork point x1 is before the baseline)", merged, known)
	}
	if merged, known := in.MergedAfter("m"); !known || len(merged) != 0 {
		t.Fatalf("MergedAfter(m) = %v, %v; want nothing merged since the merge", merged, known)
	}

	// A branch of the PR's own merged in is the PR's work: its commits are
	// the PR's, so only its fork point from main counts, and only when the
	// baseline doesn't reach it.
	side := append([]Commit{}, pr[:2]...)
	side = append(side,
		Commit{SHA: "s1", Parents: []string{"c1"}},
		Commit{SHA: "m2", Parents: []string{"c2", "s1"}},
	)
	if merged, known := (Inputs{HeadSHA: "m2", Commits: side}).MergedAfter("c2"); !known || len(merged) != 0 {
		t.Fatalf("MergedAfter with a merged side branch = %v, %v; want nothing from outside", merged, known)
	}

	for name, in := range map[string]Inputs{
		"commits cached without parents": {HeadSHA: "c4", Commits: []Commit{{SHA: "c2", Parents: []string{"c1"}}, {SHA: "c4"}}},
		"baseline not among the commits": {HeadSHA: "c4", Commits: pr[2:]},
		"head not among the commits":     {HeadSHA: "c5", Commits: pr},
	} {
		if _, known := in.MergedAfter("c2"); known {
			t.Errorf("%s: MergedAfter claims to know", name)
		}
	}
}

// numbered returns lines "line from" to "line to".
func numbered(from, to int) []string {
	var lines []string
	for i := from; i <= to; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	return lines
}

func file(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// with returns lines with line n (1-based) replaced by repl — none to delete
// it, several to insert.
func with(lines []string, n int, repl ...string) []string {
	out := append([]string{}, lines[:n-1]...)
	out = append(out, repl...)
	return append(out, lines[n:]...)
}

const removed = "\x00"

type mergeScenario struct {
	name string
	// base is the base branch where the PR forks from it.
	base map[string]string
	// before is what the PR's commits up to the baseline write; between what
	// it commits after the baseline but before merging the base branch in.
	before, between map[string]string
	// main is what the base branch commits meanwhile, which the PR merges.
	main map[string]string
	// after is what the PR commits after the merge.
	after map[string]string
	// main2 and after2, when set, are a second round: the base branch
	// commits again, the PR merges it again and commits after2.
	main2, after2 map[string]string
}

func TestWithoutMergedLeavesOutWhatMergingTheBaseBranchBrought(t *testing.T) {
	a := numbered(1, 40)
	scenarios := []mergeScenario{
		{
			name:   "base branch changes other files",
			base:   map[string]string{"a.txt": file(a...), "b.txt": file(numbered(1, 10)...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"b.txt": file(with(numbered(1, 10), 5, "main 5")...), "c.txt": "new on main\n"},
			after:  map[string]string{"a.txt": file(with(with(a, 3, "pr 3"), 20, "pr 20")...)},
		},
		{
			name:   "base branch changes the PR's file elsewhere",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"a.txt": file(with(a, 30, "main 30")...)},
			after:  map[string]string{"a.txt": file(with(with(with(a, 3, "pr 3"), 30, "main 30"), 12, "pr 12")...)},
		},
		{
			name:   "base branch changes the line above the PR's change",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 30, "pr 30")...)},
			main:   map[string]string{"a.txt": file(with(a, 5, "main 5")...)},
			after:  map[string]string{"a.txt": file(with(with(with(a, 30, "pr 30"), 5, "main 5"), 6, "pr 6")...)},
		},
		{
			name:   "base branch changes the line below the PR's change",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 30, "pr 30")...)},
			main:   map[string]string{"a.txt": file(with(a, 6, "main 6")...)},
			after:  map[string]string{"a.txt": file(with(with(with(a, 30, "pr 30"), 6, "main 6"), 5, "pr 5")...)},
		},
		{
			name:   "base branch removes lines next to the PR's removal",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 30, "pr 30")...)},
			main:   map[string]string{"a.txt": file(with(with(a, 9), 8)...)},
			after:  map[string]string{"a.txt": file(with(with(with(with(a, 30, "pr 30"), 10), 9), 8)...)},
		},
		{
			name:   "PR rewords a line it added before the baseline",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3, "line 3", "pr added")...)},
			main:   map[string]string{"a.txt": file(with(a, 25, "main 25")...)},
			after:  map[string]string{"a.txt": file(with(with(a, 25, "main 25"), 3, "line 3", "pr reworded")...)},
		},
		{
			name:   "PR removes a line it added before the baseline",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3, "line 3", "TODO: remove")...)},
			main:   map[string]string{"a.txt": file(with(a, 25, "main 25")...)},
			after:  map[string]string{"a.txt": file(with(a, 25, "main 25")...)},
		},
		{
			name:   "PR puts back a line it removed before the baseline",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3)...)},
			main:   map[string]string{"a.txt": file(with(a, 25, "main 25")...)},
			after:  map[string]string{"a.txt": file(with(a, 25, "main 25")...)},
		},
		{
			name:   "base branch adds a file the PR then changes",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"d.txt": file(numbered(1, 20)...)},
			after:  map[string]string{"d.txt": file(with(numbered(1, 20), 14, "pr 14")...)},
		},
		{
			name:   "base branch renames a file the PR then changes",
			base:   map[string]string{"a.txt": file(a...), "old.txt": file(numbered(1, 20)...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"old.txt": removed, "new.txt": file(numbered(1, 20)...)},
			after:  map[string]string{"new.txt": file(with(numbered(1, 20), 7, "pr 7")...)},
		},
		{
			name:   "base branch deletes a file",
			base:   map[string]string{"a.txt": file(a...), "e.txt": file(numbered(1, 5)...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"e.txt": removed},
			after:  map[string]string{"a.txt": file(with(with(a, 3, "pr 3"), 33, "pr 33")...)},
		},
		{
			name:    "PR commits on both sides of the merge",
			base:    map[string]string{"a.txt": file(a...), "b.txt": file(numbered(1, 10)...)},
			before:  map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			between: map[string]string{"a.txt": file(with(with(a, 3, "pr 3"), 15, "pr 15")...)},
			main:    map[string]string{"b.txt": file(with(numbered(1, 10), 2, "main 2")...)},
			after:   map[string]string{"a.txt": file(with(with(with(a, 3, "pr 3"), 15, "pr 15"), 35, "pr 35")...)},
		},
		{
			name:   "PR renames a file after the merge",
			base:   map[string]string{"a.txt": file(a...), "b.txt": file(numbered(1, 10)...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"b.txt": file(with(numbered(1, 10), 2, "main 2")...)},
			after:  map[string]string{"a.txt": removed, "z.txt": file(with(with(a, 3, "pr 3"), 22, "pr 22")...)},
		},
		{
			name:   "PR changes a line the merge brought in",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"a.txt": file(with(a, 20, "line 20", "main added", "main added 2")...)},
			after:  map[string]string{"a.txt": file(with(with(a, 3, "pr 3"), 20, "line 20", "pr changed main's line", "main added 2")...)},
		},
		{
			name:   "PR removes lines the merge brought in",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"a.txt": file(with(a, 20, "line 20", "main added", "main added 2")...)},
			after:  map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
		},
		{
			name:   "PR undoes the base branch's change to a file it doesn't otherwise touch",
			base:   map[string]string{"a.txt": file(a...), "b.txt": file(numbered(1, 10)...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"b.txt": file(with(numbered(1, 10), 5, "main 5")...)},
			after:  map[string]string{"b.txt": file(numbered(1, 10)...)},
		},
		{
			name:   "PR keeps a line the base branch removed",
			base:   map[string]string{"a.txt": file(a...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"a.txt": file(with(a, 20)...)},
			after:  map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
		},
		{
			name:   "PR deletes a file the base branch changed",
			base:   map[string]string{"a.txt": file(a...), "f.txt": file(numbered(1, 8)...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...), "f.txt": file(with(numbered(1, 8), 6, "line 6", "pr added")...)},
			main:   map[string]string{"f.txt": file(with(numbered(1, 8), 2, "main 2")...)},
			after:  map[string]string{"f.txt": removed},
		},
		{
			name:   "base branch merged in twice",
			base:   map[string]string{"a.txt": file(a...), "b.txt": file(numbered(1, 10)...)},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"b.txt": file(with(numbered(1, 10), 2, "main 2")...)},
			after:  map[string]string{"a.txt": file(with(with(a, 3, "pr 3"), 15, "pr 15")...)},
			main2:  map[string]string{"a.txt": file(with(a, 35, "main 35")...)},
			after2: map[string]string{"a.txt": file(with(with(with(a, 3, "pr 3"), 15, "pr 15"), 35, "main 35", "pr after 35")...)},
		},
		{
			name:   "base branch appends to a file without a final newline",
			base:   map[string]string{"a.txt": file(a...), "n.txt": "x\ny"},
			before: map[string]string{"a.txt": file(with(a, 3, "pr 3")...)},
			main:   map[string]string{"n.txt": "x\ny\nz"},
			after:  map[string]string{"n.txt": "X\ny\nz"},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			r := newGitRepo(t)
			r.commit(sc.base)
			m1 := r.head()
			r.git("checkout", "-q", "-b", "pr")
			r.commit(sc.before)
			b := r.head()
			r.git("checkout", "-q", "main")
			r.commit(sc.main)
			m2 := r.head()
			r.git("checkout", "-q", "pr")
			if sc.between != nil {
				r.commit(sc.between)
			}
			r.git("merge", "-q", "--no-edit", "main")
			r.commit(sc.after)
			if sc.main2 != nil {
				r.git("checkout", "-q", "main")
				r.commit(sc.main2)
				m2 = r.head()
				r.git("checkout", "-q", "pr")
				r.git("merge", "-q", "--no-edit", "main")
				r.commit(sc.after2)
			}
			h := r.head()
			// What the PR changed since the baseline: from the baseline with
			// the same merge to the head.
			r.git("checkout", "-q", "-b", "x", b)
			r.git("merge", "-q", "--no-edit", m2)
			x := r.head()

			rangeDiff := r.diff(b, h)
			got := WithoutMerged(rangeDiff, r.diff(m1, b), r.diff(m2, h))
			want := r.diff(x, h)
			if normalizeDiff(got) != normalizeDiff(want) {
				t.Fatalf("without the merge:\n%s\nwant:\n%s\nfrom the range diff:\n%s", got, want, rangeDiff)
			}
		})
	}
}

// The PR's own diffs disagree on nothing without a merge, so narrowing a
// range diff with no base branch changes in it changes nothing.
func TestWithoutMergedKeepsAPRsOwnHistory(t *testing.T) {
	a := numbered(1, 40)
	r := newGitRepo(t)
	r.commit(map[string]string{"a.txt": file(a...), "b.txt": "b\n"})
	m1 := r.head()
	r.git("checkout", "-q", "-b", "pr")
	r.commit(map[string]string{"a.txt": file(with(with(a, 30), 3, "line 3", "pr added")...), "bin.dat": "\x00\x01"})
	b := r.head()
	r.commit(map[string]string{
		"a.txt":   file(with(with(a, 12, "pr 12"), 3, "line 3", "pr reworded")...),
		"b.txt":   removed,
		"c.txt":   "new\n",
		"bin.dat": "\x00\x02",
	})
	h := r.head()

	rangeDiff := r.diff(b, h)
	if got := WithoutMerged(rangeDiff, r.diff(m1, b), r.diff(m1, h)); normalizeDiff(got) != normalizeDiff(rangeDiff) {
		t.Fatalf("narrowed:\n%s\nwant the range diff:\n%s", got, rangeDiff)
	}
}

func TestWithoutMergedPassesAnUnreadableDiffThrough(t *testing.T) {
	if got := WithoutMerged("", "", ""); got != "" {
		t.Fatalf("empty diff = %q", got)
	}
	if got := WithoutMerged("not a diff", "", ""); got != "not a diff" {
		t.Fatalf("unreadable diff = %q", got)
	}
}

var hunkSection = regexp.MustCompile(`(?m)^(@@ [^@]* @@).*$`)

// normalizeDiff drops what differs between two diffs of the same change from
// different commits: blob hashes, rename similarity and the function names
// git puts after a hunk's ranges.
func normalizeDiff(d string) string {
	var keep []string
	for _, l := range strings.Split(d, "\n") {
		if strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "similarity index ") {
			continue
		}
		keep = append(keep, l)
	}
	return hunkSection.ReplaceAllString(strings.Join(keep, "\n"), "$1")
}

type gitRepo struct {
	t   *testing.T
	dir string
}

func newGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	r := &gitRepo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
		"-c", "diff.renames=true", "-c", "diff.algorithm=myers", "-c", "core.autocrlf=false",
	}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// commit writes files (removed deletes one) and commits them.
func (r *gitRepo) commit(files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		path := filepath.Join(r.dir, name)
		if content == removed {
			r.git("rm", "-q", name)
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
		r.git("add", name)
	}
	r.git("commit", "-q", "--allow-empty", "-m", "change")
}

func (r *gitRepo) head() string { return strings.TrimSpace(r.git("rev-parse", "HEAD")) }

// diff is the unified diff from one commit to another, as GitHub's compare
// serves it.
func (r *gitRepo) diff(from, to string) string {
	return r.git("diff", "--no-color", "--no-ext-diff", from, to)
}

// Random histories, checked against git's own diff from the baseline merged
// with the same base commits. The PR's edits before the baseline and the base
// branch's stay apart so the merge is clean; the PR's edits after it go
// anywhere, the merge's lines included. CRS_MERGE_FUZZ sets how many.
func TestWithoutMergedMatchesGitOnRandomHistories(t *testing.T) {
	runs := 25
	if n, err := strconv.Atoi(os.Getenv("CRS_MERGE_FUZZ")); err == nil {
		runs = n
	}
	rng := rand.New(rand.NewPCG(1, 2))
	unique := 0
	edit := func(lines []string, from, to, edits int) []string {
		for range edits {
			n := from + rng.IntN(max(min(to, len(lines))-from, 1))
			if n < 1 || n > len(lines) {
				continue
			}
			unique++
			switch rng.IntN(3) {
			case 0:
				lines = with(lines, n, fmt.Sprintf("changed %d", unique))
			case 1:
				lines = with(lines, n, lines[n-1], fmt.Sprintf("added %d", unique))
			default:
				lines = with(lines, n)
			}
		}
		return lines
	}
	for run := range runs {
		base := numbered(1, 60)
		pr := edit(base, 1, 18, 1+rng.IntN(4))
		// The base branch edits the far end, which the PR's edits above
		// leave where it was.
		main := append(append([]string{}, base[:30]...), edit(base[30:], 2, 30, 1+rng.IntN(5))...)

		r := newGitRepo(t)
		r.commit(map[string]string{"a.txt": file(base...)})
		m1 := r.head()
		r.git("checkout", "-q", "-b", "pr")
		r.commit(map[string]string{"a.txt": file(pr...)})
		b := r.head()
		r.git("checkout", "-q", "main")
		r.commit(map[string]string{"a.txt": file(main...)})
		m2 := r.head()
		r.git("checkout", "-q", "pr")
		r.git("merge", "-q", "--no-edit", "main")
		merged := strings.Split(strings.TrimSuffix(r.git("show", "HEAD:a.txt"), "\n"), "\n")
		r.commit(map[string]string{"a.txt": file(edit(merged, 1, len(merged)+1, 1+rng.IntN(5))...)})
		h := r.head()
		r.git("checkout", "-q", "-b", "x", b)
		r.git("merge", "-q", "--no-edit", m2)
		x := r.head()

		rangeDiff := r.diff(b, h)
		got := WithoutMerged(rangeDiff, r.diff(m1, b), r.diff(m2, h))
		if want := r.diff(x, h); normalizeDiff(got) != normalizeDiff(want) {
			t.Fatalf("run %d: without the merge:\n%s\nwant:\n%s\nfrom the range diff:\n%s\nbaseline diff:\n%s\nhead diff:\n%s",
				run, got, want, rangeDiff, r.diff(m1, b), r.diff(m2, h))
		}
	}
}
