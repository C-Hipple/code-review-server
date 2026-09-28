package llm

// The LLM call log mirrors the cache-miss log in server/renderer.go: every
// non-plugin LLM call appends a human-readable report to
// ~/.crs/llm_calls.log, recording what was asked for, which backend was
// called, and — when the call fails or its response can't be parsed — the
// stage it died at and enough of the response to see why. This is the place
// to look when a PR is missing its review-ease tag.
//
// AI feature runs (the ai package) append here too, through AppendCallReport,
// one entry per run — including runs that settled everything deterministically
// and never called a model, and runs that ended as INSUFFICIENT-INPUT.

import (
	"crs/config"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const callLogName = "llm_calls.log"

// callContext says which PR an LLM call was made for and which code path
// asked for it.
type callContext struct {
	repo     string
	prNumber int
	sha      string
	trigger  string // "render" or "post-update hook"
}

// callRecord accumulates everything worth logging about one LLM call.
type callRecord struct {
	context callContext

	provider string // empty when the client could not be built
	model    string
	purpose  string // what the call asked for

	fileCount     int
	diffBytes     int  // diff size before truncation
	truncated     bool // diff was cut down to maxDiffSize for the prompt
	promptBytes   int
	responseBytes int
	start         time.Time
	duration      time.Duration

	// Outcome. stage/err are set on failure; warnings record non-fatal
	// anomalies such as a combined call whose rating line was unusable.
	stage           string
	err             error
	warnings        []string
	orderingCount   int    // file paths parsed from the response
	reviewEase      string // rating parsed from the response, if any
	responseSnippet string // head of the response, set when parsing had problems
}

// fail marks the record as failed at a stage and passes the error through,
// so callers can write `return nil, rec.fail(stage, err)`.
func (r *callRecord) fail(stage string, err error) error {
	r.stage = stage
	r.err = err
	return err
}

// warn records a non-fatal anomaly with the call.
func (r *callRecord) warn(msg string) {
	r.warnings = append(r.warnings, msg)
}

// Snippet returns the head of an LLM response for the log, truncated so a
// chatty response can't blow up the log file.
func Snippet(text string) string {
	const max = 600
	s := strings.TrimSpace(text)
	if s == "" {
		return "(empty response text)"
	}
	if len(s) > max {
		s = s[:max] + "\n... (response truncated)"
	}
	return s
}

// CallReport is one entry in the call log. The diff analysis in this package
// and the ai package's feature runs both write through it, so every model
// call the server makes — and every AI feature run, including the ones that
// never needed a model — lands in the same file in the same shape.
type CallReport struct {
	// Title heads the entry; empty means "LLM Call Report".
	Title    string
	Time     time.Time
	Repo     string
	PRNumber int
	SHA      string
	Trigger  string
	Purpose  string
	// Provider and Model are empty when no backend was built.
	Provider string
	Model    string
	// Input says what was sent, in one line.
	Input string
	// Call describes the call itself; empty means
	// "took <Duration>, <ResponseBytes>-byte response".
	Call          string
	Duration      time.Duration
	ResponseBytes int

	// Outcome. Stage and Err are set when the call failed. Status names a
	// completed outcome other than success (e.g. "INSUFFICIENT-INPUT"), and
	// Warnings list non-fatal problems; either one lists them as Problem lines.
	Stage    string
	Err      error
	Status   string
	Warnings []string
	// Parsed summarizes what was read out of the response.
	Parsed string
	// ResponseSnippet is the head of the response, set when parsing had
	// problems so the log shows what the model actually said.
	ResponseSnippet string
}

// writeCallLog appends a diff-analysis record to the call log.
func writeCallLog(rec *callRecord) {
	if rec.duration == 0 {
		rec.duration = time.Since(rec.start)
	}

	input := fmt.Sprintf("%d files, %d-byte diff", rec.fileCount, rec.diffBytes)
	if rec.truncated {
		input += fmt.Sprintf(" (truncated to %d bytes for the prompt)", maxDiffSize)
	}
	ease := rec.reviewEase
	if ease == "" {
		ease = "(none)"
	}

	AppendCallReport(CallReport{
		Time:            rec.start,
		Repo:            rec.context.repo,
		PRNumber:        rec.context.prNumber,
		SHA:             rec.context.sha,
		Trigger:         rec.context.trigger,
		Purpose:         rec.purpose,
		Provider:        rec.provider,
		Model:           rec.model,
		Input:           input,
		Duration:        rec.duration,
		ResponseBytes:   rec.responseBytes,
		Stage:           rec.stage,
		Err:             rec.err,
		Warnings:        rec.warnings,
		Parsed:          fmt.Sprintf("%d ordered file paths, review-ease %s", rec.orderingCount, ease),
		ResponseSnippet: rec.responseSnippet,
	})
}

// AppendCallReport appends a report to ~/.crs/llm_calls.log. Log failures are
// reported via slog but never affect the call being logged.
func AppendCallReport(r CallReport) {
	crsHome, err := config.GetCRSHome()
	if err != nil {
		slog.Warn("llm call log: cannot determine CRS home", "error", err)
		return
	}

	logPath := filepath.Join(crsHome, callLogName)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		slog.Warn("llm call log: cannot open log file", "path", logPath, "error", err)
		return
	}
	defer f.Close()

	title := r.Title
	if title == "" {
		title = "LLM Call Report"
	}
	call := r.Call
	if call == "" {
		call = fmt.Sprintf("took %s, %d-byte response", r.Duration.Round(time.Millisecond), r.ResponseBytes)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("=== %s ===\n", title))
	sb.WriteString(fmt.Sprintf("Time:     %s\n", r.Time.UTC().Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString(fmt.Sprintf("PR:       #%d  (repo: %s, sha: %s)\n", r.PRNumber, r.Repo, r.SHA))
	sb.WriteString(fmt.Sprintf("Trigger:  %s\n", r.Trigger))
	sb.WriteString(fmt.Sprintf("Purpose:  %s\n", r.Purpose))
	if r.Provider != "" {
		sb.WriteString(fmt.Sprintf("LLM:      %s (%s)\n", r.Provider, r.Model))
	}
	if r.Input != "" {
		sb.WriteString(fmt.Sprintf("Input:    %s\n", r.Input))
	}
	sb.WriteString(fmt.Sprintf("Call:     %s\n", call))

	switch {
	case r.Stage != "":
		sb.WriteString(fmt.Sprintf("Outcome:  FAILURE at stage %q\n", r.Stage))
		sb.WriteString(fmt.Sprintf("Error:    %v\n", r.Err))
	case r.Status != "":
		sb.WriteString(fmt.Sprintf("Outcome:  %s\n", r.Status))
		for _, w := range r.Warnings {
			sb.WriteString(fmt.Sprintf("Problem:  %s\n", w))
		}
	case len(r.Warnings) > 0:
		sb.WriteString("Outcome:  PARTIAL\n")
		for _, w := range r.Warnings {
			sb.WriteString(fmt.Sprintf("Problem:  %s\n", w))
		}
	default:
		sb.WriteString("Outcome:  SUCCESS\n")
	}

	if r.Parsed != "" {
		sb.WriteString(fmt.Sprintf("Parsed:   %s\n", r.Parsed))
	}

	if r.ResponseSnippet != "" {
		sb.WriteString("Response (for parse debugging):\n")
		for _, line := range strings.Split(r.ResponseSnippet, "\n") {
			sb.WriteString("  | " + line + "\n")
		}
	}

	sb.WriteString("---\n\n")

	if _, err := f.WriteString(sb.String()); err != nil {
		slog.Warn("llm call log: write failed", "error", err)
	}
}
