package git_tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Shared plumbing for the GitHub GraphQL calls this package makes. Most of
// what it asks goes through go-github's REST client, whose replies can be
// revalidated for free (http_cache.go); GraphQL can't be, so it answers only
// what REST can't — review-thread resolution (review_threads.go) — and the
// lookups the config routes to it (routing.go): the review-request history
// (team_reviews.go), who reacted to each comment (reactions.go) and whether
// each PR merges cleanly (mergeability.go). Each of those has a REST
// implementation alongside, and is answered by REST unless the config says
// otherwise.

// GraphQLEndpoint is the GitHub GraphQL URL. Overridable in tests.
var GraphQLEndpoint = "https://api.github.com/graphql"

type graphQLRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

type graphQLError struct {
	Message string `json:"message"`
}

// runGraphQL posts one query and unmarshals the response into out. A GraphQL
// `errors` array is returned as an error even though the HTTP status is 200,
// because a partially-resolved response is not something callers can use.
func runGraphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	respBody, err := postGraphQL(ctx, query, variables)
	if err != nil {
		return err
	}

	var envelope struct {
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err == nil && len(envelope.Errors) > 0 {
		return errorFromGraphQL(envelope.Errors)
	}

	return json.Unmarshal(respBody, out)
}

// postGraphQL posts one query and returns the body of GitHub's 200 response,
// errors array and all, for the caller to decode.
func postGraphQL(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	httpClient, err := newAuthedHTTPClient(GraphQL)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GraphQLEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github graphql returned %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	return respBody, nil
}

// errorFromGraphQL folds a response's errors array into one error, or nil when
// it is empty.
func errorFromGraphQL(errs []graphQLError) error {
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Message)
	}
	return fmt.Errorf("github graphql error: %s", strings.Join(msgs, "; "))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
