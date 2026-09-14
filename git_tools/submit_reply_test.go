package git_tools

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v74/github"
)

// TestSubmitReplyPostsInReplyTo is the regression guard for replies that were
// accepted locally and then never appeared on the PR. GitHub's create-review-
// comment endpoint takes `in_reply_to`; go-github's PullRequestComment.InReplyTo
// serializes as `in_reply_to_id`, the name GitHub uses when *returning* a
// comment. Sending that spelling makes GitHub read the request as a new
// top-level comment with no anchor and reject it with a 422, so the reply was
// lost while the review around it went through.
func TestSubmitReplyPostsInReplyTo(t *testing.T) {
	var gotPath, gotMethod string
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decoding request body %q: %v", raw, err)
		}
		w.Header().Set("Content-Type", "application/json")
		created := `{"id":999,"in_reply_to_id":12345,"body":"looks good"}`
		if _, err := w.Write([]byte(created)); err != nil {
			t.Errorf("writing response: %v", err)
		}
	}))
	defer srv.Close()

	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parsing base URL: %v", err)
	}
	client := github.NewClient(nil)
	client.BaseURL = base

	if err := SubmitReply(client, "owner", "repo", 42, "looks good", 12345); err != nil {
		t.Fatalf("SubmitReply: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if want := "/repos/owner/repo/pulls/42/comments"; gotPath != want {
		t.Errorf("path = %s, want %s", gotPath, want)
	}
	if got, ok := body["in_reply_to"]; !ok || got != float64(12345) {
		t.Errorf("request body = %v, want in_reply_to 12345 (GitHub ignores in_reply_to_id on create)", body)
	}
	if _, ok := body["in_reply_to_id"]; ok {
		t.Errorf("request body carries in_reply_to_id, which the create endpoint ignores: %v", body)
	}
	if got := body["body"]; got != "looks good" {
		t.Errorf("body = %v, want %q", got, "looks good")
	}
}
