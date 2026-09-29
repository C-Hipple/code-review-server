package llm

// The call log mirrors the cache-miss log in server/renderer.go: every AI
// feature run (the ai package) appends a human-readable report to
// ~/.crs/llm_calls.log, recording what was asked for, which backend was
// called, and — when the run fails or a model's answer can't be parsed — the
// stage it died at and enough of the answer to see why. It gets one entry per
// run, including runs that settled everything deterministically and never
// called a model, and runs that ended as INSUFFICIENT-INPUT. This is the place
// to look when a PR is missing its review-ease tag or its file ordering.

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

// CallReport is one entry in the call log, written through AppendCallReport so
// every entry lands in the same file in the same shape.
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
