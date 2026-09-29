package main

import (
	"crs/cmd/internal/pluginkit"
	"strings"
	"testing"
)

func TestBuildPromptCarriesContextAndDiff(t *testing.T) {
	prompt := buildPrompt("+func handler() {}", pluginkit.PRMetadata{Title: "Add an endpoint", Body: "Because."})
	for _, want := range []string{
		"PR Title: Add an endpoint\nPR Description: Because.\nDiff:\n+func handler() {}",
		"Identify any new or modified API endpoints.",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
