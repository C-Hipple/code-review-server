package ai

import (
	"context"
	"crs/config"
	"crs/llm"
	"errors"
	"strings"
	"testing"
)

func TestReviewEaseStoresTheRating(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{"REVIEW_EASE: medium\n"}}
	res, err := ReviewEase{}.Run(context.Background(), appliedRequest(orderingDiff, model))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report, ok := res.Report.(ReviewEaseReport); !ok || report.Rating != EaseMedium {
		t.Fatalf("report = %#v", res.Report)
	}
	if res.Body.BodyContent != "**Review ease: medium** — typical changes that need a careful read.\n" {
		t.Errorf("body = %q", res.Body.BodyContent)
	}
	if res.Log.Parsed != "review-ease medium" || !strings.HasPrefix(res.Log.Input, "3 file(s), ") {
		t.Errorf("log = %+v", res.Log)
	}

	// The prompt asks for the rating alone, never for an order.
	prompt := model.prompts[0]
	for _, want := range []string{
		"You are rating a pull request diff for a code reviewer.",
		`"easy" for small, mechanical, or repetitive changes; "medium" for typical changes that need a careful read; "hard" for large, subtle, or high-risk changes (tricky logic, concurrency, security, many interacting files).`,
		"Files in this diff:\n- main_test.go\n- main.go\n- helper.go\n",
		`Respond with ONLY a single line of exactly "REVIEW_EASE: <rating>"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "ordering the files") || strings.Contains(prompt, "file paths, one per line") {
		t.Error("the rating prompt must not ask for a file order")
	}
}

func TestReviewEaseFailures(t *testing.T) {
	t.Run("no diff", func(t *testing.T) {
		_, err := ReviewEase{}.Run(context.Background(), appliedRequest("", noModel{t}))
		wantStage(t, err, StageInput)
	})
	t.Run("model unavailable", func(t *testing.T) {
		model := &scriptedProvider{t: t, err: &llm.CallError{Stage: llm.StageHTTPStatus, Err: errors.New("quota exceeded")}}
		_, err := ReviewEase{}.Run(context.Background(), appliedRequest(orderingDiff, model))
		wantStage(t, err, llm.StageHTTPStatus)
	})
	t.Run("no usable rating", func(t *testing.T) {
		model := &scriptedProvider{t: t, answers: []string{"REVIEW_EASE: trivial\nmain.go\n"}}
		res, err := ReviewEase{}.Run(context.Background(), appliedRequest(orderingDiff, model))
		wantStage(t, err, llm.StageParse)
		if !strings.Contains(res.Log.ResponseSnippet, "REVIEW_EASE: trivial") {
			t.Errorf("an unreadable answer is logged for debugging: %+v", res.Log)
		}
	})
}

func TestParseReviewEase(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"rating line", "REVIEW_EASE: medium\n", "medium"},
		{"rating line among paths", "main.go\nREVIEW_EASE: hard\nmain_test.go\n", "hard"},
		{"decorated", "  **REVIEW_EASE: Easy**  \n", "easy"},
		{"prefix is case-insensitive", "Review_Ease: hard\n", "hard"},
		{"rating followed by commentary", "REVIEW_EASE: medium — several interacting files\n", "medium"},
		{"invalid rating", "REVIEW_EASE: trivial\n", ""},
		{"bare rating without the prefix", "easy\n", ""},
		{"the last usable line wins", "REVIEW_EASE: easy\nREVIEW_EASE: hard\nREVIEW_EASE: unsure\n", "hard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseReviewEase(tt.text); got != tt.want {
				t.Errorf("parseReviewEase(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestNormalizeReviewEase(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"easy", "easy"},
		{" Medium ", "medium"},
		{"\"hard\"", "hard"},
		{"**easy**", "easy"},
		{"hard - tricky concurrency", "hard"},
		{"medium (many files touched)", "medium"},
		{"easy.", "easy"},
		{"hardly", ""},
		{"trivial", ""},
		{"", ""},
	}
	for i, tc := range tests {
		if got := normalizeReviewEase(tc.in); got != tc.want {
			t.Errorf("[%d] normalizeReviewEase(%q) = %q, want %q", i, tc.in, got, tc.want)
		}
	}
}

func TestStoredReviewEaseReadsWhatRunStored(t *testing.T) {
	encoded, err := Result{Report: ReviewEaseReport{Rating: EaseHard}}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if got := StoredReviewEase(encoded); got != EaseHard {
		t.Errorf("StoredReviewEase = %q", got)
	}
	bogus, _ := Result{Report: ReviewEaseReport{Rating: "trivial"}}.Encode()
	failed, _ := Result{Body: Body{BodyContent: "**Review ease failed.**"}}.Encode()
	for _, stored := range []string{"", "not json", bogus, failed} {
		if got := StoredReviewEase(stored); got != "" {
			t.Errorf("StoredReviewEase(%q) = %q, want none", stored, got)
		}
	}
}

// TestAppliedFeaturesThroughTheRunner runs both features the way the server
// does, on a provider that can't be built: the run fails at client-init, the
// call log says why, and the stored error holds no order or rating.
func TestAppliedFeaturesThroughTheRunner(t *testing.T) {
	r, db, crsHome := testRunner(t, nil, FileOrdering{}, ReviewEase{})
	t.Setenv("GEMINI_API_KEY", "")
	r.NewProvider = NewProvider
	build := func(ctx context.Context) (Request, error) {
		return Request{Owner: "acme", Repo: "widgets", Number: 42, HeadSHA: "sha-1", Diff: orderingDiff}, nil
	}

	for _, id := range []string{FileOrderingID, ReviewEaseID} {
		j := job(id, TriggerAutomatic, "sha-1", "digest-1")
		j.Build = build
		if got := r.RunSync(j); got != OutcomeStarted {
			t.Fatalf("%s: RunSync = %q", id, got)
		}
		stored, ok, err := db.GetAIResult("acme", "widgets", 42, id)
		if err != nil || !ok {
			t.Fatalf("%s: nothing stored (err %v)", id, err)
		}
		if stored.Status != StatusError || stored.SHA != "sha-1" || stored.InputHash != CodeOnlyDigest {
			t.Errorf("%s: stored %+v", id, stored)
		}
		if StoredFileOrdering(stored.Result) != nil || StoredReviewEase(stored.Result) != "" {
			t.Errorf("%s: a failed run stores no order or rating", id)
		}
	}

	log := callLog(t, crsHome)
	for _, want := range []string{
		"Purpose:  ai:file-ordering (oneshot)",
		"Purpose:  ai:review-ease (oneshot)",
		"Trigger:  automatic",
		"LLM:      gemini (unavailable: GEMINI_API_KEY not set)",
		`Outcome:  FAILURE at stage "client-init"`,
		"Input:    3 file(s), ",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("call log missing %q:\n%s", want, log)
		}
	}
}

func TestAppliedFeaturesAreOneShotAndCodeOnly(t *testing.T) {
	for _, f := range []Feature{FileOrdering{}, ReviewEase{}} {
		if !IsApplied(f) || KeyDigest(f, "digest") != CodeOnlyDigest {
			t.Errorf("%s should be applied and code-only", f.ID())
		}
		if strings.Join(modesOf(f), ",") != config.AIModeOneShot {
			t.Errorf("%s modes = %v", f.ID(), modesOf(f))
		}
	}
	if IsApplied(CommentsAddressed{}) || IsApplied(FeatureFlags{}) {
		t.Error("the report features are not applied")
	}
}
