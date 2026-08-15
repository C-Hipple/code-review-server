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

// Shared plumbing for the handful of GitHub GraphQL calls this package makes.
// Everything else goes through go-github's REST client; GraphQL is reserved for
// the questions REST cannot answer — review-thread resolution state
// (review_threads.go) and the full review-request history (team_reviews.go).

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
	// trackBudget=false: GraphQL is metered separately from REST, so its
	// X-RateLimit-* headers must not overwrite the REST budget reading.
	httpClient, err := newAuthedHTTPClient(false)
	if err != nil {
		return err
	}

	body, err := json.Marshal(graphQLRequest{Query: query, Variables: variables})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, GraphQLEndpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github graphql returned %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var envelope struct {
		Errors []graphQLError `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err == nil && len(envelope.Errors) > 0 {
		msgs := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			msgs = append(msgs, e.Message)
		}
		return fmt.Errorf("github graphql error: %s", strings.Join(msgs, "; "))
	}

	return json.Unmarshal(respBody, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
