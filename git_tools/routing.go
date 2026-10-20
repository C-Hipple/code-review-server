package git_tools

import (
	"crs/config"
	"errors"
	"net/http"

	"github.com/google/go-github/v74/github"
)

// GitHub answers much of what this package asks through either of two APIs,
// each metered against its own budget (RateResourceCore, RateResourceGraphQL)
// and each good at different things. REST answers one resource per request,
// but every GET is revalidated against what was fetched before
// (http_cache.go), and GitHub doesn't charge for a 304 Not Modified — asking
// again about something that hasn't changed is free. GraphQL can ask about
// many things in one request, and knows a few things REST doesn't, but every
// query is charged, changed or not.
//
// So each question both APIs can answer is a Lookup with an implementation on
// each, and RouteFor picks which one runs. A question only one API can answer
// has no Lookup and goes straight to that API: review-thread resolution
// (review_threads.go) exists only in GraphQL.

// API is one of GitHub's two APIs.
type API string

const (
	REST    API = config.GitHubAPIREST
	GraphQL API = config.GitHubAPIGraphQL
)

// Lookup is a question RouteFor can send to either API. Its value is the
// [GitHubAPI] config key that routes it.
type Lookup string

const (
	// LookupReviewRequestHistory is every reviewer a PR has been asked for
	// (GetReviewRequestHistory).
	LookupReviewRequestHistory Lookup = "ReviewRequestHistory"
	// LookupReactions is who reacted to each comment and review (GetReactions).
	LookupReactions Lookup = "Reactions"
	// LookupMergeability is whether each PR merges cleanly (GetMergeability).
	LookupMergeability Lookup = "Mergeability"
)

// RouteFor picks the API that answers lookup: the one the config's [GitHubAPI]
// table names for it, or REST when the table names none — or names something
// that isn't an API, which config validation reports.
//
// This is where a smarter policy belongs: say, sending lookups to GraphQL
// while the REST budget runs low (GetRateLimitStatusFor reports both), or
// batching mergeability through GraphQL once a review list grows past what a
// request per PR covers cheaply.
func RouteFor(lookup Lookup) API {
	if API(config.C().GitHubAPI.Routes()[string(lookup)]) == GraphQL {
		return GraphQL
	}
	return REST
}

// newRESTClient builds the REST client a routed lookup runs on. A seam for
// tests, which point it at a fake GitHub.
var newRESTClient = restClientFromToken

// restClientFromToken is GetGithubClient for a lookup: without a token it
// fails where GetGithubClient would exit the process, so a lookup that can't
// reach GitHub reports an error, as its GraphQL implementation does.
func restClientFromToken() (*github.Client, error) {
	httpClient, err := newAuthedHTTPClient(REST)
	if err != nil {
		return nil, err
	}
	return github.NewClient(httpClient), nil
}

// isNotFound reports whether err is GitHub answering 404: the thing asked
// about is gone, or the token can no longer see it.
func isNotFound(err error) bool {
	var ghErr *github.ErrorResponse
	return errors.As(err, &ghErr) && ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusNotFound
}
