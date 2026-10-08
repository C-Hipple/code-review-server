package git_tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-github/v74/github"
)

func TestGetReactionsOverGraphQLParsesUsersPerEmoji(t *testing.T) {
	routeTo(t, LookupReactions, GraphQL)
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{
			"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
				{"databaseId":900,
				 "reactionGroups":[
					{"content":"HOORAY","viewerHasReacted":false,"users":{"totalCount":1,"nodes":[{"login":"dana"}]}}],
				 "comments":{"nodes":[
					{"databaseId":11,
					 "reactionGroups":[
						{"content":"THUMBS_UP","viewerHasReacted":true,"users":{"totalCount":2,"nodes":[{"login":"alice"},{"login":"me"}]}},
						{"content":"EYES","viewerHasReacted":false,"users":{"totalCount":1,"nodes":[{"login":"bob"}]}},
						{"content":"ROCKET","viewerHasReacted":false,"users":{"totalCount":0,"nodes":[]}}]},
					{"databaseId":12,"reactionGroups":[]}]}}]},
			"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
				{"databaseId":55,
				 "reactionGroups":[
					{"content":"HEART","viewerHasReacted":false,"users":{"totalCount":1,"nodes":[{"login":"carol"}]}}]}]}
		}}}}`)
	})

	reactions, err := GetReactions("o", "r", 7)
	if err != nil {
		t.Fatalf("GetReactions returned an error: %v", err)
	}

	got := reactions.ForComment("11")
	if len(got) != 2 {
		t.Fatalf("comment 11 reactions = %+v, want 2 (the empty ROCKET group dropped)", got)
	}
	if got[0].Content != "+1" || got[0].Emoji != "👍" {
		t.Errorf("comment 11 first reaction = %+v, want the REST name and emoji for THUMBS_UP", got[0])
	}
	if got[0].Count != 2 || len(got[0].Users) != 2 || got[0].Users[0] != "alice" {
		t.Errorf("comment 11 first reaction users = %+v, want alice and me", got[0])
	}
	if !got[0].ViewerReacted {
		t.Error("comment 11 first reaction should report viewer_reacted")
	}
	if got[1].Content != "eyes" || got[1].Users[0] != "bob" {
		t.Errorf("comment 11 second reaction = %+v, want eyes by bob", got[1])
	}

	// A comment with an empty reactionGroups list is simply absent, so clients
	// can treat "no key" and "no reactions" the same way.
	if len(reactions.ForComment("12")) != 0 {
		t.Errorf("comment 12 = %+v, want no reactions", reactions.ForComment("12"))
	}
	if got := reactions.ForComment("55"); len(got) != 1 || got[0].Content != "heart" {
		t.Errorf("conversation comment 55 = %+v, want one heart", got)
	}
	if got := reactions.ForReview(900); len(got) != 1 || got[0].Content != "hooray" {
		t.Errorf("review 900 = %+v, want one hooray", got)
	}
	if reactions.Empty() {
		t.Error("Empty() should be false when reactions were found")
	}
}

// Reviews and conversation comments page independently, but share one request.
// A connection that has run out is asked for the page after its last cursor,
// which comes back empty rather than restarting at page one.
func TestGetReactionsOverGraphQLPagesBothConnections(t *testing.T) {
	routeTo(t, LookupReactions, GraphQL)
	var seen []map[string]any
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		seen = append(seen, req.Variables)

		w.Header().Set("Content-Type", "application/json")
		if len(seen) == 1 {
			io.WriteString(w, `{"data":{"repository":{"pullRequest":{
				"reviews":{"pageInfo":{"hasNextPage":true,"endCursor":"R1"},"nodes":[
					{"databaseId":900,"reactionGroups":[],"comments":{"nodes":[
						{"databaseId":11,"reactionGroups":[
							{"content":"THUMBS_UP","viewerHasReacted":false,"users":{"totalCount":1,"nodes":[{"login":"alice"}]}}]}]}}]},
				"comments":{"pageInfo":{"hasNextPage":false,"endCursor":"C1"},"nodes":[]}
			}}}}`)
			return
		}
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{
			"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":"R2"},"nodes":[
				{"databaseId":901,"reactionGroups":[
					{"content":"HEART","viewerHasReacted":false,"users":{"totalCount":1,"nodes":[{"login":"bob"}]}}],
				 "comments":{"nodes":[]}}]},
			"comments":{"pageInfo":{"hasNextPage":false,"endCursor":"C1"},"nodes":[]}
		}}}}`)
	})

	reactions, err := GetReactions("o", "r", 7)
	if err != nil {
		t.Fatalf("GetReactions returned an error: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("made %d requests, want 2", len(seen))
	}
	if seen[0]["reviewsAfter"] != nil || seen[0]["commentsAfter"] != nil {
		t.Errorf("first request cursors = %v, want both null", seen[0])
	}
	if seen[1]["reviewsAfter"] != "R1" {
		t.Errorf("second request reviewsAfter = %v, want R1", seen[1]["reviewsAfter"])
	}
	if seen[1]["commentsAfter"] != "C1" {
		t.Errorf("second request commentsAfter = %v, want the exhausted connection's last cursor C1", seen[1]["commentsAfter"])
	}
	if len(reactions.ForComment("11")) != 1 || len(reactions.ForReview(901)) != 1 {
		t.Errorf("reactions = %+v, want results accumulated across both pages", reactions)
	}
}

func TestGetReactionsOverGraphQLSurfacesGraphQLErrors(t *testing.T) {
	routeTo(t, LookupReactions, GraphQL)
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors":[{"message":"Could not resolve to a Repository"}]}`)
	})

	if _, err := GetReactions("o", "r", 7); err == nil {
		t.Fatal("GetReactions should return the GraphQL error, not an empty result")
	}
}

// An unrecognised content value (GitHub adding a new emoji) is passed through
// rather than dropped, so the reaction still shows up with a name.
func TestGetReactionsOverGraphQLKeepsUnknownContent(t *testing.T) {
	routeTo(t, LookupReactions, GraphQL)
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"repository":{"pullRequest":{
			"reviews":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]},
			"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[
				{"databaseId":55,"reactionGroups":[
					{"content":"PARTY_POPPER","viewerHasReacted":false,"users":{"totalCount":1,"nodes":[{"login":"carol"}]}}]}]}
		}}}}`)
	})

	reactions, err := GetReactions("o", "r", 7)
	if err != nil {
		t.Fatalf("GetReactions returned an error: %v", err)
	}
	got := reactions.ForComment("55")
	if len(got) != 1 || got[0].Content != "party_popper" || got[0].Emoji != "" {
		t.Errorf("unknown content = %+v, want it lowercased with no emoji", got)
	}
}

// asViewer makes login the account behind the token for the rest of the test,
// as GetAuthenticatedLogin reports it.
func asViewer(t *testing.T, login string) {
	t.Helper()
	GlobalCache.Set("authenticated_login", login, time.Hour)
	t.Cleanup(func() { GlobalCache.Set("authenticated_login", "", -1) })
}

// serveJSON answers path with body, and fails the test on any path it wasn't
// told about — over REST, a request for a comment nobody reacted to is the
// waste the per-emoji totals exist to avoid.
func serveJSON(t *testing.T, routes map[string]string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request for %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	})
}

func TestGetReactionsOverRESTAsksOnlyAboutReactedComments(t *testing.T) {
	asViewer(t, "me")
	withFakeREST(t, serveJSON(t, map[string]string{
		"/repos/o/r/pulls/7/comments": `[
			{"id":11,"reactions":{"total_count":3}},
			{"id":12,"reactions":{"total_count":0}}]`,
		"/repos/o/r/issues/7/comments": `[
			{"id":55,"reactions":{"total_count":1}},
			{"id":56}]`,
		"/repos/o/r/pulls/comments/11/reactions": `[
			{"content":"eyes","user":{"login":"bob"}},
			{"content":"+1","user":{"login":"alice"}},
			{"content":"+1","user":{"login":"Me"}}]`,
		"/repos/o/r/issues/comments/55/reactions": `[
			{"content":"heart","user":{"login":"carol"}}]`,
	}))

	reactions, err := GetReactions("o", "r", 7)
	if err != nil {
		t.Fatalf("GetReactions returned an error: %v", err)
	}

	// Grouped by emoji in GitHub's order, not the order the reactions came in.
	want := []Reaction{
		{Content: "+1", Emoji: "👍", Users: []string{"alice", "Me"}, Count: 2, ViewerReacted: true},
		{Content: "eyes", Emoji: "👀", Users: []string{"bob"}, Count: 1},
	}
	if got := reactions.ForComment("11"); !reflect.DeepEqual(got, want) {
		t.Errorf("comment 11 = %+v, want %+v", got, want)
	}
	if got := reactions.ForComment("55"); len(got) != 1 || got[0].Content != "heart" || got[0].ViewerReacted {
		t.Errorf("conversation comment 55 = %+v, want one heart, not the viewer's", got)
	}
	if got := reactions.ForComment("12"); len(got) != 0 {
		t.Errorf("comment 12 = %+v, want none", got)
	}
	// REST has no endpoint for reactions on a review's own body.
	if len(reactions.Reviews) != 0 {
		t.Errorf("reviews = %+v, want none over REST", reactions.Reviews)
	}
}

func TestGetReactionsOverRESTSkipsACommentDeletedMeanwhile(t *testing.T) {
	asViewer(t, "me")
	mux := http.NewServeMux()
	mux.Handle("/", serveJSON(t, map[string]string{
		"/repos/o/r/pulls/7/comments": `[
			{"id":11,"reactions":{"total_count":1}},
			{"id":13,"reactions":{"total_count":1}}]`,
		"/repos/o/r/issues/7/comments":           `[]`,
		"/repos/o/r/pulls/comments/13/reactions": `[{"content":"rocket","user":{"login":"dana"}}]`,
	}))
	mux.HandleFunc("/repos/o/r/pulls/comments/11/reactions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"Not Found"}`)
	})
	withFakeREST(t, mux)

	reactions, err := GetReactions("o", "r", 7)
	if err != nil {
		t.Fatalf("GetReactions returned an error: %v", err)
	}
	if got := reactions.ForComment("11"); len(got) != 0 {
		t.Errorf("deleted comment 11 = %+v, want nothing", got)
	}
	if got := reactions.ForComment("13"); len(got) != 1 || got[0].Content != "rocket" {
		t.Errorf("comment 13 = %+v, want one rocket", got)
	}
}

func TestGetReactionsOverRESTSurfacesErrors(t *testing.T) {
	asViewer(t, "me")
	mux := http.NewServeMux()
	mux.Handle("/", serveJSON(t, map[string]string{
		"/repos/o/r/pulls/7/comments":  `[{"id":11,"reactions":{"total_count":1}}]`,
		"/repos/o/r/issues/7/comments": `[]`,
	}))
	mux.HandleFunc("/repos/o/r/pulls/comments/11/reactions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"message":"Server Error"}`)
	})
	withFakeREST(t, mux)

	if _, err := GetReactions("o", "r", 7); err == nil {
		t.Fatal("GetReactions should return the error, not a partial result that looks complete")
	}
}

func TestGroupReactions(t *testing.T) {
	var reactions []*github.Reaction
	for i := range reactionUsersPerGroup + 5 {
		reactions = append(reactions, &github.Reaction{
			Content: github.Ptr("+1"),
			User:    &github.User{Login: github.Ptr(fmt.Sprintf("user%d", i))},
		})
	}
	reactions = append(reactions,
		&github.Reaction{Content: github.Ptr("party_popper"), User: &github.User{Login: github.Ptr("carol")}},
		&github.Reaction{Content: github.Ptr("-1"), User: &github.User{Login: github.Ptr("dave")}},
	)

	groups := groupReactions(reactions, "")
	if len(groups) != 3 {
		t.Fatalf("groups = %+v, want +1, -1 and the unknown one", groups)
	}
	if got := groups[0]; got.Content != "+1" || got.Count != reactionUsersPerGroup+5 || len(got.Users) != reactionUsersPerGroup {
		t.Errorf("+1 = %d users of %d, want the first %d named and all %d counted",
			len(got.Users), got.Count, reactionUsersPerGroup, reactionUsersPerGroup+5)
	}
	if groups[1].Content != "-1" {
		t.Errorf("second group = %q, want -1 in GitHub's order", groups[1].Content)
	}
	// A reaction GitHub adds later sorts last, keeping its name, with no emoji.
	if got := groups[2]; got.Content != "party_popper" || got.Emoji != "" {
		t.Errorf("unknown reaction = %+v, want party_popper with no emoji", got)
	}
}
