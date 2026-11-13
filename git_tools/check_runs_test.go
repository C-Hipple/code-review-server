package git_tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/google/go-github/v74/github"
)

func checkRun(id int64, name string, app, suite int64, conclusion string) *github.CheckRun {
	return &github.CheckRun{
		ID:         github.Ptr(id),
		Name:       github.Ptr(name),
		App:        &github.App{ID: github.Ptr(app)},
		CheckSuite: &github.CheckSuite{ID: github.Ptr(suite)},
		Status:     github.Ptr("completed"),
		Conclusion: github.Ptr(conclusion),
	}
}

func runIDs(runs []*github.CheckRun) []int64 {
	ids := []int64{}
	for _, r := range runs {
		ids = append(ids, r.GetID())
	}
	return ids
}

func TestLatestCheckRunsKeepsNewestOfEachCheck(t *testing.T) {
	runs := []*github.CheckRun{
		checkRun(1, "test", 15, 100, "cancelled"),
		checkRun(2, "lint", 15, 100, "success"),
		nil,
		checkRun(3, "test", 15, 101, "success"),
		// Same name from another app is another check.
		checkRun(4, "test", 99, 200, "failure"),
	}
	got := runIDs(LatestCheckRuns(runs, nil))
	if want := []int64{2, 3, 4}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}

func TestLatestCheckRunsDropsSupersededSuites(t *testing.T) {
	// The first workflow run was cancelled by the second, which never ran
	// "extra": the cancelled "extra" is still not shown.
	runs := []*github.CheckRun{
		checkRun(1, "test", 15, 100, "cancelled"),
		checkRun(2, "extra", 15, 100, "cancelled"),
		checkRun(3, "required", 15, 100, "failure"),
		checkRun(4, "test", 15, 101, "success"),
		checkRun(5, "required", 15, 101, "success"),
		checkRun(6, "storybook", 7, 300, "success"),
	}
	got := runIDs(LatestCheckRuns(runs, map[int64]bool{100: true}))
	if want := []int64{4, 5, 6}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}

func TestSupersededSuites(t *testing.T) {
	run := func(id, workflow, suite int64, event string) *github.WorkflowRun {
		return &github.WorkflowRun{ID: github.Ptr(id), WorkflowID: github.Ptr(workflow), CheckSuiteID: github.Ptr(suite), Event: github.Ptr(event)}
	}
	got := supersededSuites([]*github.WorkflowRun{
		run(11, 1, 100, "pull_request"),
		run(12, 1, 101, "pull_request"),
		run(10, 1, 99, "pull_request"),
		// Another event of the same workflow is shown on its own.
		run(13, 1, 102, "push"),
		run(14, 2, 103, "pull_request"),
		nil,
	})
	want := map[int64]bool{99: true, 100: true}
	if len(got) != len(want) || !got[99] || !got[100] {
		t.Errorf("superseded %v, want %v", got, want)
	}
}

func checkRunsServer(t *testing.T, actions http.HandlerFunc) *github.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/commits/sha/check-runs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_count":4,"check_runs":[
			{"id":1,"name":"test","app":{"id":15},"check_suite":{"id":100},"status":"completed","conclusion":"cancelled"},
			{"id":2,"name":"extra","app":{"id":15},"check_suite":{"id":100},"status":"completed","conclusion":"cancelled"},
			{"id":3,"name":"test","app":{"id":15},"check_suite":{"id":101},"status":"completed","conclusion":"success"},
			{"id":4,"name":"lint","app":{"id":15},"check_suite":{"id":101},"status":"completed","conclusion":"success"}]}`)
	})
	mux.HandleFunc("/repos/o/r/actions/runs", actions)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base, _ := url.Parse(srv.URL + "/")
	c := github.NewClient(nil)
	c.BaseURL = base
	return c
}

func TestGetCheckRunsLeavesOutSupersededRuns(t *testing.T) {
	var asked string
	c := checkRunsServer(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("head_sha")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"total_count":2,"workflow_runs":[
			{"id":11,"workflow_id":1,"check_suite_id":100,"event":"pull_request"},
			{"id":12,"workflow_id":1,"check_suite_id":101,"event":"pull_request"}]}`)
	})
	got, err := GetCheckRuns(c, "o", "r", "sha")
	if err != nil {
		t.Fatal(err)
	}
	if asked != "sha" {
		t.Errorf("workflow runs asked for head_sha %q, want sha", asked)
	}
	if ids := runIDs(got.CheckRuns); !slices.Equal(ids, []int64{3, 4}) || got.GetTotal() != 2 {
		t.Errorf("got runs %v total %d, want [3 4] total 2", ids, got.GetTotal())
	}
}

func TestGetCheckRunsWithoutActionsAccess(t *testing.T) {
	c := checkRunsServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	})
	got, err := GetCheckRuns(c, "o", "r", "sha")
	if err != nil {
		t.Fatal(err)
	}
	// Only the re-run check is left out.
	if ids := runIDs(got.CheckRuns); !slices.Equal(ids, []int64{2, 3, 4}) {
		t.Errorf("got runs %v, want [2 3 4]", ids)
	}
}
