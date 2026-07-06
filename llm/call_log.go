package llm

// The LLM call log mirrors the cache-miss log in server/renderer.go: every
// non-plugin LLM call appends a human-readable report to
// ~/.crs/llm_calls.log, recording what was asked for, which backend was
// called, and — when the call fails or its response can't be parsed — the
// stage it died at and enough of the response to see why. This is the place
// to look when a PR is missing its review-ease tag.

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

// snippet returns the head of an LLM response for the log, truncated so a
// chatty response can't blow up the log file.
func snippet(text string) string {
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

// writeCallLog appends the record to ~/.crs/llm_calls.log. Log failures are
// reported via slog but never affect the call itself.
func writeCallLog(rec *callRecord) {
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

	if rec.duration == 0 {
		rec.duration = time.Since(rec.start)
	}

	var sb strings.Builder
	sb.WriteString("=== LLM Call Report ===\n")
	sb.WriteString(fmt.Sprintf("Time:     %s\n", rec.start.UTC().Format("2006-01-02 15:04:05 UTC")))
	sb.WriteString(fmt.Sprintf("PR:       #%d  (repo: %s, sha: %s)\n", rec.context.prNumber, rec.context.repo, rec.context.sha))
	sb.WriteString(fmt.Sprintf("Trigger:  %s\n", rec.context.trigger))
	sb.WriteString(fmt.Sprintf("Purpose:  %s\n", rec.purpose))
	if rec.provider != "" {
		sb.WriteString(fmt.Sprintf("LLM:      %s (%s)\n", rec.provider, rec.model))
	}

	sb.WriteString(fmt.Sprintf("Input:    %d files, %d-byte diff", rec.fileCount, rec.diffBytes))
	if rec.truncated {
		sb.WriteString(fmt.Sprintf(" (truncated to %d bytes for the prompt)", maxDiffSize))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("Call:     took %s, %d-byte response\n", rec.duration.Round(time.Millisecond), rec.responseBytes))

	switch {
	case rec.stage != "":
		sb.WriteString(fmt.Sprintf("Outcome:  FAILURE at stage %q\n", rec.stage))
		sb.WriteString(fmt.Sprintf("Error:    %v\n", rec.err))
	case len(rec.warnings) > 0:
		sb.WriteString("Outcome:  PARTIAL\n")
		for _, w := range rec.warnings {
			sb.WriteString(fmt.Sprintf("Problem:  %s\n", w))
		}
	default:
		sb.WriteString("Outcome:  SUCCESS\n")
	}

	ease := rec.reviewEase
	if ease == "" {
		ease = "(none)"
	}
	sb.WriteString(fmt.Sprintf("Parsed:   %d ordered file paths, review-ease %s\n", rec.orderingCount, ease))

	if rec.responseSnippet != "" {
		sb.WriteString("Response (for parse debugging):\n")
		for _, line := range strings.Split(rec.responseSnippet, "\n") {
			sb.WriteString("  | " + line + "\n")
		}
	}

	sb.WriteString("---\n\n")

	if _, err := f.WriteString(sb.String()); err != nil {
		slog.Warn("llm call log: write failed", "error", err)
	}
}
