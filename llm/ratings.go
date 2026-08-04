// Package llm implements the non-plugin LLM calls made by the server —
// today, the experimental diff analysis that orders a PR's files for review
// and rates how easy the PR is to review ("easy", "medium", or "hard").
//
// The concrete backend is hidden behind the Client interface so the provider
// can one day be swapped out; the only implementation today is the Gemini
// client in gemini.go.
//
// Every call — including ones that fail before reaching the API — is
// appended to ~/.crs/llm_calls.log (see call_log.go) so it's possible to see
// after the fact why a call failed or why its response couldn't be parsed
// (e.g. why a PR never got its review-ease tag).
package llm

import (
	"crs/config"
	"crs/utils"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Client is the interface to an LLM text-generation backend. It exists so
// the provider behind the non-plugin LLM calls can be swapped out (or faked
// in tests) without touching the callers.
type Client interface {
	// Provider identifies the backend, e.g. "gemini".
	Provider() string
	// Model identifies the specific model, e.g. "gemini-2.5-flash".
	Model() string
	// Generate sends a prompt and returns the model's text response. Errors
	// should be *CallError values so the call log can attribute the failure
	// to a stage.
	Generate(prompt string) (string, error)
}

// DefaultClient returns the LLM backend used for non-plugin calls. Today
// that is always Gemini; this is the single place to swap in another
// provider.
func DefaultClient() (Client, error) {
	return NewGeminiClient()
}

// newClient is what analyzeDiff uses to build its backend; tests swap it out
// to fake the LLM.
var newClient = DefaultClient

// Stages an LLM call can fail at, recorded in the call log so a failure can
// be attributed to the step that produced it.
const (
	StageClientInit    = "client-init"    // building the client (e.g. missing API key)
	StageRequest       = "request"        // sending the request (network error, timeout)
	StageHTTPStatus    = "http-status"    // non-200 response from the API
	StageDecode        = "decode"         // malformed response body
	StageEmptyResponse = "empty-response" // well-formed response with no content
	StageParse         = "parse"          // response text lacked what we asked for
)

// CallError is an error from an LLM call attributed to the stage it failed
// at.
type CallError struct {
	Stage string
	Err   error
}

func (e *CallError) Error() string { return e.Err.Error() }
func (e *CallError) Unwrap() error { return e.Err }

const (
	// maxDiffSize caps how much of the diff is sent to the LLM.
	maxDiffSize = 200000

	reviewEaseLinePrefix = "REVIEW_EASE:"
)

// DiffAnalysis holds the results of the single LLM call made per PR SHA:
// the display ordering of the diff files and, when enabled, the review-ease
// rating.
type DiffAnalysis struct {
	Ordering   []string
	ReviewEase string
}

// Triggers recorded in the call log, naming what caused an analysis call.
const (
	// TriggerPostUpdateHook is the analysis the post-update hook runs after a
	// PR is fetched or its head SHA changes.
	TriggerPostUpdateHook = "post-update hook"
	// TriggerRender is the analysis a render kicks off in the background when
	// it finds no cached ordering. The render itself does not wait for it.
	TriggerRender = "render (background)"
)

// inflightAnalysis tracks the (repo, PR, SHA) triples an analysis is currently
// running for, so the render-triggered dispatch and the post-update hook
// firing for the same revision collapse into a single LLM call instead of two.
var inflightAnalysis sync.Map // "repo#pr@sha" -> struct{}

// EnsureDiffAnalysis makes sure the LLM-derived values enabled in config
// (the diff file ordering and the review-ease rating) are cached for the PR
// SHA, calling the LLM at most once if anything is missing. It blocks for the
// duration of the LLM call, so callers on a request path must run it in a
// goroutine. Concurrent calls for the same PR SHA collapse into one: the
// first runs the analysis and the rest return immediately.
func EnsureDiffAnalysis(files []*utils.DiffFile, repo string, prNumber int, sha, trigger string) {
	cfg := config.C()
	needOrdering := cfg.ExperimentalLLMFileOrdering && len(files) >= 2 &&
		cachedFileOrdering(repo, prNumber, sha) == nil
	needEase := cfg.ExperimentalLLMReviewEase && cachedReviewEase(repo, prNumber, sha) == ""
	if !needOrdering && !needEase {
		return
	}

	key := fmt.Sprintf("%s#%d@%s", repo, prNumber, sha)
	if _, running := inflightAnalysis.LoadOrStore(key, struct{}{}); running {
		slog.Debug("LLM diff analysis already running for revision, skipping", "repo", repo, "pr", prNumber, "sha", sha)
		return
	}
	defer inflightAnalysis.Delete(key)

	ctx := callContext{repo: repo, prNumber: prNumber, sha: sha, trigger: trigger}
	analysis, err := analyzeDiff(files, needOrdering, needEase, ctx)
	if err != nil {
		slog.Warn("LLM diff analysis failed", "repo", repo, "pr", prNumber, "error", err)
		return
	}
	storeDiffAnalysis(repo, prNumber, sha, analysis)
}

// CachedOrderedDiffFiles returns files in the LLM-derived display order for
// the PR SHA when that ordering is already cached. It never calls the LLM:
// rendering a PR is on the critical path of opening a review, and an LLM
// round trip there is a delay the reviewer waits on. Producing the ordering
// is EnsureDiffAnalysis's job, which callers run in the background.
//
// The second return is false when nothing is cached and the caller should
// fall back to its default sort.
func CachedOrderedDiffFiles(files []*utils.DiffFile, repo string, prNumber int, sha string) ([]*utils.DiffFile, bool) {
	names := cachedFileOrdering(repo, prNumber, sha)
	if names == nil {
		return nil, false
	}
	return reorderFilesByNames(files, names), true
}

// analyzeDiff asks the LLM for the requested analysis values: the file
// ordering that makes the PR read top-to-bottom from integration points
// through implementation to tests, and/or the rating of how easy the PR is
// to review. The prompt only asks for what the caller needs. Every call is
// appended to the LLM call log, whatever the outcome.
func analyzeDiff(files []*utils.DiffFile, includeOrdering, includeEase bool, ctx callContext) (*DiffAnalysis, error) {
	rec := callRecord{
		context:   ctx,
		purpose:   callPurpose(includeOrdering, includeEase),
		fileCount: len(files),
		start:     time.Now(),
	}
	defer func() { writeCallLog(&rec) }()

	client, err := newClient()
	if err != nil {
		return nil, rec.fail(StageClientInit, err)
	}
	rec.provider = client.Provider()
	rec.model = client.Model()

	var fileList strings.Builder
	for _, f := range files {
		fileList.WriteString("- " + diffFileName(f) + "\n")
	}

	diffText := buildDiffText(files)
	rec.diffBytes = len(diffText)
	if len(diffText) > maxDiffSize {
		diffText = diffText[:maxDiffSize] + "\n... (diff truncated)\n"
		rec.truncated = true
	}

	prompt := buildAnalysisPrompt(fileList.String(), diffText, includeOrdering, includeEase)
	rec.promptBytes = len(prompt)

	text, err := client.Generate(prompt)
	rec.duration = time.Since(rec.start)
	if err != nil {
		stage := StageRequest
		var callErr *CallError
		if errors.As(err, &callErr) {
			stage = callErr.Stage
		}
		return nil, rec.fail(stage, err)
	}
	rec.responseBytes = len(text)

	analysis := parseDiffAnalysisText(text)
	rec.orderingCount = len(analysis.Ordering)
	rec.reviewEase = analysis.ReviewEase

	if includeOrdering && len(analysis.Ordering) == 0 {
		rec.responseSnippet = snippet(text)
		return nil, rec.fail(StageParse, fmt.Errorf("response contained no file paths"))
	}
	if includeEase && analysis.ReviewEase == "" {
		rec.responseSnippet = snippet(text)
		if !includeOrdering {
			return nil, rec.fail(StageParse, fmt.Errorf("response contained no usable review-ease rating"))
		}
		// Partial success: the ordering is usable, but the rating requested
		// alongside it is not. The ordering is still cached, so no later
		// render will retry the rating — the PR keeps missing its
		// review-ease tag until the post-update hook runs an ease-only call.
		rec.warn("response contained no usable review-ease rating")
		slog.Warn("LLM response contained no usable review-ease rating")
	}
	return analysis, nil
}

// callPurpose names what an analysis call asked the LLM for, for the log.
func callPurpose(includeOrdering, includeEase bool) string {
	switch {
	case includeOrdering && includeEase:
		return "file-ordering + review-ease"
	case includeOrdering:
		return "file-ordering"
	default:
		return "review-ease"
	}
}

// storeDiffAnalysis persists the LLM analysis results keyed by PR SHA.
func storeDiffAnalysis(repo string, prNumber int, sha string, analysis *DiffAnalysis) {
	storeFileOrdering(repo, prNumber, sha, analysis.Ordering)
	storeReviewEase(repo, prNumber, sha, analysis.ReviewEase)
}

// cachedFileOrdering returns a previously cached LLM file ordering for the
// given PR SHA, or nil when there is no usable cache entry.
func cachedFileOrdering(repo string, prNumber int, sha string) []string {
	if sha == "" {
		return nil
	}
	db := config.C().DB
	if db == nil {
		return nil
	}
	stored, err := db.GetDiffFileOrdering(prNumber, repo, sha)
	if err != nil {
		slog.Warn("failed to read diff file ordering cache", "error", err)
		return nil
	}
	if stored == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(stored), &names); err != nil {
		slog.Warn("failed to decode cached diff file ordering", "error", err)
		return nil
	}
	return names
}

// storeFileOrdering persists an LLM file ordering keyed by PR SHA. Empty
// orderings (e.g. from an ease-only analysis) are not stored.
func storeFileOrdering(repo string, prNumber int, sha string, names []string) {
	if sha == "" || len(names) == 0 {
		return
	}
	db := config.C().DB
	if db == nil {
		return
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		slog.Warn("failed to encode diff file ordering for cache", "error", err)
		return
	}
	if err := db.UpsertDiffFileOrdering(prNumber, repo, sha, string(encoded)); err != nil {
		slog.Warn("failed to write diff file ordering cache", "error", err)
	}
}

// cachedReviewEase returns a previously cached review-ease rating for the
// given PR SHA, or an empty string when there is no cached rating.
func cachedReviewEase(repo string, prNumber int, sha string) string {
	if sha == "" {
		return ""
	}
	db := config.C().DB
	if db == nil {
		return ""
	}
	ease, err := db.GetReviewEase(prNumber, repo, sha)
	if err != nil {
		slog.Warn("failed to read review ease cache", "error", err)
		return ""
	}
	return ease
}

// storeReviewEase persists a review-ease rating keyed by PR SHA. Empty
// ratings (feature disabled, or unusable LLM output) are not stored.
func storeReviewEase(repo string, prNumber int, sha string, ease string) {
	if sha == "" || ease == "" {
		return
	}
	db := config.C().DB
	if db == nil {
		return
	}
	if err := db.UpsertReviewEase(prNumber, repo, sha, ease); err != nil {
		slog.Warn("failed to write review ease cache", "error", err)
	}
}

// diffFileName returns the path used to identify a diff file, preferring the
// new name and falling back to the original name for deleted files.
func diffFileName(file *utils.DiffFile) string {
	if file.NewName != "" {
		return file.NewName
	}
	return file.OrigName
}

// buildAnalysisPrompt builds the prompt for the diff analysis LLM call,
// asking only for the values the caller requested: the file ordering, the
// review-ease rating, or both.
func buildAnalysisPrompt(fileList, diffText string, includeOrdering, includeEase bool) string {
	var b strings.Builder
	if includeOrdering {
		b.WriteString(`You are ordering the files of a pull request diff so a reviewer can read the PR from top to bottom and understand it.

Order the files by this priority:
1. The most important and relevant changes first: the entry points where the change is integrated (call sites, public APIs, top-level wiring).
2. Then helper functions and implementation details: how the change actually works.
3. Then styling changes such as CSS: after code, but before tests.
4. Then test files, last.

A reviewer should be able to read top to bottom: starting where the change is integrated, then into how it works, then the tests.
`)
	} else {
		b.WriteString(`You are rating a pull request diff for a code reviewer.
`)
	}
	if includeEase {
		b.WriteString(`
Rate how easy this pull request is to review: "easy" for small, mechanical, or repetitive changes; "medium" for typical changes that need a careful read; "hard" for large, subtle, or high-risk changes (tricky logic, concurrency, security, many interacting files).
`)
	}
	b.WriteString(fmt.Sprintf(`
Files in this diff:
%s
Full diff:
%s
`, fileList, diffText))
	switch {
	case includeOrdering && includeEase:
		b.WriteString(`
Respond with a first line of exactly "REVIEW_EASE: <rating>" where <rating> is easy, medium, or hard, followed by the file paths, one per line, in the order they should be displayed. Use the exact file paths listed above. Do not include numbering, bullets, commentary, or code fences.`)
	case includeOrdering:
		b.WriteString(`
Respond with ONLY the file paths, one per line, in the order they should be displayed. Use the exact file paths listed above. Do not include numbering, bullets, commentary, or code fences.`)
	default:
		b.WriteString(`
Respond with ONLY a single line of exactly "REVIEW_EASE: <rating>" where <rating> is easy, medium, or hard. Do not include anything else.`)
	}
	return b.String()
}

// parseDiffAnalysisText parses the LLM response text into the ordered file
// paths and the optional REVIEW_EASE rating line.
func parseDiffAnalysisText(text string) *DiffAnalysis {
	analysis := &DiffAnalysis{}
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.Trim(strings.TrimSpace(line), "`*")
		trimmed = strings.TrimSpace(trimmed)
		if rest, ok := cutPrefixFold(trimmed, reviewEaseLinePrefix); ok {
			if ease := normalizeReviewEase(rest); ease != "" {
				analysis.ReviewEase = ease
			}
			continue
		}
		if cleaned := cleanFileName(line); cleaned != "" {
			analysis.Ordering = append(analysis.Ordering, cleaned)
		}
	}
	return analysis
}

// cutPrefixFold is strings.CutPrefix with case-insensitive matching, so a
// model that answers "Review_Ease: easy" still parses.
func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// normalizeReviewEase maps an LLM rating string onto one of the canonical
// ratings ("easy", "medium", or "hard"); it returns an empty string for
// anything else. The rating only has to lead the string — "hard - tricky
// concurrency" still counts — so mild chattiness doesn't drop the rating.
func normalizeReviewEase(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`*")
	s = strings.ToLower(strings.TrimSpace(s))
	for _, rating := range []string{"easy", "medium", "hard"} {
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

// buildDiffText renders the diff files into a plain-text form for the LLM,
// labelling each file so the model can refer back to exact paths.
func buildDiffText(files []*utils.DiffFile) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("=== FILE: " + diffFileName(f) + " ===\n")
		for _, hunk := range f.Hunks {
			b.WriteString(hunk.RangeHeader() + "\n")
			for _, line := range hunk.WholeRange.Lines {
				b.WriteString(line.Render())
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// cleanFileName normalizes a single line of LLM output into a bare file
// path, stripping list markers, surrounding quotes/backticks, and a/ b/
// prefixes. It returns an empty string for lines that are not file paths.
func cleanFileName(line string) string {
	s := strings.TrimSpace(line)
	s = strings.Trim(s, "`")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "- ")
	s = strings.TrimPrefix(s, "* ")
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimPrefix(s, "a/")
	s = strings.TrimPrefix(s, "b/")
	return strings.TrimSpace(s)
}

// reorderFilesByNames reorders files to match the sequence of paths returned
// by the LLM. Files the LLM did not mention (or that could not be matched)
// keep their original relative order and are appended at the end.
func reorderFilesByNames(files []*utils.DiffFile, names []string) []*utils.DiffFile {
	used := make(map[*utils.DiffFile]bool)
	result := make([]*utils.DiffFile, 0, len(files))

	for _, name := range names {
		if f := matchDiffFile(files, name, used); f != nil {
			result = append(result, f)
			used[f] = true
		}
	}
	for _, f := range files {
		if !used[f] {
			result = append(result, f)
		}
	}
	return result
}

// matchDiffFile finds an unused diff file whose path matches name, trying an
// exact match first and then a basename match.
func matchDiffFile(files []*utils.DiffFile, name string, used map[*utils.DiffFile]bool) *utils.DiffFile {
	for _, f := range files {
		if !used[f] && diffFileName(f) == name {
			return f
		}
	}
	base := name[strings.LastIndex(name, "/")+1:]
	for _, f := range files {
		if used[f] {
			continue
		}
		fn := diffFileName(f)
		if fn[strings.LastIndex(fn, "/")+1:] == base {
			return f
		}
	}
	return nil
}
