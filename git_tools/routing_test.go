package git_tools

import (
	"crs/config"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-github/v74/github"
)

// routeTo sends lookup to api for the rest of the test.
func routeTo(t *testing.T, lookup Lookup, api API) {
	t.Helper()
	prev := config.C()
	t.Cleanup(func() { config.SetC(prev) })

	cfg := config.C()
	switch lookup {
	case LookupReviewRequestHistory:
		cfg.GitHubAPI.ReviewRequestHistory = string(api)
	case LookupReactions:
		cfg.GitHubAPI.Reactions = string(api)
	case LookupMergeability:
		cfg.GitHubAPI.Mergeability = string(api)
	default:
		t.Fatalf("no [GitHubAPI] key routes %q", lookup)
	}
	config.SetC(cfg)
}

// withFakeREST points the routed lookups' REST client at handler, through the
// same transports — the response cache, the rate limiter — a real client goes
// through, and returns the fake's URL.
func withFakeREST(t *testing.T, handler http.Handler) string {
	t.Helper()
	t.Setenv("CRS_GITHUB_TOKEN", "test-token")
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parsing the fake's URL: %v", err)
	}

	prev := newRESTClient
	t.Cleanup(func() { newRESTClient = prev })
	newRESTClient = func() (*github.Client, error) {
		client, err := restClientFromToken()
		if err != nil {
			return nil, err
		}
		client.BaseURL = base
		return client, nil
	}
	return srv.URL
}

func TestRouteForDefaultsToREST(t *testing.T) {
	prev := config.C()
	t.Cleanup(func() { config.SetC(prev) })
	config.SetC(config.Config{})

	for _, lookup := range []Lookup{LookupReviewRequestHistory, LookupReactions, LookupMergeability} {
		if got := RouteFor(lookup); got != REST {
			t.Errorf("RouteFor(%s) = %q with no [GitHubAPI] table, want rest", lookup, got)
		}
	}
}

func TestRouteForFollowsTheConfig(t *testing.T) {
	routeTo(t, LookupReactions, GraphQL)

	if got := RouteFor(LookupReactions); got != GraphQL {
		t.Errorf("RouteFor(Reactions) = %q, want graphql", got)
	}
	// Routing one lookup leaves the others alone.
	if got := RouteFor(LookupMergeability); got != REST {
		t.Errorf("RouteFor(Mergeability) = %q, want rest", got)
	}
}

// A value that isn't an API is reported by config validation; at run time the
// lookup stays on the default rather than going nowhere.
func TestRouteForTreatsAnUnknownAPIAsREST(t *testing.T) {
	routeTo(t, LookupMergeability, API("GraphQL"))

	if got := RouteFor(LookupMergeability); got != REST {
		t.Errorf("RouteFor(Mergeability) = %q, want rest", got)
	}
}

// Every [GitHubAPI] key has a Lookup, and every Lookup a key: a mismatch would
// leave a lookup the config can't route, or a key that routes nothing.
func TestEveryLookupHasAConfigKey(t *testing.T) {
	lookups := map[string]bool{
		string(LookupReviewRequestHistory): true,
		string(LookupReactions):            true,
		string(LookupMergeability):         true,
	}
	routes := config.GitHubAPISettings{}.Routes()
	if len(routes) != len(lookups) {
		t.Errorf("[GitHubAPI] has %d keys, want one per lookup (%d)", len(routes), len(lookups))
	}
	for key := range routes {
		if !lookups[key] {
			t.Errorf("[GitHubAPI] key %q routes no Lookup", key)
		}
	}
}

// Without a token a REST lookup fails like its GraphQL implementation does,
// instead of exiting the process the way GetGithubClient would.
func TestRESTLookupsFailWithoutAToken(t *testing.T) {
	t.Setenv("CRS_GITHUB_TOKEN", "")
	prev := newRESTClient
	t.Cleanup(func() { newRESTClient = prev })
	newRESTClient = restClientFromToken

	if _, err := GetReviewRequestHistory("acme", "widgets", 7); err == nil {
		t.Error("GetReviewRequestHistory succeeded without a token")
	}
	if _, err := GetReactions("acme", "widgets", 7); err == nil {
		t.Error("GetReactions succeeded without a token")
	}
	if _, err := GetMergeability([]PRRef{{Owner: "acme", Repo: "widgets", Number: 7}}); err == nil {
		t.Error("GetMergeability succeeded without a token")
	}
}
