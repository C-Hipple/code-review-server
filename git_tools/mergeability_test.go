package git_tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-github/v74/github"
)

// fakeMergeability answers mergeability queries the way GitHub does, from
// states keyed by PR: a PR with no state fails its own lookup, as GitHub fails
// one for a PR that doesn't exist, and the rest are still answered. Each PR's
// head SHA is "sha-<number>". It returns how many PRs each request asked about.
func fakeMergeability(t *testing.T, states map[PRRef]string) *[]int {
	t.Helper()
	batches := []int{}
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req graphQLRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		data := map[string]any{}
		errs := []map[string]any{}
		asked := 0
		for ; ; asked++ {
			owner, ok := req.Variables[fmt.Sprintf("owner%d", asked)]
			if !ok {
				break
			}
			alias := fmt.Sprintf("pr%d", asked)
			if !strings.Contains(req.Query, alias+": repository(") {
				t.Errorf("query has no lookup aliased %s:\n%s", alias, req.Query)
			}
			ref := PRRef{
				Owner:  owner.(string),
				Repo:   req.Variables[fmt.Sprintf("name%d", asked)].(string),
				Number: int(req.Variables[fmt.Sprintf("number%d", asked)].(float64)),
			}
			state, ok := states[ref]
			if !ok {
				data[alias] = map[string]any{"pullRequest": nil}
				errs = append(errs, map[string]any{
					"type":    "NOT_FOUND",
					"path":    []string{alias, "pullRequest"},
					"message": fmt.Sprintf("Could not resolve to a PullRequest with the number of %d.", ref.Number),
				})
				continue
			}
			data[alias] = map[string]any{"pullRequest": map[string]any{
				"headRefOid": fmt.Sprintf("sha-%d", ref.Number),
				"mergeable":  state,
			}}
		}
		batches = append(batches, asked)

		resp := map[string]any{"data": data}
		if len(errs) > 0 {
			resp["errors"] = errs
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	return &batches
}

func TestGetMergeabilityOverGraphQLReadsEachPRsAnswer(t *testing.T) {
	routeTo(t, LookupMergeability, GraphQL)
	clean := PRRef{Owner: "acme", Repo: "widgets", Number: 1}
	conflicted := PRRef{Owner: "acme", Repo: "widgets", Number: 2}
	computing := PRRef{Owner: "acme", Repo: "gadgets", Number: 3}
	// Deleted since it was listed: GitHub fails this one lookup and answers
	// the others.
	gone := PRRef{Owner: "acme", Repo: "widgets", Number: 404}
	fakeMergeability(t, map[PRRef]string{
		clean:      "MERGEABLE",
		conflicted: "CONFLICTING",
		computing:  "UNKNOWN",
	})

	answers, err := GetMergeability([]PRRef{clean, conflicted, computing, gone})
	if err != nil {
		t.Fatalf("GetMergeability: %v", err)
	}
	want := map[PRRef]Mergeability{
		clean:      {State: MergeabilityMergeable, HeadSHA: "sha-1"},
		conflicted: {State: MergeabilityConflicting, HeadSHA: "sha-2"},
		computing:  {State: MergeabilityUnknown, HeadSHA: "sha-3"},
	}
	if !reflect.DeepEqual(answers, want) {
		t.Errorf("answers = %+v, want %+v", answers, want)
	}
}

func TestGetMergeabilityOverGraphQLBatchesALongList(t *testing.T) {
	routeTo(t, LookupMergeability, GraphQL)
	states := map[PRRef]string{}
	refs := []PRRef{}
	total := 2*mergeabilityBatchSize + 7
	for n := 1; n <= total; n++ {
		ref := PRRef{Owner: "acme", Repo: "widgets", Number: n}
		states[ref] = "MERGEABLE"
		refs = append(refs, ref)
	}
	// A PR in two sections is listed twice but asked about once.
	refs = append(refs, refs[0])
	batches := fakeMergeability(t, states)

	answers, err := GetMergeability(refs)
	if err != nil {
		t.Fatalf("GetMergeability: %v", err)
	}
	if len(answers) != total {
		t.Errorf("got %d answers, want %d", len(answers), total)
	}
	if want := []int{mergeabilityBatchSize, mergeabilityBatchSize, 7}; !reflect.DeepEqual(*batches, want) {
		t.Errorf("requests asked about %v PRs, want %v", *batches, want)
	}
}

func TestGetMergeabilityOverGraphQLFailsWhenARequestAnswersNothing(t *testing.T) {
	routeTo(t, LookupMergeability, GraphQL)
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
	})

	answers, err := GetMergeability([]PRRef{{Owner: "acme", Repo: "widgets", Number: 1}})
	if err == nil || !strings.Contains(err.Error(), "API rate limit exceeded") {
		t.Fatalf("err = %v, want the rate limit error", err)
	}
	if len(answers) != 0 {
		t.Errorf("answers = %+v, want none", answers)
	}
}

func TestGetMergeabilityOverGraphQLKeepsTheBatchesBeforeAFailure(t *testing.T) {
	routeTo(t, LookupMergeability, GraphQL)
	requests := 0
	withFakeGraphQL(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			http.Error(w, "boom", http.StatusBadGateway)
			return
		}
		data := map[string]any{}
		for i := 0; i < mergeabilityBatchSize; i++ {
			data[fmt.Sprintf("pr%d", i)] = map[string]any{"pullRequest": map[string]any{
				"headRefOid": "sha", "mergeable": "CONFLICTING",
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})

	refs := []PRRef{}
	for n := 1; n <= mergeabilityBatchSize+1; n++ {
		refs = append(refs, PRRef{Owner: "acme", Repo: "widgets", Number: n})
	}
	answers, err := GetMergeability(refs)
	if err == nil {
		t.Fatal("GetMergeability succeeded although its second request failed")
	}
	if len(answers) != mergeabilityBatchSize {
		t.Errorf("got %d answers, want the first batch's %d", len(answers), mergeabilityBatchSize)
	}
	if got := answers[refs[0]]; got.State != MergeabilityConflicting {
		t.Errorf("first PR = %+v, want conflicting", got)
	}
}

func TestRESTMergeability(t *testing.T) {
	cases := []struct {
		name string
		pr   *github.PullRequest
		want string
	}{
		{"merges cleanly", &github.PullRequest{Mergeable: github.Ptr(true)}, MergeabilityMergeable},
		{"conflicts", &github.PullRequest{Mergeable: github.Ptr(false)}, MergeabilityConflicting},
		{"still computing, or from the list endpoint", &github.PullRequest{}, MergeabilityUnknown},
		{"no PR", nil, MergeabilityUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RESTMergeability(tc.pr); got != tc.want {
				t.Errorf("RESTMergeability = %q, want %q", got, tc.want)
			}
		})
	}
}

// fakePullRequests serves the single-PR endpoint from mergeable, keyed by PR:
// the REST mergeable value each carries (nil while GitHub is computing), with
// head "sha-<number>". A PR missing from the map is a 404. Every reply carries
// an ETag, and a request repeating it is answered 304, as GitHub does. It
// returns how many replies were sent in full and how many were 304s.
func fakePullRequests(t *testing.T, mergeable map[PRRef]*bool) (full, notModified *atomic.Int64) {
	t.Helper()
	full, notModified = &atomic.Int64{}, &atomic.Int64{}
	withFakeREST(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /repos/{owner}/{repo}/pulls/{number}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		number, err := strconv.Atoi(parts[len(parts)-1])
		if len(parts) != 5 || parts[0] != "repos" || parts[3] != "pulls" || err != nil {
			t.Errorf("unexpected request for %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		state, ok := mergeable[PRRef{Owner: parts[1], Repo: parts[2], Number: number}]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		body, _ := json.Marshal(map[string]any{
			"number":    number,
			"state":     "open",
			"mergeable": state,
			"head":      map[string]any{"sha": fmt.Sprintf("sha-%d", number)},
		})
		etag := fmt.Sprintf(`W/"%x"`, sha256.Sum256(body))
		if r.Header.Get("If-None-Match") == etag {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	return full, notModified
}

func TestGetMergeabilityOverRESTReadsEachPRsAnswer(t *testing.T) {
	clean := PRRef{Owner: "acme", Repo: "widgets", Number: 1}
	conflicted := PRRef{Owner: "acme", Repo: "widgets", Number: 2}
	computing := PRRef{Owner: "acme", Repo: "gadgets", Number: 3}
	// Deleted since it was listed: absent from the answers, the rest answered.
	gone := PRRef{Owner: "acme", Repo: "widgets", Number: 404}
	fakePullRequests(t, map[PRRef]*bool{
		clean:      github.Ptr(true),
		conflicted: github.Ptr(false),
		computing:  nil,
	})

	answers, err := GetMergeability([]PRRef{clean, conflicted, computing, gone, clean})
	if err != nil {
		t.Fatalf("GetMergeability: %v", err)
	}
	want := map[PRRef]Mergeability{
		clean:      {State: MergeabilityMergeable, HeadSHA: "sha-1"},
		conflicted: {State: MergeabilityConflicting, HeadSHA: "sha-2"},
		computing:  {State: MergeabilityUnknown, HeadSHA: "sha-3"},
	}
	if !reflect.DeepEqual(answers, want) {
		t.Errorf("answers = %+v, want %+v", answers, want)
	}
}

// The case for REST: the second time a cycle asks about PRs nothing has
// happened to, every reply is a 304, which GitHub doesn't charge for.
func TestGetMergeabilityOverRESTRevalidatesWhatItAskedBefore(t *testing.T) {
	refs := []PRRef{}
	mergeable := map[PRRef]*bool{}
	for n := 1; n <= 5; n++ {
		ref := PRRef{Owner: "acme", Repo: "revalidated", Number: n}
		refs = append(refs, ref)
		mergeable[ref] = github.Ptr(n%2 == 0)
	}
	full, notModified := fakePullRequests(t, mergeable)

	first, err := GetMergeability(refs)
	if err != nil {
		t.Fatalf("first GetMergeability: %v", err)
	}
	second, err := GetMergeability(refs)
	if err != nil {
		t.Fatalf("second GetMergeability: %v", err)
	}
	if !reflect.DeepEqual(first, second) || len(second) != len(refs) {
		t.Errorf("second answers = %+v, want the first's %+v", second, first)
	}
	if full.Load() != int64(len(refs)) || notModified.Load() != int64(len(refs)) {
		t.Errorf("GitHub sent %d full replies and %d 304s, want %d of each",
			full.Load(), notModified.Load(), len(refs))
	}
}

// Once a request fails outright, the PRs not yet asked about aren't: whatever
// broke it — usually a spent budget — would break them too.
func TestGetMergeabilityOverRESTStopsAskingAfterAFailure(t *testing.T) {
	requests := atomic.Int64{}
	withFakeREST(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, `{"message":"Server Error"}`)
	}))

	refs := []PRRef{}
	for n := 1; n <= 3*prFetchConcurrency; n++ {
		refs = append(refs, PRRef{Owner: "acme", Repo: "widgets", Number: n})
	}
	answers, err := GetMergeability(refs)
	if err == nil {
		t.Fatal("GetMergeability succeeded although every request failed")
	}
	if len(answers) != 0 {
		t.Errorf("answers = %+v, want none", answers)
	}
	// The first batch of concurrent requests is already in flight when the
	// first failure lands; nothing after it is sent.
	if got := requests.Load(); got > prFetchConcurrency {
		t.Errorf("sent %d requests, want at most the %d in flight when the first failed", got, prFetchConcurrency)
	}
}
