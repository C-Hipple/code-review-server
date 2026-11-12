package chat

import (
	"strings"
	"testing"
)

func TestOpeningPromptCutsALargeDiffAtALine(t *testing.T) {
	pr := testPR
	line := "+" + strings.Repeat("x", 99) + "\n"
	pr.Diff = strings.Repeat(line, maxPromptDiffBytes/len(line)+50)
	prompt, truncated := openingPrompt(pr, nil, "Big one")
	if !truncated {
		t.Fatal("a diff over the cap should be cut")
	}
	if !strings.Contains(prompt, DiffFile+" in your working directory has all of it") {
		t.Errorf("the prompt should point at the full diff")
	}
	diff := prompt[strings.Index(prompt, "```diff\n")+len("```diff\n"):]
	diff = diff[:strings.Index(diff, "\n```")]
	if len(diff) > maxPromptDiffBytes || !strings.HasSuffix(diff, strings.Repeat("x", 99)) {
		t.Errorf("the cut should land at the end of a line within the cap (got %d bytes)", len(diff))
	}
}

func TestOpeningPromptFencesADiffThatHasFences(t *testing.T) {
	pr := testPR
	pr.Diff = "+```go\n+x := 1\n+```\n"
	prompt, _ := openingPrompt(pr, nil, "q")
	if !strings.Contains(prompt, "````diff\n") {
		t.Errorf("the diff's own fences must not close the prompt's:\n%s", prompt)
	}
}

func TestOpeningPromptWithoutADescriptionOrDiff(t *testing.T) {
	pr := testPR
	pr.Body, pr.Diff = "", ""
	prompt, truncated := openingPrompt(pr, nil, "q")
	if truncated || !strings.Contains(prompt, "_No description._") || !strings.Contains(prompt, "_The diff is not available._") {
		t.Errorf("prompt:\n%s", prompt)
	}
}

func TestSystemPromptDescribesTheCheckout(t *testing.T) {
	pr := testPR
	if p := systemPrompt(pr); strings.Contains(p, "checked out") || strings.Contains(p, "clone") {
		t.Errorf("no checkout, nothing said about one:\n%s", p)
	}
	pr.Checkout = "/src/widgets"
	if p := systemPrompt(pr); !strings.Contains(p, "/src/widgets") || !strings.Contains(p, "may not be at the PR's head") {
		t.Errorf("a clone should come with a warning:\n%s", p)
	}
	pr.AtHead = true
	if p := systemPrompt(pr); !strings.Contains(p, "branch is checked out at /src/widgets") {
		t.Errorf("a worktree is at the head:\n%s", p)
	}
}

func TestTitleFor(t *testing.T) {
	if got := titleFor("  why\n\tthis?  "); got != "why this?" {
		t.Errorf("titleFor = %q", got)
	}
	long := strings.Repeat("é", 100)
	if got := titleFor(long); len([]rune(got)) != 80 || !strings.HasSuffix(got, "…") {
		t.Errorf("a long title should be cut to 80 runes, got %d: %q", len([]rune(got)), got)
	}
}

func TestNewIDIsAUUID(t *testing.T) {
	id := newID()
	if len(id) != 36 || id[14] != '4' || strings.Count(id, "-") != 4 {
		t.Errorf("newID = %q", id)
	}
	if newID() == id {
		t.Error("IDs should differ")
	}
}
