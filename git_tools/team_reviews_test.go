package git_tools

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v74/github"
)

func TestGetReviewRequestHistoryCollectsTeamsAndUsers(t *testing.T) {
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{"timelineItems":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"requestedReviewer":{"__typename":"Team","name":"Platform Eng","slug":"platform-eng","organization":{"login":"acme"}}},
				{"requestedReviewer":{"__typename":"User","login":"alice"}},
				{"requestedReviewer":{"__typename":"Team","name":"Platform Eng","slug":"platform-eng","organization":{"login":"acme"}}},
				{"requestedReviewer":{"__typename":"Team","name":"","slug":"security","organization":{"login":""}}}
			]}}}}}`)
	})

	history, err := GetReviewRequestHistory("acme", "widgets", 7)
	if err != nil {
		t.Fatalf("GetReviewRequestHistory returned an error: %v", err)
	}

	want := []TeamRef{
		{Name: "Platform Eng", Slug: "platform-eng", Org: "acme"},
		// A team with no name or org falls back to its slug and the repo owner.
		{Name: "security", Slug: "security", Org: "acme"},
	}
	if len(history.Teams) != len(want) {
		t.Fatalf("got %d teams (%+v), want %d", len(history.Teams), history.Teams, len(want))
	}
	for i, team := range want {
		if history.Teams[i] != team {
			t.Errorf("team %d = %+v, want %+v", i, history.Teams[i], team)
		}
	}
	if len(history.Users) != 1 || history.Users[0] != "alice" {
		t.Errorf("users = %v, want [alice]", history.Users)
	}
}

func TestGetReviewRequestHistoryFollowsPagination(t *testing.T) {
	page := 0
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)

		page++
		w.Header().Set("Content-Type", "application/json")
		if page == 1 {
			if after := req.Variables["after"]; after != nil {
				t.Errorf("first page sent after=%v, want null", after)
			}
			io.WriteString(w, `{"data":{"repository":{"pullRequest":{"timelineItems":{
				"pageInfo":{"hasNextPage":true,"endCursor":"CURSOR"},
				"nodes":[{"requestedReviewer":{"__typename":"Team","name":"A","slug":"a","organization":{"login":"acme"}}}]}}}}}`)
			return
		}
		if got := req.Variables["after"]; got != "CURSOR" {
			t.Errorf("second page sent after=%v, want CURSOR", got)
		}
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{"timelineItems":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[{"requestedReviewer":{"__typename":"Team","name":"B","slug":"b","organization":{"login":"acme"}}}]}}}}}`)
	})

	history, err := GetReviewRequestHistory("acme", "widgets", 7)
	if err != nil {
		t.Fatalf("GetReviewRequestHistory returned an error: %v", err)
	}
	if len(history.Teams) != 2 {
		t.Fatalf("got %d teams, want both pages", len(history.Teams))
	}
}

func TestGetReviewRequestHistoryReturnsGraphQLErrors(t *testing.T) {
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors":[{"message":"Resource not accessible"}]}`)
	})

	if _, err := GetReviewRequestHistory("acme", "widgets", 7); err == nil {
		t.Fatal("expected an error when GitHub reports one")
	} else if !strings.Contains(err.Error(), "Resource not accessible") {
		t.Errorf("error = %v, want GitHub's message", err)
	}
}

func review(login, state string) *github.PullRequestReview {
	return &github.PullRequestReview{
		User:  &github.User{Login: github.String(login)},
		State: github.String(state),
	}
}

func TestLatestReviewDecisions(t *testing.T) {
	decisions := LatestReviewDecisions([]*github.PullRequestReview{
		review("Alice", "APPROVED"),
		// A later plain comment does not undo the approval, exactly as GitHub
		// itself treats it.
		review("alice", "COMMENTED"),
		review("bob", "APPROVED"),
		review("bob", "CHANGES_REQUESTED"),
		review("carol", "CHANGES_REQUESTED"),
		review("carol", "DISMISSED"),
		review("dave", "COMMENTED"),
	})

	want := map[string]string{"alice": "APPROVED", "bob": "CHANGES_REQUESTED"}
	if len(decisions) != len(want) {
		t.Fatalf("decisions = %v, want %v", decisions, want)
	}
	for login, state := range want {
		if decisions[login] != state {
			t.Errorf("decision for %s = %q, want %q", login, decisions[login], state)
		}
	}
}

func teamRef(slug string) TeamRef {
	return TeamRef{Name: strings.ToUpper(slug[:1]) + slug[1:], Slug: slug, Org: "acme"}
}

func TestResolveTeamReviewsStatuses(t *testing.T) {
	in := TeamReviewInput{
		Teams: []TeamRef{teamRef("approvers"), teamRef("blockers"), teamRef("waiters"), teamRef("unknowns")},
		PendingKeys: map[string]bool{
			"acme/waiters": true,
			// blockers asked for changes, so GitHub still lists them as pending.
			"acme/blockers": true,
		},
		Members: map[string][]string{
			"acme/approvers": {"alice", "bob"},
			"acme/blockers":  {"carol"},
			"acme/waiters":   {"dave"},
			// "unknowns" is absent: its membership could not be read.
		},
		MyLogin:   "erin",
		Decisions: map[string]string{"alice": "APPROVED", "carol": "CHANGES_REQUESTED"},
	}

	byName := map[string]TeamReviewStatus{}
	for _, status := range ResolveTeamReviews(in) {
		byName[status.Name] = status
	}

	cases := map[string]string{
		"Approvers": TeamReviewApproved,
		"Blockers":  TeamReviewChangesRequested,
		"Waiters":   TeamReviewPending,
		// Nobody attributable reviewed and GitHub is no longer waiting on them:
		// somebody satisfied the request, we just can't say who.
		"Unknowns": TeamReviewReviewed,
	}
	for name, want := range cases {
		if got := byName[name].Status; got != want {
			t.Errorf("%s status = %q, want %q", name, got, want)
		}
	}
}

func TestResolveTeamReviewsChangesRequestedBeatsApproval(t *testing.T) {
	statuses := ResolveTeamReviews(TeamReviewInput{
		Teams:       []TeamRef{teamRef("platform")},
		PendingKeys: map[string]bool{},
		Members:     map[string][]string{"acme/platform": {"alice", "bob"}},
		Decisions:   map[string]string{"alice": "APPROVED", "bob": "CHANGES_REQUESTED"},
	})

	if len(statuses) != 1 || statuses[0].Status != TeamReviewChangesRequested {
		t.Fatalf("statuses = %+v, want a single changes_requested entry", statuses)
	}
}

func TestResolveTeamReviewsMarksMyTeams(t *testing.T) {
	statuses := ResolveTeamReviews(TeamReviewInput{
		Teams: []TeamRef{teamRef("byMembership"), teamRef("byRef"), teamRef("theirs")},
		Members: map[string][]string{
			"acme/byMembership": {"ERIN"}, // GitHub is inconsistent about casing
			"acme/theirs":       {"alice"},
			// byRef's membership is unreadable; GetMyTeamRefs still knows it.
		},
		MyTeamKeys:  map[string]bool{"acme/byRef": true},
		PendingKeys: map[string]bool{"acme/byMembership": true, "acme/byRef": true, "acme/theirs": true},
		MyLogin:     "erin",
	})

	mine := map[string]bool{}
	for _, status := range statuses {
		mine[status.Name] = status.Mine
	}
	if !mine["ByMembership"] || !mine["ByRef"] {
		t.Errorf("expected both of the user's teams to be marked mine, got %v", mine)
	}
	if mine["Theirs"] {
		t.Error("a team the user is not on should not be marked mine")
	}
}

func TestResolveTeamReviewsWithoutIdentityMarksNothingMine(t *testing.T) {
	statuses := ResolveTeamReviews(TeamReviewInput{
		Teams:       []TeamRef{teamRef("platform")},
		Members:     map[string][]string{"acme/platform": {"erin"}},
		PendingKeys: map[string]bool{"acme/platform": true},
		Personal:    true,
	})

	if len(statuses) != 1 {
		t.Fatalf("statuses = %+v, want only the team (no personal entry without a login)", statuses)
	}
	if statuses[0].Mine {
		t.Error("no configured identity should leave every entry unhighlighted")
	}
}

func TestResolveTeamReviewsOrdersPersonalThenMineThenName(t *testing.T) {
	statuses := ResolveTeamReviews(TeamReviewInput{
		Teams:       []TeamRef{teamRef("zebra"), teamRef("alpha"), teamRef("mine")},
		MyTeamKeys:  map[string]bool{"acme/mine": true},
		PendingKeys: map[string]bool{"acme/zebra": true, "acme/alpha": true, "acme/mine": true},
		MyLogin:     "erin",
		Personal:    true,
	})

	var names []string
	for _, status := range statuses {
		names = append(names, status.Name)
	}
	want := []string{"erin", "Mine", "Alpha", "Zebra"}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names = %v, want %v", names, want)
		}
	}
	if !statuses[0].Personal || !statuses[0].Mine {
		t.Errorf("personal entry = %+v, want personal and mine", statuses[0])
	}
}

func TestResolveTeamReviewsPersonalStatusFollowsMyOwnReview(t *testing.T) {
	cases := []struct {
		name     string
		decision string
		pending  bool
		want     string
	}{
		{"still requested", "", true, TeamReviewPending},
		{"approved", "APPROVED", false, TeamReviewApproved},
		{"asked for changes", "CHANGES_REQUESTED", false, TeamReviewChangesRequested},
		// The request is gone and no decision of ours survives: we reviewed and
		// only commented, or somebody removed the request.
		{"request cleared", "", false, TeamReviewReviewed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decisions := map[string]string{}
			if tc.decision != "" {
				decisions["erin"] = tc.decision
			}
			statuses := ResolveTeamReviews(TeamReviewInput{
				MyLogin:         "Erin",
				Personal:        true,
				PersonalPending: tc.pending,
				Decisions:       decisions,
			})
			if len(statuses) != 1 {
				t.Fatalf("statuses = %+v, want just the personal entry", statuses)
			}
			if statuses[0].Status != tc.want {
				t.Errorf("personal status = %q, want %q", statuses[0].Status, tc.want)
			}
		})
	}
}
