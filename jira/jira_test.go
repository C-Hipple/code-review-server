package jira

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestParsePRURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantRef   RepoRef
		wantNum   int
		wantError bool
	}{
		{
			name:    "standard github PR url",
			url:     "https://github.com/C-Hipple/code-review-server/pull/197",
			wantRef: RepoRef{Owner: "C-Hipple", Repo: "code-review-server"},
			wantNum: 197,
		},
		{
			name:    "trailing slash",
			url:     "https://github.com/C-Hipple/diff-lsp/pull/12/",
			wantRef: RepoRef{Owner: "C-Hipple", Repo: "diff-lsp"},
			wantNum: 12,
		},
		{
			name:    "enterprise host",
			url:     "https://git.example.com/platform/api/pull/8",
			wantRef: RepoRef{Owner: "platform", Repo: "api"},
			wantNum: 8,
		},
		{
			name:      "too short to parse",
			url:       "pull/3",
			wantError: true,
		},
		{
			name:      "non-numeric PR number",
			url:       "https://github.com/owner/repo/pull/abc",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, num, err := parsePRURL(tt.url)
			if tt.wantError {
				if err == nil {
					t.Fatalf("parsePRURL(%q) = %v, %d, want error", tt.url, ref, num)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePRURL(%q) returned unexpected error: %v", tt.url, err)
			}
			if ref != tt.wantRef {
				t.Errorf("ref = %v, want %v", ref, tt.wantRef)
			}
			if num != tt.wantNum {
				t.Errorf("num = %d, want %d", num, tt.wantNum)
			}
		})
	}
}

func TestTargetSetLookup(t *testing.T) {
	configured := RepoRef{Owner: "C-Hipple", Repo: "Code-Review-Server"}
	targets := newTargetSet([]RepoRef{configured, {Owner: "other-org", Repo: "code-review-server"}})

	// Jira reports whatever casing the PR was linked with; the configured ref
	// (with its original casing) is what comes back so it can be handed to the
	// GitHub API.
	got, ok := targets.lookup(RepoRef{Owner: "c-hipple", Repo: "code-review-server"})
	if !ok {
		t.Fatal("expected case-insensitive match")
	}
	if got != configured {
		t.Errorf("lookup returned %v, want the configured ref %v", got, configured)
	}

	// Same short name, different owner, resolves to the other entry.
	got, ok = targets.lookup(RepoRef{Owner: "other-org", Repo: "code-review-server"})
	if !ok {
		t.Fatal("expected match for the second owner")
	}
	if want := (RepoRef{Owner: "other-org", Repo: "code-review-server"}); got != want {
		t.Errorf("lookup returned %v, want %v", got, want)
	}

	if _, ok := targets.lookup(RepoRef{Owner: "C-Hipple", Repo: "diff-lsp"}); ok {
		t.Error("expected no match for a repo outside the target list")
	}
}

// devStatusServer serves the Jira dev-status endpoint, mapping issue ID to the
// PR URL linked from that issue.
func devStatusServer(t *testing.T, prsByIssue map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "dev-status") {
			http.NotFound(w, r)
			return
		}
		issueID := r.URL.Query().Get("issueId")
		prURL, ok := prsByIssue[issueID]
		if !ok {
			json.NewEncoder(w).Encode(DevStatusResponse{})
			return
		}
		json.NewEncoder(w).Encode(DevStatusResponse{
			Detail: []DevDetails{{
				PullRequests: []JiraPullRequestIdentifier{{URL: prURL, Status: "OPEN"}},
			}},
		})
	}))
}

func issues(ids ...string) JiraSearchResponse {
	resp := JiraSearchResponse{}
	for _, id := range ids {
		resp.Issues = append(resp.Issues, Issue{ID: id})
	}
	return resp
}

func TestProcessIssuesGroupsPRsAcrossRepos(t *testing.T) {
	server := devStatusServer(t, map[string]string{
		"1": "https://github.com/C-Hipple/code-review-server/pull/10",
		"2": "https://github.com/C-Hipple/diff-lsp/pull/20",
		"3": "https://github.com/C-Hipple/code-review-server/pull/11",
		// Linked to a repo the project doesn't span; must be dropped.
		"4": "https://github.com/C-Hipple/unrelated/pull/99",
		// No PR linked at all.
		"5": "",
	})
	defer server.Close()

	crs := RepoRef{Owner: "C-Hipple", Repo: "code-review-server"}
	diffLsp := RepoRef{Owner: "C-Hipple", Repo: "diff-lsp"}

	got := processIssues(server.URL, issues("1", "2", "3", "4", "5"), []RepoRef{crs, diffLsp})

	want := map[RepoRef][]int{
		crs:     {10, 11},
		diffLsp: {20},
	}
	for ref := range got {
		sort.Ints(got[ref])
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("processIssues() = %v, want %v", got, want)
	}
}

func TestProcessIssuesDistinguishesSameRepoNameAcrossOwners(t *testing.T) {
	server := devStatusServer(t, map[string]string{
		"1": "https://github.com/org-a/service/pull/1",
		"2": "https://github.com/org-b/service/pull/2",
	})
	defer server.Close()

	orgA := RepoRef{Owner: "org-a", Repo: "service"}
	orgB := RepoRef{Owner: "org-b", Repo: "service"}

	got := processIssues(server.URL, issues("1", "2"), []RepoRef{orgA, orgB})

	want := map[RepoRef][]int{
		orgA: {1},
		orgB: {2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("processIssues() = %v, want %v", got, want)
	}
}

func TestProcessIssuesExcludesUnconfiguredOwner(t *testing.T) {
	server := devStatusServer(t, map[string]string{
		"1": "https://github.com/fork-org/service/pull/1",
	})
	defer server.Close()

	got := processIssues(server.URL, issues("1"), []RepoRef{{Owner: "org-a", Repo: "service"}})

	if len(got) != 0 {
		t.Errorf("processIssues() = %v, want no results for an owner outside the target list", got)
	}
}

func TestRepoRefFullName(t *testing.T) {
	ref := RepoRef{Owner: "C-Hipple", Repo: "code-review-server"}
	if got, want := ref.FullName(), "C-Hipple/code-review-server"; got != want {
		t.Errorf("FullName() = %q, want %q", got, want)
	}
}

// Guard the URL shape processIssues depends on: the dev-status request must
// carry the issue ID as a query parameter.
func TestGetDevURLCarriesIssueID(t *testing.T) {
	raw := getDevURL("https://example.atlassian.net", "12345")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("getDevURL produced an unparseable URL %q: %v", raw, err)
	}
	if got := parsed.Query().Get("issueId"); got != "12345" {
		t.Errorf("issueId = %q, want %q", got, "12345")
	}
	if got := parsed.Query().Get("dataType"); got != "pullrequest" {
		t.Errorf("dataType = %q, want %q", got, "pullrequest")
	}
}
