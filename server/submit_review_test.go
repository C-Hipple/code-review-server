package server

import (
	"crs/database"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func localComment(id int64, replyTo *int64) database.LocalComment {
	body := "pending"
	return database.LocalComment{ID: id, Body: &body, ReplyToID: replyTo}
}

func ptr(v int64) *int64 { return &v }

// TestResolveReplyAnchor covers the ids SubmitReview may hand GitHub. Local ids
// are row ids in our own database; sending one as in_reply_to gets the reply
// rejected, and the reviewer's text is lost with it.
func TestResolveReplyAnchor(t *testing.T) {
	tests := []struct {
		name    string
		comment database.LocalComment
		pending []database.LocalComment
		want    int64
		wantOK  bool
	}{
		{
			name:    "top-level comment is not a reply",
			comment: localComment(1, nil),
			want:    0,
		},
		{
			name:    "reply to a GitHub comment answers that comment",
			comment: localComment(1, ptr(2147483001)),
			want:    2147483001,
			wantOK:  true,
		},
		{
			// Replying twice to the same thread in one sitting: the second
			// reply targets the first, which only exists locally.
			name:    "reply to a pending reply answers the GitHub comment above it",
			comment: localComment(2, ptr(1)),
			pending: []database.LocalComment{localComment(1, ptr(2147483001))},
			want:    2147483001,
			wantOK:  true,
		},
		{
			name:    "chain of pending replies walks up to the GitHub comment",
			comment: localComment(3, ptr(2)),
			pending: []database.LocalComment{
				localComment(1, ptr(2147483001)),
				localComment(2, ptr(1)),
			},
			want:   2147483001,
			wantOK: true,
		},
		{
			// The comment being answered has not been submitted either, so
			// there is nothing on GitHub to reply to yet.
			name:    "reply to a pending top-level comment has no anchor",
			comment: localComment(2, ptr(1)),
			pending: []database.LocalComment{localComment(1, nil)},
			want:    0,
		},
		{
			name:    "cycle does not hang",
			comment: localComment(1, ptr(2)),
			pending: []database.LocalComment{localComment(2, ptr(1))},
			want:    0,
		},
		{
			name:    "self-reference does not hang",
			comment: localComment(1, ptr(1)),
			pending: []database.LocalComment{localComment(1, ptr(1))},
			want:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pending := map[int64]database.LocalComment{tt.comment.ID: tt.comment}
			for _, c := range tt.pending {
				pending[c.ID] = c
			}
			got, ok := resolveReplyAnchor(tt.comment, pending)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("resolveReplyAnchor() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// A submit carries the ids of the drafts the client previewed, and is refused
// — nothing posted — when any of them is gone: the review would otherwise go
// out without comments the reviewer believes it carries. A retry of a submit
// that already went through is refused the same way.
func TestSubmitReviewRefusesWhenPreviewedDraftsAreGone(t *testing.T) {
	db := aiTestSetup(t, nil)
	allowServerIdentityActions(t)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	fake := useFakeGitHub(t)
	alice := handlerFor("alice", false)
	kept, gone := addDraft(t, alice, "kept"), addDraft(t, alice, "gone")
	if err := db.DeleteLocalComment("alice", gone); err != nil {
		t.Fatal(err)
	}
	submit := func(ids ...int64) error {
		return alice.SubmitReview(&SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
			Event: "APPROVE", Body: "LGTM", ExpectedDraftIDs: ids}, &SubmitReviewReply{})
	}
	reviews := func() int {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.reviews)
	}

	wantError(t, "a submit previewing a deleted draft", submit(kept, gone), errDraftsChanged.Error())
	if n := reviews(); n != 0 {
		t.Fatalf("GitHub received %d reviews from a refused submit", n)
	}
	if n := draftCount(t, db, "alice"); n != 1 {
		t.Fatalf("a refused submit left %d drafts, want the one still there", n)
	}

	if err := submit(kept); err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}
	wantError(t, "the same submit again", submit(kept), errDraftsChanged.Error())
	if n := reviews(); n != 1 {
		t.Errorf("GitHub received %d reviews, want the one", n)
	}
}

// blockingGitHub holds the first review posted until release is closed.
type blockingGitHub struct {
	*fakeGitHub
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *blockingGitHub) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/reviews") {
		g.once.Do(func() {
			close(g.entered)
			<-g.release
		})
	}
	return g.fakeGitHub.RoundTrip(req)
}

// A submit sent again while the first is still posting — a client that lost
// its connection mid-submit and retried — waits for the first rather than
// reading the same drafts and posting them a second time.
func TestConcurrentSubmitsOfOneReviewAreSerialized(t *testing.T) {
	db := aiTestSetup(t, nil)
	allowServerIdentityActions(t)
	seedAIPR(t, db, aiTestComments(), aiThreadsJSON(false))
	fake := &fakeGitHub{}
	gh := &blockingGitHub{fakeGitHub: fake, entered: make(chan struct{}), release: make(chan struct{})}
	orig := http.DefaultTransport
	http.DefaultTransport = gh
	t.Cleanup(func() { http.DefaultTransport = orig })
	alice := handlerFor("alice", false)
	draft := addDraft(t, alice, "only once")

	submit := func(ids []int64, done chan<- error) {
		done <- alice.SubmitReview(&SubmitReviewArgs{Owner: aiOwner, Repo: aiRepo, Number: aiNumber,
			Event: "REQUEST_CHANGES", Body: "Needs a test.", ExpectedDraftIDs: ids}, &SubmitReviewReply{})
	}
	first, previewed, unchecked := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	go submit([]int64{draft}, first)
	<-gh.entered
	// Both retries reach the server while the first is posting.
	go submit([]int64{draft}, previewed)
	go submit(nil, unchecked)
	time.Sleep(50 * time.Millisecond)
	close(gh.release)

	if err := <-first; err != nil {
		t.Fatalf("the first submit: %v", err)
	}
	wantError(t, "the retry that previewed the draft", <-previewed, errDraftsChanged.Error())
	if err := <-unchecked; err != nil {
		t.Fatalf("the retry that named no drafts: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.reviews) != 2 {
		t.Fatalf("GitHub received %d reviews, want the first and the unchecked retry's", len(fake.reviews))
	}
	for i, review := range fake.reviews {
		comments, _ := review["comments"].([]any)
		if want := map[int]int{0: 1, 1: 0}[i]; len(comments) != want {
			t.Errorf("review %d carried %d comments, want %d: the draft went out twice", i, len(comments), want)
		}
	}
}
