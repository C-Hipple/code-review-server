package git_tools

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withFakeGraphQL points GraphQLEndpoint at a test server and makes sure a token
// is set, so GetReviewThreads can build its authenticated client.
func withFakeGraphQL(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	prev := GraphQLEndpoint
	GraphQLEndpoint = srv.URL
	t.Cleanup(func() { GraphQLEndpoint = prev })
}

func TestGetReviewThreadsParsesResolutionState(t *testing.T) {
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[
				{"id":"T1","isResolved":true,"isOutdated":false,"path":"a.go",
				 "resolvedBy":{"login":"alice"},
				 "comments":{"nodes":[{"databaseId":11},{"databaseId":12}]}},
				{"id":"T2","isResolved":false,"isOutdated":true,"path":"b.go",
				 "resolvedBy":null,
				 "comments":{"nodes":[{"databaseId":21}]}}
			]}}}}}`)
	})

	threads, err := GetReviewThreads("o", "r", 7)
	if err != nil {
		t.Fatalf("GetReviewThreads returned an error: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(threads))
	}

	if !threads[0].IsResolved || threads[0].ResolvedBy != "alice" {
		t.Errorf("thread 0 = %+v, want resolved by alice", threads[0])
	}
	if len(threads[0].CommentIDs) != 2 || threads[0].CommentIDs[0] != 11 || threads[0].CommentIDs[1] != 12 {
		t.Errorf("thread 0 comment ids = %v, want [11 12]", threads[0].CommentIDs)
	}
	if threads[1].IsResolved || !threads[1].IsOutdated {
		t.Errorf("thread 1 = %+v, want unresolved and outdated", threads[1])
	}
	if threads[1].ResolvedBy != "" {
		t.Errorf("thread 1 resolved_by = %q, want empty for a null resolvedBy", threads[1].ResolvedBy)
	}
}

func TestGetReviewThreadsFollowsPagination(t *testing.T) {
	calls := 0
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		var req graphQLRequest
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &req)
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			if after := req.Variables["after"]; after != nil {
				t.Errorf("first page sent after=%v, want null", after)
			}
			io.WriteString(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{
				"pageInfo":{"hasNextPage":true,"endCursor":"CUR"},
				"nodes":[{"id":"T1","isResolved":true,"comments":{"nodes":[{"databaseId":1}]}}]}}}}}`)
			return
		}
		if got := req.Variables["after"]; got != "CUR" {
			t.Errorf("second page sent after=%v, want CUR", got)
		}
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{
			"pageInfo":{"hasNextPage":false,"endCursor":""},
			"nodes":[{"id":"T2","isResolved":false,"comments":{"nodes":[{"databaseId":2}]}}]}}}}}`)
	})

	threads, err := GetReviewThreads("o", "r", 7)
	if err != nil {
		t.Fatalf("GetReviewThreads returned an error: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("got %d threads across pages, want 2", len(threads))
	}
	if calls != 2 {
		t.Errorf("made %d requests, want 2", calls)
	}
}

func TestGetReviewThreadsSurfacesGraphQLErrors(t *testing.T) {
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors":[{"message":"Could not resolve to a Repository"}]}`)
	})

	if _, err := GetReviewThreads("o", "r", 7); err == nil {
		t.Fatal("expected an error when GraphQL returns errors, got nil")
	}
}

func TestGetReviewThreadsSurfacesHTTPErrors(t *testing.T) {
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"message":"Bad credentials"}`)
	})

	if _, err := GetReviewThreads("o", "r", 7); err == nil {
		t.Fatal("expected an error on a non-200 response, got nil")
	}
}
