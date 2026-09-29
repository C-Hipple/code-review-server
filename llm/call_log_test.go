package llm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readCallLog returns the contents of llm_calls.log under the given CRS home.
func readCallLog(t *testing.T, crsHome string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(crsHome, callLogName))
	if err != nil {
		t.Fatalf("failed to read call log: %v", err)
	}
	return string(data)
}

func TestAppendCallReportCustomOutcome(t *testing.T) {
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)

	AppendCallReport(CallReport{
		Title:    "AI Feature Run",
		Repo:     "widgets",
		PRNumber: 42,
		SHA:      "sha-42",
		Trigger:  "explicit",
		Purpose:  "ai:comments-addressed (oneshot)",
		Input:    "3 threads, 0 sent to the model",
		Call:     "no model call",
		Status:   "INSUFFICIENT-INPUT",
		Warnings: []string{"no comments are cached for this PR"},
		Parsed:   "verdict insufficient-input",
	})

	log := readCallLog(t, crsHome)
	for _, want := range []string{
		"=== AI Feature Run ===",
		"PR:       #42  (repo: widgets, sha: sha-42)",
		"Purpose:  ai:comments-addressed (oneshot)",
		"Input:    3 threads, 0 sent to the model",
		"Call:     no model call",
		"Outcome:  INSUFFICIENT-INPUT",
		"Problem:  no comments are cached for this PR",
		"Parsed:   verdict insufficient-input",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("call log missing %q; log:\n%s", want, log)
		}
	}
	// No provider was built, so there is no LLM line to report.
	if strings.Contains(log, "LLM:") {
		t.Errorf("expected no LLM line for a run without a provider; log:\n%s", log)
	}
}

func TestAppendCallReportFailureWinsOverStatus(t *testing.T) {
	crsHome := t.TempDir()
	t.Setenv("CRS_HOME", crsHome)

	AppendCallReport(CallReport{
		Status: "INSUFFICIENT-INPUT",
		Stage:  StageTimeout,
		Err:    fmt.Errorf("deadline exceeded"),
	})

	log := readCallLog(t, crsHome)
	if !strings.Contains(log, `Outcome:  FAILURE at stage "timeout"`) || !strings.Contains(log, "Error:    deadline exceeded") {
		t.Errorf("expected the failure to be reported; log:\n%s", log)
	}
	if !strings.Contains(log, "=== LLM Call Report ===") {
		t.Errorf("expected the default title; log:\n%s", log)
	}
}
