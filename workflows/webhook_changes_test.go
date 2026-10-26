package workflows

import (
	"slices"
	"testing"

	"github.com/google/go-github/v74/github"
)

// recordPRChanges registers a PR-changed hook that collects what it is told.
func recordPRChanges(t *testing.T) *[]PRChange {
	t.Helper()
	var got []PRChange
	SetPRChangedHook(func(c PRChange) { got = append(got, c) })
	t.Cleanup(func() { SetPRChangedHook(nil) })
	return &got
}

func user(login string) *github.User { return &github.User{Login: github.Ptr(login)} }

func TestDeliveriesAnnounceWhatTheyChanged(t *testing.T) {
	key := whKey(1)
	merged := whPR(1, "head1")
	merged.State, merged.Merged = github.Ptr("closed"), github.Ptr(true)

	for _, c := range []struct {
		name        string
		eventType   string
		event       any
		wantEvent   string
		wantChanges []string // each must be among those reported
		wantSummary string
		wantHead    string
	}{
		{
			name:      "an approval",
			eventType: "pull_request_review",
			event: &github.PullRequestReviewEvent{
				Action:      github.Ptr("submitted"),
				Review:      &github.PullRequestReview{ID: github.Ptr(int64(7)), User: user("rev"), State: github.Ptr("approved")},
				PullRequest: whPR(1, "head1"),
				Repo:        whRepository(),
				Sender:      user("rev"),
			},
			wantEvent:   "pull_request_review.submitted",
			wantChanges: []string{"reviews", "metadata"},
			wantSummary: "rev approved",
			wantHead:    "head1",
		},
		{
			name:      "a review comment",
			eventType: "pull_request_review_comment",
			event: &github.PullRequestReviewCommentEvent{
				Action: github.Ptr("created"),
				Comment: &github.PullRequestComment{
					ID: github.Ptr(int64(10)), Path: github.Ptr("w.go"), Body: github.Ptr("why?"), User: user("rev"),
				},
				PullRequest: whPR(1, "head1"),
				Repo:        whRepository(),
				Sender:      user("rev"),
			},
			wantEvent:   "pull_request_review_comment.created",
			wantChanges: []string{"comments"},
			wantSummary: "rev commented on w.go",
			wantHead:    "head1",
		},
		{
			name:      "a conversation comment",
			eventType: "issue_comment",
			event: &github.IssueCommentEvent{
				Action:  github.Ptr("created"),
				Issue:   &github.Issue{Number: github.Ptr(1), PullRequestLinks: &github.PullRequestLinks{}},
				Comment: &github.IssueComment{ID: github.Ptr(int64(11)), Body: github.Ptr("lgtm"), User: user("rev")},
				Repo:    whRepository(),
				Sender:  user("rev"),
			},
			wantEvent:   "issue_comment.created",
			wantChanges: []string{"comments"},
			wantSummary: "rev commented",
		},
		{
			name:        "a merge",
			eventType:   "pull_request",
			event:       &github.PullRequestEvent{Action: github.Ptr("closed"), PullRequest: merged, Repo: whRepository(), Sender: user("lead")},
			wantEvent:   "pull_request.closed",
			wantChanges: []string{ChangePR, "metadata"},
			wantSummary: "lead merged the PR",
			wantHead:    "head1",
		},
		{
			name:        "a push",
			eventType:   "pull_request",
			event:       &github.PullRequestEvent{Action: github.Ptr("synchronize"), PullRequest: whPR(1, "head2"), Repo: whRepository(), Sender: user("author")},
			wantEvent:   "pull_request.synchronize",
			wantChanges: []string{ChangePR, ChangeHead},
			wantSummary: "author pushed new commits",
			wantHead:    "head2",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newWebhookTestEnv(t)
			seedCachedPR(t, env.db, key, "head1")
			trackItem(t, env.db, key)
			got := recordPRChanges(t)

			applyWebhookEvent(webhookDelivery{eventType: c.eventType, id: "abc", event: c.event})

			if len(*got) != 1 {
				t.Fatalf("announced %d changes, want 1: %+v", len(*got), *got)
			}
			change := (*got)[0]
			if change.Key != key || change.Event != c.wantEvent || change.Summary != c.wantSummary || change.HeadSHA != c.wantHead {
				t.Errorf("change = %+v\nwant key %v, event %q, summary %q, head %q", change, key, c.wantEvent, c.wantSummary, c.wantHead)
			}
			for _, want := range c.wantChanges {
				if !slices.Contains(change.Changes, want) {
					t.Errorf("changes = %v, want %q among them", change.Changes, want)
				}
			}
			if change.Actor == "" {
				t.Error("no actor for a delivery with a sender")
			}
		})
	}
}

func TestDeliveriesThatChangeNothingAreNotAnnounced(t *testing.T) {
	env := newWebhookTestEnv(t)
	got := recordPRChanges(t)
	review := func(number int) *github.PullRequestReviewEvent {
		return &github.PullRequestReviewEvent{
			Action:      github.Ptr("submitted"),
			Review:      &github.PullRequestReview{ID: github.Ptr(int64(1)), User: user("rev"), State: github.Ptr("approved")},
			PullRequest: whPR(number, "head1"),
			Repo:        whRepository(),
		}
	}

	// A PR nothing holds.
	applyWebhookEvent(webhookDelivery{eventType: "pull_request_review", event: review(5)})
	// A PR held with no reviews cached to patch.
	key := whKey(1)
	trackItem(t, env.db, key)
	env.db.UpsertPullRequest(key.Number, key.Repo, "head1", "", "")
	applyWebhookEvent(webhookDelivery{eventType: "pull_request_review", event: review(1)})

	if len(*got) != 0 {
		t.Errorf("announced %+v for deliveries that changed nothing", *got)
	}
}

func TestCIIsAnnouncedWhenItSettles(t *testing.T) {
	env := newWebhookTestEnv(t)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	got := recordPRChanges(t)
	app := &github.App{ID: github.Ptr(int64(1))}
	deliver := func(id int64, name, status, conclusion string) PRChange {
		t.Helper()
		run := &github.CheckRun{ID: github.Ptr(id), Name: github.Ptr(name), HeadSHA: github.Ptr("head1"), App: app,
			Status: github.Ptr(status), PullRequests: []*github.PullRequest{{Number: github.Ptr(1)}}}
		if conclusion != "" {
			run.Conclusion = github.Ptr(conclusion)
		}
		*got = nil
		applyWebhookEvent(webhookDelivery{eventType: "check_run", event: &github.CheckRunEvent{
			Action: github.Ptr(status), CheckRun: run, Repo: whRepository(), Sender: user("github-actions[bot]"),
		}})
		if len(*got) != 1 {
			t.Fatalf("announced %d changes for one check run, want 1", len(*got))
		}
		return (*got)[0]
	}

	steps := []struct {
		id                       int64
		name, status, conclusion string
		wantSummary              string
	}{
		{1, "build", "in_progress", "", ""},
		{2, "lint", "in_progress", "", ""},
		{1, "build", "completed", "failure", "Checks failed: build"},
		// Already failing: another failure isn't news.
		{2, "lint", "completed", "failure", ""},
		{3, "build", "in_progress", "", ""},
		{3, "build", "completed", "success", ""},
		{4, "lint", "completed", "success", "Checks passed"},
	}
	for _, s := range steps {
		change := deliver(s.id, s.name, s.status, s.conclusion)
		if change.Summary != s.wantSummary {
			t.Errorf("%s %s %s: summary = %q, want %q", s.name, s.status, s.conclusion, change.Summary, s.wantSummary)
		}
		if !slices.Contains(change.Changes, "ci_status") {
			t.Errorf("%s %s: changes = %v, want ci_status", s.name, s.status, change.Changes)
		}
		if change.Actor != "" {
			t.Errorf("CI announced with actor %q", change.Actor)
		}
		if change.Event != "check_run."+s.status {
			t.Errorf("event = %q", change.Event)
		}
	}
}

func TestSectionChangesAreAnnounced(t *testing.T) {
	wf := reviewRequestedWorkflow()
	env := membershipEnv(t, wf)
	key := whKey(1)
	seedCachedPR(t, env.db, key, "head1")
	listIn(t, env.db, wf, 1)
	got := recordPRChanges(t)

	applyWebhookEvent(webhookDelivery{eventType: "pull_request_review", event: &github.PullRequestReviewEvent{
		Action:      github.Ptr("submitted"),
		Review:      &github.PullRequestReview{ID: github.Ptr(int64(9)), User: user("me"), State: github.Ptr("approved")},
		PullRequest: requestedOfMe(whPR(1, "head1")),
		Repo:        whRepository(),
		Sender:      user("me"),
	}})
	if len(*got) != 1 || (*got)[0].Event != "pull_request_review.submitted" {
		t.Fatalf("announced %+v before re-evaluating, want just the review", *got)
	}
	drainReevaluations()

	if len(*got) != 2 {
		t.Fatalf("announced %+v, want the review then the section change", *got)
	}
	sections := (*got)[1]
	if sections.Key != key || sections.Event != ChangeSections || !slices.Equal(sections.Changes, []string{ChangeSections}) || sections.Summary != "" {
		t.Errorf("section change = %+v", sections)
	}
}

func TestPullRequestSummary(t *testing.T) {
	pr := whPR(1, "head1")
	for _, c := range []struct {
		sender string
		event  *github.PullRequestEvent
		want   string
	}{
		{"", &github.PullRequestEvent{Action: github.Ptr("closed")}, "Someone closed the PR"},
		{"a", &github.PullRequestEvent{Action: github.Ptr("edited"), Changes: &github.EditChange{Title: &github.EditTitle{From: github.Ptr("old")}}}, "a edited the title"},
		{"a", &github.PullRequestEvent{Action: github.Ptr("edited"), Changes: &github.EditChange{Base: &github.EditBase{Ref: &github.EditRef{From: github.Ptr("develop")}}}}, "a changed the base branch to main"},
		{"a", &github.PullRequestEvent{Action: github.Ptr("review_requested"), RequestedReviewer: user("rev")}, "a requested a review from rev"},
		{"a", &github.PullRequestEvent{Action: github.Ptr("review_requested"), RequestedTeam: &github.Team{Slug: github.Ptr("core")}}, "a requested a review from core"},
		{"a", &github.PullRequestEvent{Action: github.Ptr("labeled"), Label: &github.Label{Name: github.Ptr("bug")}}, "a added the label bug"},
		{"a", &github.PullRequestEvent{Action: github.Ptr("ready_for_review")}, "a marked the PR ready for review"},
		{"a", &github.PullRequestEvent{Action: github.Ptr("auto_merge_enabled")}, ""},
	} {
		c.event.PullRequest = pr
		if c.sender != "" {
			c.event.Sender = user(c.sender)
		}
		if got := pullRequestSummary(c.event); got != c.want {
			t.Errorf("%s: summary = %q, want %q", c.event.GetAction(), got, c.want)
		}
	}
}

func TestReviewSummary(t *testing.T) {
	review := func(state string) *github.PullRequestReview {
		return &github.PullRequestReview{User: user("rev"), State: github.Ptr(state)}
	}
	for _, c := range []struct {
		action string
		review *github.PullRequestReview
		want   string
	}{
		{"submitted", review("approved"), "rev approved"},
		{"submitted", review("changes_requested"), "rev requested changes"},
		{"submitted", review("commented"), "rev reviewed"},
		{"dismissed", review("dismissed"), "lead dismissed rev's review"},
	} {
		e := &github.PullRequestReviewEvent{Action: github.Ptr(c.action), Review: c.review, Sender: user("lead")}
		if got := reviewSummary(e); got != c.want {
			t.Errorf("%s %s: summary = %q, want %q", c.action, c.review.GetState(), got, c.want)
		}
	}
}

func TestCISummaryChangeNamesAFewFailures(t *testing.T) {
	failed := []string{"a", "b", "c", "d", "e"}
	if got, want := ciSummaryChange("pending", "failure", failed), "Checks failed: a, b, c, 2 more"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if !slices.Equal(failed, []string{"a", "b", "c", "d", "e"}) {
		t.Errorf("summarizing rewrote the failures: %v", failed)
	}
	if got := ciSummaryChange("success", "pending", nil); got != "" {
		t.Errorf("a check starting was announced: %q", got)
	}
}
