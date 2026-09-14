package git_tools

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func TestGetReactionsParsesUsersPerEmoji(t *testing.T) {
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
func TestGetReactionsPagesBothConnections(t *testing.T) {
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

func TestGetReactionsSurfacesGraphQLErrors(t *testing.T) {
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
func TestGetReactionsKeepsUnknownContent(t *testing.T) {
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
