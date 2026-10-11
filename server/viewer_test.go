package server

import (
	"crs/config"
	"crs/database"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// handlerFor is the handler of a remote connection authenticated as login.
func handlerFor(login string, admin bool) *RPCHandler {
	session := &Session{}
	session.SetIdentity(login, admin)
	return &RPCHandler{session: session}
}

func TestHandlerIdentity(t *testing.T) {
	local := &RPCHandler{}
	if local.viewer() != "" || !local.isAdmin() || !local.isLocal() {
		t.Errorf("a handler without a session is the local one: viewer %q, admin %v, local %v",
			local.viewer(), local.isAdmin(), local.isLocal())
	}

	alice := handlerFor("alice", false)
	if alice.viewer() != "alice" || alice.isAdmin() || alice.isLocal() {
		t.Errorf("alice's handler: viewer %q, admin %v, local %v", alice.viewer(), alice.isAdmin(), alice.isLocal())
	}
	if admin := handlerFor("", true); admin.viewer() != "" || !admin.isAdmin() || admin.isLocal() {
		t.Errorf("a remote administrator is the server's identity but not local: viewer %q, admin %v, local %v",
			admin.viewer(), admin.isAdmin(), admin.isLocal())
	}

	// A connection that has not authenticated is nobody, and no administrator.
	fresh := &RPCHandler{session: &Session{}}
	if _, admin, authenticated := fresh.session.Identity(); admin || authenticated {
		t.Errorf("a fresh session reads as admin %v, authenticated %v", admin, authenticated)
	}
	if fresh.isAdmin() || fresh.isLocal() {
		t.Error("an unauthenticated remote connection must be neither administrator nor local")
	}
}

// Authenticating replaces the identity while other calls on the same
// connection read it; run with -race this catches an unguarded field.
func TestSessionIsSafeForConcurrentCalls(t *testing.T) {
	h := &RPCHandler{session: &Session{}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			h.session.SetIdentity(fmt.Sprintf("user-%d", i), i%2 == 0)
		}(i)
		go func() {
			defer wg.Done()
			_, _ = h.viewer(), h.isAdmin()
		}()
	}
	wg.Wait()
	if login, _, authenticated := h.session.Identity(); !authenticated || !strings.HasPrefix(login, "user-") {
		t.Errorf("after authenticating the session reads %q, authenticated %v", login, authenticated)
	}
}

// seedSectionItem puts acme/widgets#number into user's section, created at
// createdAt.
func seedSectionItem(t *testing.T, db *database.DB, user, section string, number int, createdAt time.Time) {
	t.Helper()
	s, err := db.GetOrCreateSection(user, section, 0)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}
	details := []string{fmt.Sprintf("%d", number), "Repo: acme/widgets", fmt.Sprintf("https://github.com/acme/widgets/pull/%d", number)}
	item, err := db.UpsertItemWithWorkflow(s.ID, fmt.Sprintf("acme/widgets-%d", number), "TODO",
		fmt.Sprintf("PR %d", number), details, nil, 0, config.OwnershipKey(user, "wf"))
	if err != nil {
		t.Fatalf("UpsertItemWithWorkflow: %v", err)
	}
	if _, err := db.Exec("UPDATE items SET created_at = ? WHERE id = ?", createdAt.UTC().Format("2006-01-02 15:04:05"), item.ID); err != nil {
		t.Fatalf("setting created_at: %v", err)
	}
}

func reviewNumbers(items []ReviewItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, fmt.Sprintf("%s#%d", item.Section, item.Number))
	}
	return out
}

// Each viewer sees their own dashboard — their sections, their items, sorted
// by their own SectionSorting — and nobody else's, though every dashboard
// here has a "Needs Review".
func TestRendererShowsOnlyTheViewersDashboard(t *testing.T) {
	db := setupTestDB(t)
	config.SetC(config.Config{DB: db, SectionSorting: map[string]string{"Needs Review": SortNewestFirst}})
	t.Cleanup(func() { config.SetC(config.Config{}) })

	if err := db.CreateUser(database.User{Login: "alice", TokenHash: "a",
		SectionSorting: map[string]string{"Needs Review": SortOldestFirst}}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(database.User{Login: "bob", TokenHash: "b"}); err != nil {
		t.Fatal(err)
	}
	old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	seedSectionItem(t, db, "", "Needs Review", 1, old)
	seedSectionItem(t, db, "", "Needs Review", 2, recent)
	seedSectionItem(t, db, "alice", "Needs Review", 3, recent)
	seedSectionItem(t, db, "alice", "Needs Review", 4, old)
	seedSectionItem(t, db, "alice", "Alice Only", 5, old)
	seedSectionItem(t, db, "bob", "Needs Review", 6, old)

	for viewer, want := range map[string][]string{
		"":      {"Needs Review#2", "Needs Review#1"}, // the config's newest_first
		"alice": {"Alice Only#5", "Needs Review#4", "Needs Review#3"},
		"bob":   {"Needs Review#6"},
	} {
		items, err := NewOrgRenderer(db, viewer).GetAllReviewItems()
		if err != nil {
			t.Fatalf("GetAllReviewItems(%q): %v", viewer, err)
		}
		if got := reviewNumbers(items); !slices.Equal(got, want) {
			t.Errorf("%q sees %v, want %v", viewer, got, want)
		}
	}

	content, _, err := NewOrgRenderer(db, "bob").RenderAndGetItems(RenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content, "Alice Only") || strings.Count(content, "Needs Review") != 1 {
		t.Errorf("bob's org text shows sections that are not his:\n%s", content)
	}
}

// GetAllReviews and GetAdjacentPR read the connection's own list.
func TestReviewListRPCsReadTheViewersList(t *testing.T) {
	db := aiTestSetup(t, nil)
	useTempConfig(t, "SleepDuration = 10\n")
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	seedSectionItem(t, db, "", "Needs Review", 7, time.Now())
	seedSectionItem(t, db, "", "Needs Review", 8, time.Now())
	seedSectionItem(t, db, "alice", "Needs Review", aiNumber, time.Now())

	alice := handlerFor("alice", false)
	reviews := &GetReviewsReply{}
	if err := alice.GetAllReviews(&GetReviewsArgs{}, reviews); err != nil {
		t.Fatalf("GetAllReviews: %v", err)
	}
	if got := reviewNumbers(reviews.Items); !slices.Equal(got, []string{fmt.Sprintf("Needs Review#%d", aiNumber)}) {
		t.Errorf("alice's review list = %v, want only her PR", got)
	}

	// #7 is not on alice's list, so "next" is the first PR that is. Walking
	// the server's list instead would land on #8.
	adjacent := &GetAdjacentPRReply{}
	if err := alice.GetAdjacentPR(&GetAdjacentPRArgs{Owner: aiOwner, Repo: aiRepo, Number: 7}, adjacent); err != nil {
		t.Fatalf("GetAdjacentPR: %v", err)
	}
	if adjacent.AdjacentNumber != aiNumber {
		t.Errorf("alice's next PR is #%d, want #%d", adjacent.AdjacentNumber, aiNumber)
	}

	hello := &HelloReply{}
	if err := alice.Hello(&HelloArgs{}, hello); err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if hello.Count != 1 {
		t.Errorf("Hello counted %d sections for alice, want her 1", hello.Count)
	}
}

// localDrafts are the bodies of the drafts a reply shows.
func localDrafts(p PRPayload) []string {
	var bodies []string
	for _, c := range append(slices.Clone(p.Comments), p.OutdatedComments...) {
		if c.Author == "local" {
			bodies = append(bodies, c.Body)
		}
	}
	slices.Sort(bodies)
	return bodies
}

func addDraft(t *testing.T, h *RPCHandler, body string) int64 {
	t.Helper()
	reply := &AddCommentReply{}
	if err := h.AddComment(&AddCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
		Filename: "src/main.ts", Position: 2, Body: body}, reply); err != nil {
		t.Fatalf("AddComment(%q): %v", body, err)
	}
	return reply.ID
}

func getPR(t *testing.T, h *RPCHandler) PRPayload {
	t.Helper()
	reply := &GetPRReply{}
	if err := h.GetPR(&GetPRstructArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, reply); err != nil {
		t.Fatalf("GetPR: %v", err)
	}
	return reply.PRPayload
}

// Drafts and feedback are their author's alone: each connection reads, edits
// and clears only its own, and another user's draft id reads as no draft at
// all.
func TestDraftsAndFeedbackBelongToTheirAuthor(t *testing.T) {
	db := aiTestSetup(t, nil)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	alice, bob, local := handlerFor("alice", false), handlerFor("bob", false), &RPCHandler{}

	aliceDraft := addDraft(t, alice, "alice's draft")
	addDraft(t, bob, "bob's draft")
	addDraft(t, local, "the server's draft")

	for name, c := range map[string]struct {
		h    *RPCHandler
		want []string
	}{
		"alice": {alice, []string{"alice's draft"}},
		"bob":   {bob, []string{"bob's draft"}},
		"local": {local, []string{"the server's draft"}},
	} {
		if got := localDrafts(getPR(t, c.h)); !slices.Equal(got, c.want) {
			t.Errorf("%s sees drafts %v, want %v", name, got, c.want)
		}
	}

	feedback := &SetFeedbackReply{}
	if err := alice.SetFeedback(&SetFeedbackArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Body: "alice's notes"}, feedback); err != nil {
		t.Fatalf("SetFeedback: %v", err)
	}
	if feedback.Feedback != "alice's notes" {
		t.Errorf("SetFeedback replied with feedback %q", feedback.Feedback)
	}
	if got := getPR(t, bob).Feedback; got != "" {
		t.Errorf("bob sees alice's feedback %q", got)
	}
	if got := getPR(t, local).Feedback; got != "" {
		t.Errorf("the local connection sees alice's feedback %q", got)
	}

	// Bob knows the id of alice's draft; it gets him nothing.
	err := bob.EditComment(&EditCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, ID: aliceDraft, Body: "defaced"}, &EditCommentReply{})
	if err == nil || err.Error() != "comment not found" {
		t.Errorf("editing another user's draft: err = %v, want \"comment not found\"", err)
	}
	err = bob.DeleteComment(&DeleteCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, ID: aliceDraft}, &DeleteCommentReply{})
	if err == nil || err.Error() != "comment not found" {
		t.Errorf("deleting another user's draft: err = %v, want \"comment not found\"", err)
	}
	if err := bob.RemovePRComments(&RemovePRCommentsArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber}, &RemovePRCommentsReply{}); err != nil {
		t.Fatalf("RemovePRComments: %v", err)
	}
	if got := localDrafts(getPR(t, alice)); !slices.Equal(got, []string{"alice's draft"}) {
		t.Errorf("bob's edits reached alice's drafts: she now has %v", got)
	}
	if got := localDrafts(getPR(t, bob)); len(got) != 0 {
		t.Errorf("RemovePRComments left bob's drafts %v", got)
	}
	if got := localDrafts(getPR(t, local)); !slices.Equal(got, []string{"the server's draft"}) {
		t.Errorf("bob's RemovePRComments reached the server's drafts: %v", got)
	}

	// Her own draft is hers to change.
	edited := &EditCommentReply{}
	if err := alice.EditComment(&EditCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, ID: aliceDraft, Body: "alice's edit"}, edited); err != nil {
		t.Fatalf("EditComment on her own draft: %v", err)
	}
	if got := localDrafts(edited.PRPayload); !slices.Equal(got, []string{"alice's edit"}) {
		t.Errorf("after editing, alice sees %v", got)
	}
	if err := alice.DeleteComment(&DeleteCommentArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, ID: aliceDraft}, &DeleteCommentReply{}); err != nil {
		t.Fatalf("DeleteComment on her own draft: %v", err)
	}
}

// fakeGitHub answers the GitHub API from memory for the rest of the test, so a
// handler that reaches GitHub — SubmitReview always does — can run. It
// records the reviews, replies and merges posted, and answers every read with
// an empty but well-formed PR. GraphQL fails, which the readers of it
// tolerate.
//
// It also records the Authorization header each write and read was sent
// with, and answers 401 to any token in rejected, as GitHub does a revoked
// one.
type fakeGitHub struct {
	mu        sync.Mutex
	reviews   []map[string]any
	replies   []map[string]any
	merges    int
	writeAuth []string
	readAuth  []string
	rejected  map[string]bool
}

// reject makes the fake answer 401 to requests carrying token.
func (f *fakeGitHub) reject(token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rejected == nil {
		f.rejected = map[string]bool{}
	}
	f.rejected[token] = true
}

// auths returns the Authorization headers of the writes and the reads so far.
func (f *fakeGitHub) auths() (writes, reads []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writeAuth...), append([]string(nil), f.readAuth...)
}

func useFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	fake := &fakeGitHub{}
	orig := http.DefaultTransport
	http.DefaultTransport = fake
	t.Cleanup(func() { http.DefaultTransport = orig })
	return fake
}

func (f *fakeGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	}
	path := req.URL.Path
	if req.URL.Host == "api.github.com" {
		auth := req.Header.Get("Authorization")
		f.mu.Lock()
		// GraphQL is a POST, and a read.
		if req.Method == http.MethodGet || strings.HasSuffix(path, "/graphql") {
			f.readAuth = append(f.readAuth, auth)
		} else {
			f.writeAuth = append(f.writeAuth, auth)
		}
		rejected := f.rejected[strings.TrimPrefix(auth, "Bearer ")]
		f.mu.Unlock()
		if rejected {
			return respond(http.StatusUnauthorized, `{"message": "Bad credentials"}`)
		}
	}
	switch {
	case req.URL.Host != "api.github.com":
		return respond(http.StatusNotFound, `{"message": "not the GitHub API"}`)
	case strings.HasSuffix(path, "/graphql"):
		return respond(http.StatusBadGateway, `{"message": "no GraphQL here"}`)
	case req.Method == http.MethodPut && strings.HasSuffix(path, "/merge"):
		f.mu.Lock()
		defer f.mu.Unlock()
		f.merges++
		return respond(http.StatusOK, `{"merged": true, "sha": "merge-sha", "message": "Pull Request successfully merged"}`)
	case req.Method == http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return respond(http.StatusBadRequest, `{"message": "bad body"}`)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(path, "/reviews") {
			f.reviews = append(f.reviews, body)
		} else {
			f.replies = append(f.replies, body)
		}
		return respond(http.StatusOK, `{"id": 1}`)
	case strings.Contains(req.Header.Get("Accept"), "diff"):
		return respond(http.StatusOK, aiTestDiff)
	case strings.HasSuffix(path, fmt.Sprintf("/pulls/%d", aiNumber)):
		// Draft, the head's label and both repos are always in GitHub's
		// answer, and the workflow layer reads them unchecked when a submit
		// reprocesses the PR for a user whose workflow lists it.
		repo := fmt.Sprintf(`{"name": %q, "full_name": "%s/%s", "owner": {"login": %q}}`, aiRepo, aiOwner, aiRepo, aiOwner)
		return respond(http.StatusOK, fmt.Sprintf(`{"number": %d, "state": "open", "draft": false, "title": "Add greeting helper",
			"html_url": "https://github.com/%s/%s/pull/%d", "user": {"login": "author"},
			"head": {"sha": "sha-1", "ref": "feature", "label": "author:feature", "repo": %s},
			"base": {"sha": "base-1", "ref": "main", "label": "%s:main", "repo": %s}}`,
			aiNumber, aiOwner, aiRepo, aiNumber, repo, aiOwner, repo))
	case strings.HasSuffix(path, "/requested_reviewers"):
		return respond(http.StatusOK, `{"users": [], "teams": []}`)
	case strings.HasSuffix(path, "/status"):
		return respond(http.StatusOK, `{"state": "success", "statuses": [], "total_count": 0}`)
	case strings.HasSuffix(path, "/check-runs"):
		return respond(http.StatusOK, `{"total_count": 0, "check_runs": []}`)
	}
	// Reviews, comments, commits: an empty list.
	return respond(http.StatusOK, `[]`)
}

// The review a user submits is made of their drafts alone; everyone else's
// stay where they were.
func TestSubmitReviewSendsOnlyTheViewersDrafts(t *testing.T) {
	db := aiTestSetup(t, nil)
	allowServerIdentityActions(t)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	alice, bob := handlerFor("alice", false), handlerFor("bob", false)
	addDraft(t, alice, "alice's draft")
	addDraft(t, bob, "bob's draft")
	fake := useFakeGitHub(t)

	reply := &SubmitReviewReply{}
	if err := alice.SubmitReview(&SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber, Event: "COMMENT"}, reply); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	fake.mu.Lock()
	reviews := fake.reviews
	fake.mu.Unlock()
	if len(reviews) != 1 {
		t.Fatalf("GitHub received %d reviews, want 1", len(reviews))
	}
	comments, _ := reviews[0]["comments"].([]any)
	if len(comments) != 1 || comments[0].(map[string]any)["body"] != "alice's draft" {
		t.Errorf("the review carried %v, want alice's draft alone", comments)
	}

	if drafts, _ := db.GetLocalCommentsForPR("alice", aiOwner, aiRepo, aiNumber); len(drafts) != 0 {
		t.Errorf("alice's submitted drafts were kept: %v", drafts)
	}
	drafts, _ := db.GetLocalCommentsForPR("bob", aiOwner, aiRepo, aiNumber)
	if len(drafts) != 1 || *drafts[0].Body != "bob's draft" {
		t.Errorf("bob's drafts after alice's submit = %v, want his one draft untouched", drafts)
	}
}

// allowServerIdentityActions lets remote connections write to GitHub under
// the server's token for the rest of the test.
func allowServerIdentityActions(t *testing.T) {
	t.Helper()
	cfg := config.C()
	cfg.AllowServerIdentityActions = true
	config.SetC(cfg)
}

func TestDescribeWorkflowKeys(t *testing.T) {
	keys := config.OwnershipKey("", "needs_review") + "," + config.OwnershipKey("alice", "needs_review")
	if got, want := describeWorkflowKeys(keys), "needs_review,needs_review (alice)"; got != want {
		t.Errorf("describeWorkflowKeys = %q, want %q", got, want)
	}
}
