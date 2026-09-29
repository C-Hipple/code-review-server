package ai

import (
	"context"
	"crs/llm"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

// ReviewEaseID is the ID of the review-ease feature.
const ReviewEaseID = "review-ease"

// Review-ease ratings.
const (
	EaseEasy   = "easy"
	EaseMedium = "medium"
	EaseHard   = "hard"
)

// easeMeanings says what each rating stands for, as the prompt defines it.
var easeMeanings = map[string]string{
	EaseEasy:   "small, mechanical, or repetitive changes",
	EaseMedium: "typical changes that need a careful read",
	EaseHard:   "large, subtle, or high-risk changes (tricky logic, concurrency, security, many interacting files)",
}

const reviewEaseLinePrefix = "REVIEW_EASE:"

// ReviewEase rates how easy a PR is to review: easy, medium or hard.
//
// It is an applied feature: the server shows the rating beside the PR in the
// review list (review_ease, and a headline tag in the org content) and in the
// PR's metadata. The rating depends on the code alone, so it is keyed by the
// head SHA only; after a push, the PR keeps showing its last rating until the
// new head's is stored.
type ReviewEase struct{}

func (ReviewEase) ID() string   { return ReviewEaseID }
func (ReviewEase) Name() string { return "Review ease" }

func (ReviewEase) Description() string {
	return "Rates how easy the PR is to review — easy, medium or hard — from its diff. The rating shows " +
		"beside the PR in the review list."
}

// CodeOnly keys the feature's results by the head SHA alone.
func (ReviewEase) CodeOnly() bool { return true }

// Applied marks the rating as something the server shows with the PR, not a
// report.
func (ReviewEase) Applied() bool { return true }

// ReviewEaseReport is the typed report review-ease stores as the result's
// "report".
type ReviewEaseReport struct {
	// Rating is easy, medium or hard.
	Rating string `json:"rating"`
}

func (ReviewEase) Run(ctx context.Context, req Request) (Result, error) {
	in, err := readDiffInput(req.Diff)
	if err != nil {
		return Result{}, err
	}
	runLog := RunLog{Input: in.describe()}

	text, err := req.Model.Generate(ctx, buildReviewEasePrompt(in))
	if err != nil {
		return Result{Log: runLog}, err
	}
	rating := parseReviewEase(text)
	if rating == "" {
		runLog.Parsed = "no review-ease rating"
		runLog.ResponseSnippet = llm.Snippet(text)
		return Result{Log: runLog}, &llm.CallError{Stage: llm.StageParse, Err: fmt.Errorf("response contained no usable review-ease rating")}
	}
	runLog.Parsed = "review-ease " + rating
	return Result{
		Body: Body{BodyType: BodyMarkdown,
			BodyContent: fmt.Sprintf("**Review ease: %s** — %s.\n", rating, easeMeanings[rating])},
		Report:    ReviewEaseReport{Rating: rating},
		Truncated: in.truncated,
		Log:       runLog,
	}, nil
}

// buildReviewEasePrompt asks the model to rate the diff.
func buildReviewEasePrompt(in diffInput) string {
	var b strings.Builder
	b.WriteString(`You are rating a pull request diff for a code reviewer.
`)
	b.WriteString(fmt.Sprintf(`
Rate how easy this pull request is to review: "easy" for %s; "medium" for %s; "hard" for %s.
`, easeMeanings[EaseEasy], easeMeanings[EaseMedium], easeMeanings[EaseHard]))
	b.WriteString(fmt.Sprintf(`
Files in this diff:
%s
Full diff:
%s
`, in.fileList, in.text))
	b.WriteString(`
Respond with ONLY a single line of exactly "REVIEW_EASE: <rating>" where <rating> is easy, medium, or hard. Do not include anything else.`)
	return b.String()
}

// parseReviewEase reads the rating off the model's REVIEW_EASE line; the last
// usable one wins. It returns "" when no line carries a usable rating.
func parseReviewEase(text string) string {
	rating := ""
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := reviewEaseLine(line); ok {
			if ease := normalizeReviewEase(rest); ease != "" {
				rating = ease
			}
		}
	}
	return rating
}

// reviewEaseLine reports whether line is a REVIEW_EASE line and returns what
// follows the prefix. Bold or backticks around the line don't matter, and nor
// does the prefix's case, so a model that answers "Review_Ease: easy" still
// parses.
func reviewEaseLine(line string) (string, bool) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), "`*"))
	if len(s) >= len(reviewEaseLinePrefix) && strings.EqualFold(s[:len(reviewEaseLinePrefix)], reviewEaseLinePrefix) {
		return s[len(reviewEaseLinePrefix):], true
	}
	return "", false
}

// normalizeReviewEase maps a model's rating string onto one of the canonical
// ratings ("easy", "medium", or "hard"); it returns an empty string for
// anything else. The rating only has to lead the string — "hard - tricky
// concurrency" still counts — so mild chattiness doesn't drop the rating.
func normalizeReviewEase(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`*")
	s = strings.ToLower(strings.TrimSpace(s))
	for _, rating := range []string{EaseEasy, EaseMedium, EaseHard} {
		if s == rating {
			return rating
		}
		// Accept a leading rating only as a whole word, so e.g. "hardly"
		// does not count as "hard".
		if strings.HasPrefix(s, rating) {
			next := rune(s[len(rating)])
			if !unicode.IsLetter(next) && !unicode.IsDigit(next) {
				return rating
			}
		}
	}
	return ""
}

// StoredReviewEase reads the rating out of a stored review-ease result (as
// Result.Encode wrote it): easy, medium or hard, or "" when it holds none.
func StoredReviewEase(stored string) string {
	var report ReviewEaseReport
	doc := DecodeDocument(stored)
	if len(doc.Report) == 0 || json.Unmarshal(doc.Report, &report) != nil {
		return ""
	}
	if _, ok := easeMeanings[report.Rating]; !ok {
		return ""
	}
	return report.Rating
}
