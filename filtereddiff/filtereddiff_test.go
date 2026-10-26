package filtereddiff

import (
	"errors"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func commits() []Commit {
	return []Commit{{SHA: "c1", At: at(0)}, {SHA: "c2", At: at(10)}, {SHA: "c3", At: at(20)}, {SHA: "c4", At: at(30)}}
}

func baseline(t *testing.T, id string, in Inputs) (Baseline, error) {
	t.Helper()
	f, ok := Lookup(id)
	if !ok {
		t.Fatalf("filter %q is not registered", id)
	}
	return f.Baseline(in)
}

func TestSinceLastReviewUsesTheLatestSubmittedReviewsCommit(t *testing.T) {
	in := Inputs{
		Me:      "me",
		HeadSHA: "c4",
		Commits: commits(),
		Reviews: []Review{
			{ID: 1, User: "me", State: "COMMENTED", SubmittedAt: at(5), CommitID: "c1"},
			{ID: 2, User: "Me", State: "APPROVED", SubmittedAt: at(15), CommitID: "c2"},
			{ID: 3, User: "someone", State: "APPROVED", SubmittedAt: at(25), CommitID: "c3"},
			// A draft GitHub holds unsubmitted is not a review yet.
			{ID: 4, User: "me", State: "PENDING", CommitID: "c4"},
		},
	}
	b, err := baseline(t, SinceLastReview, in)
	if err != nil {
		t.Fatal(err)
	}
	if b.SHA != "c2" || b.Kind != KindReview || b.Inferred {
		t.Fatalf("baseline = %+v, want review 2 at c2", b)
	}
	if got := in.CommitsAfter(b.SHA); len(got) != 2 || got[0] != "c3" || got[1] != "c4" {
		t.Fatalf("CommitsAfter(c2) = %v, want [c3 c4]", got)
	}
}

func TestSinceLastReviewInfersTheCommitForAReviewCachedWithoutOne(t *testing.T) {
	in := Inputs{
		Me:      "me",
		Commits: commits(),
		Reviews: []Review{{ID: 1, User: "me", State: "APPROVED", SubmittedAt: at(25)}},
	}
	b, err := baseline(t, SinceLastReview, in)
	if err != nil {
		t.Fatal(err)
	}
	if b.SHA != "c3" || !b.Inferred {
		t.Fatalf("baseline = %+v, want c3 inferred from the review's time", b)
	}
}

func TestSinceLastReviewWithoutAReviewHasNoBaseline(t *testing.T) {
	in := Inputs{
		Me:       "me",
		Commits:  commits(),
		Reviews:  []Review{{ID: 1, User: "someone", State: "APPROVED", SubmittedAt: at(25), CommitID: "c3"}},
		Comments: []Comment{{ID: 7, Author: "me", CreatedAt: at(26), CommitID: "c3"}},
	}
	_, err := baseline(t, SinceLastReview, in)
	var none *NoBaselineError
	if !errors.As(err, &none) {
		t.Fatalf("err = %v, want a NoBaselineError", err)
	}
}

func TestNoLoginHasNoBaseline(t *testing.T) {
	in := Inputs{Commits: commits(), Reviews: []Review{{ID: 1, User: "", State: "APPROVED", SubmittedAt: at(25), CommitID: "c3"}}}
	for _, id := range []string{SinceLastReview, SinceLastInteraction} {
		_, err := baseline(t, id, in)
		var none *NoBaselineError
		if !errors.As(err, &none) {
			t.Fatalf("%s: err = %v, want a NoBaselineError", id, err)
		}
	}
}

func TestSinceLastInteractionTakesTheNewestReviewOrComment(t *testing.T) {
	base := Inputs{
		Me:      "me",
		Commits: commits(),
		Reviews: []Review{{ID: 1, User: "me", State: "COMMENTED", SubmittedAt: at(12), CommitID: "c2"}},
	}

	t.Run("a later review comment", func(t *testing.T) {
		in := base
		in.Comments = []Comment{{ID: 5, Author: "me", CreatedAt: at(22), CommitID: "c3"}}
		b, err := baseline(t, SinceLastInteraction, in)
		if err != nil {
			t.Fatal(err)
		}
		if b.SHA != "c3" || b.Kind != KindReviewComment || b.Inferred {
			t.Fatalf("baseline = %+v, want the review comment at c3", b)
		}
	})

	t.Run("a later conversation comment", func(t *testing.T) {
		in := base
		in.Comments = []Comment{{ID: 6, Author: "me", CreatedAt: at(35)}}
		b, err := baseline(t, SinceLastInteraction, in)
		if err != nil {
			t.Fatal(err)
		}
		if b.SHA != "c4" || b.Kind != KindComment || !b.Inferred {
			t.Fatalf("baseline = %+v, want c4 inferred from the comment's time", b)
		}
	})

	t.Run("the review is newest", func(t *testing.T) {
		in := base
		in.Comments = []Comment{
			{ID: 8, Author: "me", CreatedAt: at(3), CommitID: "c1"},
			{ID: 9, Author: "someone", CreatedAt: at(40), CommitID: "c4"},
		}
		b, err := baseline(t, SinceLastInteraction, in)
		if err != nil {
			t.Fatal(err)
		}
		if b.SHA != "c2" || b.Kind != KindReview {
			t.Fatalf("baseline = %+v, want the review at c2", b)
		}
	})

	t.Run("nothing by me", func(t *testing.T) {
		in := Inputs{Me: "me", Commits: commits(), Comments: []Comment{{ID: 9, Author: "someone", CreatedAt: at(40)}}}
		_, err := baseline(t, SinceLastInteraction, in)
		var none *NoBaselineError
		if !errors.As(err, &none) {
			t.Fatalf("err = %v, want a NoBaselineError", err)
		}
	})
}

func TestInputsFromCaches(t *testing.T) {
	reviews := `[{"id":1,"user":"me","body":"","state":"APPROVED","submitted_at":"2026-10-01T12:15:00Z","html_url":"","commit_id":"c2"}]`
	comments := `[
		{"id":5,"user":{"login":"me"},"created_at":"2026-10-01T12:22:00Z","commit_id":"c4","original_commit_id":"c3","url":"https://api.github.com/repos/o/r/pulls/comments/5"},
		{"id":6,"user":{"login":"me"},"created_at":"2026-10-01T12:35:00Z","url":"https://api.github.com/repos/o/r/issues/comments/6"}
	]`
	commitsJSON := `[
		{"sha":"c1","date":"2026-10-01T12:00:00Z"},
		{"sha":"c2","date":"2026-09-01T00:00:00Z","committed_at":"2026-10-01T12:10:00Z"}
	]`
	in, err := InputsFromCaches("me", "c2", reviews, comments, commitsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Reviews) != 1 || in.Reviews[0].CommitID != "c2" || !in.Reviews[0].SubmittedAt.Equal(at(15)) {
		t.Fatalf("reviews = %+v", in.Reviews)
	}
	if len(in.Comments) != 2 || in.Comments[0].CommitID != "c3" || in.Comments[1].CommitID != "" || in.Comments[1].Author != "me" {
		t.Fatalf("comments = %+v, want the original commit for the review comment and none for the conversation one", in.Comments)
	}
	if len(in.Commits) != 2 || !in.Commits[1].At.Equal(at(10)) {
		t.Fatalf("commits = %+v, want committed_at preferred over the authored date", in.Commits)
	}
	if !in.ContainsCommit("c1") || in.ContainsCommit("c9") || in.ContainsCommit("") {
		t.Fatal("ContainsCommit disagrees with the cached commit list")
	}

	if _, err := InputsFromCaches("me", "c2", "{not json", "", ""); err == nil {
		t.Fatal("a reviews row that doesn't decode should be an error")
	}
	empty, err := InputsFromCaches("me", "c2", "", "", "")
	if err != nil || len(empty.Reviews)+len(empty.Comments)+len(empty.Commits) != 0 {
		t.Fatalf("empty rows = %+v, %v, want nothing and no error", empty, err)
	}
}

type fakeFilter struct{ id string }

func (f fakeFilter) ID() string                        { return f.id }
func (f fakeFilter) Name() string                      { return "Fake" }
func (f fakeFilter) Description() string               { return "" }
func (f fakeFilter) Baseline(Inputs) (Baseline, error) { return Baseline{SHA: "x"}, nil }

func TestRegistry(t *testing.T) {
	infos := Describe()
	if len(infos) < 2 || infos[0].ID != SinceLastReview || infos[1].ID != SinceLastInteraction {
		t.Fatalf("Describe() = %+v, want the built-ins first, in order", infos)
	}

	before := len(Filters())
	Register(fakeFilter{id: "fake-filter"})
	t.Cleanup(func() {
		registryMu.Lock()
		defer registryMu.Unlock()
		registry = registry[:before]
	})
	if _, ok := Lookup("fake-filter"); !ok || len(Filters()) != before+1 {
		t.Fatal("a registered filter should be listed and looked up")
	}
	Register(fakeFilter{id: "fake-filter"})
	if len(Filters()) != before+1 {
		t.Fatal("registering an ID again should replace, not add")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("Lookup found a filter that was never registered")
	}
}
