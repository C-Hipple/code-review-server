package workflows

import (
	"crs/config"
	"crs/database"
	"crs/git_tools"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/google/go-github/v74/github"
)

// TestMain keeps the package's tests off GitHub's GraphQL API. ProcessPRsDB
// looks up mergeability for the PRs it writes, and CRS_GITHUB_TOKEN is often
// set in dev shells, so a test that writes a section would otherwise make a
// live call. Tests of the lookup swap in their own (fakeMergeabilityLookup).
func TestMain(m *testing.M) {
	lookupMergeability = func([]git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
		return nil, errors.New("mergeability lookups are disabled in tests")
	}
	os.Exit(m.Run())
}

type mergeabilityAnswerer func(call int, refs []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error)

// fakeMergeabilityLookup answers lookups with answer — told which call this is,
// counting from 1 — skips the delay before the retry, and returns the PRs each
// call asked about.
func fakeMergeabilityLookup(t *testing.T, answer mergeabilityAnswerer) *[][]git_tools.PRRef {
	t.Helper()
	prevLookup, prevDelay := lookupMergeability, mergeabilityRetryDelay
	t.Cleanup(func() { lookupMergeability, mergeabilityRetryDelay = prevLookup, prevDelay })

	calls := [][]git_tools.PRRef{}
	lookupMergeability = func(refs []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
		calls = append(calls, refs)
		return answer(len(calls), refs)
	}
	mergeabilityRetryDelay = 0
	return &calls
}

// answerAll answers every PR asked about with state, for head "head-<number>".
func answerAll(state string) mergeabilityAnswerer {
	return func(_ int, refs []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
		answers := map[git_tools.PRRef]git_tools.Mergeability{}
		for _, ref := range refs {
			answers[ref] = git_tools.Mergeability{State: state, HeadSHA: fmt.Sprintf("head-%d", ref.Number)}
		}
		return answers, nil
	}
}

// mergeabilityTestPR is a PR of acme/widgets with head "head-<number>".
// mergeable is what the REST response carried: nil for one from the list
// endpoint, or from the single-PR endpoint while GitHub was still computing.
func mergeabilityTestPR(number int, state string, mergeable *bool) *github.PullRequest {
	pr := reprocessTestPR("acme", "widgets", number)
	pr.State = github.Ptr(state)
	pr.Head.SHA = github.Ptr(fmt.Sprintf("head-%d", number))
	pr.Mergeable = mergeable
	return pr
}

func widgetsRef(number int) git_tools.PRRef {
	return git_tools.PRRef{Owner: "acme", Repo: "widgets", Number: number}
}

func checkMergeability(t *testing.T, db *database.DB, number int, wantState, wantSHA string) {
	t.Helper()
	state, sha, err := db.GetPRMergeability(number, "widgets")
	if err != nil {
		t.Fatalf("GetPRMergeability(%d): %v", number, err)
	}
	if state != wantState || sha != wantSHA {
		t.Errorf("PR %d: recorded (%q, %q), want (%q, %q)", number, state, sha, wantState, wantSHA)
	}
}

func TestRecordMergeabilityLooksUpOnlyWhatThePRsDontCarry(t *testing.T) {
	db := warmTestDB(t)
	calls := fakeMergeabilityLookup(t, answerAll(git_tools.MergeabilityConflicting))

	recordMergeability(db, []*github.PullRequest{
		// From the single-PR endpoint, which answered already.
		mergeabilityTestPR(1, "open", github.Ptr(true)),
		mergeabilityTestPR(2, "open", github.Ptr(false)),
		// From the list endpoint, which never does.
		mergeabilityTestPR(3, "open", nil),
		mergeabilityTestPR(4, "open", nil),
		// Closed: no conflict worth showing, so nothing to ask.
		mergeabilityTestPR(5, "closed", nil),
	})

	if want := [][]git_tools.PRRef{{widgetsRef(3), widgetsRef(4)}}; !reflect.DeepEqual(*calls, want) {
		t.Errorf("looked up %v, want %v in one batch", *calls, want)
	}
	checkMergeability(t, db, 1, git_tools.MergeabilityMergeable, "head-1")
	checkMergeability(t, db, 2, git_tools.MergeabilityConflicting, "head-2")
	checkMergeability(t, db, 3, git_tools.MergeabilityConflicting, "head-3")
	checkMergeability(t, db, 4, git_tools.MergeabilityConflicting, "head-4")
	checkMergeability(t, db, 5, "", "")
}

// Asking is what sets GitHub computing an answer it doesn't have, so the PRs
// it answers unknown are asked about once more, and only those.
func TestRecordMergeabilityAsksAgainAboutUnknownAnswers(t *testing.T) {
	db := warmTestDB(t)
	// An answer from before the base branch moved, for the head PR 3 still has.
	if err := db.RecordPRMergeability(3, "widgets", "head-3", git_tools.MergeabilityConflicting); err != nil {
		t.Fatal(err)
	}
	calls := fakeMergeabilityLookup(t, func(call int, refs []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
		answers := map[git_tools.PRRef]git_tools.Mergeability{}
		for _, ref := range refs {
			state := git_tools.MergeabilityMergeable
			switch {
			case ref.Number == 2 && call == 1:
				state = git_tools.MergeabilityUnknown
			case ref.Number == 2:
				state = git_tools.MergeabilityConflicting
			case ref.Number == 3:
				// Still rechecking on the second ask.
				state = git_tools.MergeabilityUnknown
			}
			answers[ref] = git_tools.Mergeability{State: state, HeadSHA: fmt.Sprintf("head-%d", ref.Number)}
		}
		return answers, nil
	})

	recordMergeability(db, []*github.PullRequest{
		mergeabilityTestPR(1, "open", nil),
		mergeabilityTestPR(2, "open", nil),
		mergeabilityTestPR(3, "open", nil),
	})

	want := [][]git_tools.PRRef{
		{widgetsRef(1), widgetsRef(2), widgetsRef(3)},
		{widgetsRef(2), widgetsRef(3)},
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Errorf("looked up %v, want %v", *calls, want)
	}
	checkMergeability(t, db, 1, git_tools.MergeabilityMergeable, "head-1")
	checkMergeability(t, db, 2, git_tools.MergeabilityConflicting, "head-2")
	// Still unknown for the same head, so the conflict stands.
	checkMergeability(t, db, 3, git_tools.MergeabilityConflicting, "head-3")
}

func TestRecordMergeabilityKeepsTheLastAnswersWhenTheLookupFails(t *testing.T) {
	db := warmTestDB(t)
	if err := db.RecordPRMergeability(3, "widgets", "head-3", git_tools.MergeabilityConflicting); err != nil {
		t.Fatal(err)
	}
	calls := fakeMergeabilityLookup(t, func(int, []git_tools.PRRef) (map[git_tools.PRRef]git_tools.Mergeability, error) {
		return nil, errors.New("API rate limit exceeded")
	})

	recordMergeability(db, []*github.PullRequest{mergeabilityTestPR(3, "open", nil)})

	if len(*calls) != 1 {
		t.Errorf("looked up %d times, want once: a failed lookup is not retried", len(*calls))
	}
	checkMergeability(t, db, 3, git_tools.MergeabilityConflicting, "head-3")
}

// Every kind of workflow writes its section through ProcessPRsDB, so that is
// where the section's PRs get their mergeability checked.
func TestProcessPRsDBRecordsMergeability(t *testing.T) {
	db := warmTestDB(t)
	config.SetC(config.Config{DB: db})
	t.Cleanup(func() { config.SetC(config.Config{}) })
	seedReprocessAuxData(t, "acme", "widgets", 7)
	fakeMergeabilityLookup(t, answerAll(git_tools.MergeabilityConflicting))

	section, err := db.GetOrCreateSection("Needs Review", 0)
	if err != nil {
		t.Fatalf("GetOrCreateSection: %v", err)
	}
	changes := make(chan FileChanges, 4)
	var wg sync.WaitGroup
	result := ProcessPRsDB("needs_review", []*github.PullRequest{mergeabilityTestPR(7, "open", nil)},
		changes, db, section, &wg, false, false)

	if result.Added != 1 {
		t.Errorf("result = %+v, want the PR added", result)
	}
	checkMergeability(t, db, 7, git_tools.MergeabilityConflicting, "head-7")
}
