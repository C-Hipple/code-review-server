package chat

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// PRContext is what a chat attaches about its PR when a conversation with the
// agent starts: enough to say which PR it is, its description and its diff.
// The server builds it from the same caches GetPR reads.
type PRContext struct {
	Owner   string
	Repo    string
	Number  int
	Title   string
	Author  string
	URL     string
	BaseRef string
	HeadRef string
	HeadSHA string
	State   string
	Body    string
	Diff    string
	// Checkout is a local checkout of the repository the agent may read, ""
	// when there is none. AtHead says it is the PR's own worktree; otherwise
	// it is the user's clone, at whatever it has checked out.
	Checkout string
	AtHead   bool
}

// Files the working directory of a chat holds, for the agent to read.
const (
	// DiffFile is the PR's full diff.
	DiffFile = "pr.diff"
	// DescriptionFile is the PR's title, details and description.
	DescriptionFile = "pr.md"
)

// maxPromptDiffBytes caps how much of the diff the first message carries; the
// whole of it is in DiffFile either way.
const maxPromptDiffBytes = 200000

// Attachments the first message of a conversation carries, as a user
// message's meta names them.
const (
	AttachedDescription = "description"
	AttachedDiff        = "diff"
)

// ref names the PR the way GitHub does, "owner/repo#N".
func (pr PRContext) ref() string {
	return fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Number)
}

// systemPrompt is appended to the agent's own system prompt on every turn: it
// says where the agent is and what it can do there.
func systemPrompt(pr PRContext) string {
	var b strings.Builder
	b.WriteString("You are helping a code reviewer review the GitHub pull request " + pr.ref() +
		" from inside Code Review Server, a review dashboard. The reviewer talks to you in a small chat panel beside the PR's diff.\n\n")
	b.WriteString("- You can read and search files, but you cannot change anything or run commands. Never claim to have edited, run or tested something.\n")
	b.WriteString("- Your working directory holds the PR: " + DescriptionFile + " (its title, details and description) and " + DiffFile + " (its full diff). Read them again whenever you need to.\n")
	if pr.Checkout != "" {
		if pr.AtHead {
			b.WriteString("- The PR's branch is checked out at " + pr.Checkout + ", so you can read the code around a change.\n")
		} else {
			b.WriteString("- A clone of the repository is at " + pr.Checkout + ". It may not be at the PR's head commit, so trust " + DiffFile + " for what the PR changes and use the clone for the code around it.\n")
		}
	}
	b.WriteString("- Answer concisely in GitHub-flavored Markdown, and cite code as path:line.\n")
	return b.String()
}

// header introduces the PR in one line, then its URL.
func header(pr PRContext) string {
	var b strings.Builder
	b.WriteString("We're reviewing pull request " + pr.ref())
	if pr.Title != "" {
		b.WriteString(fmt.Sprintf(": %q", pr.Title))
	}
	if pr.Author != "" {
		b.WriteString(" by @" + pr.Author)
	}
	if pr.HeadRef != "" && pr.BaseRef != "" {
		b.WriteString(fmt.Sprintf(" (%s → %s)", pr.HeadRef, pr.BaseRef))
	}
	if pr.HeadSHA != "" {
		b.WriteString(", at commit " + shortSHA(pr.HeadSHA))
	}
	b.WriteString(".\n")
	if pr.URL != "" {
		b.WriteString(pr.URL + "\n")
	}
	return b.String()
}

// historyEntry is one message of the history a fresh conversation is given.
type historyEntry struct {
	reviewer bool
	text     string
}

// openingPrompt is the first message of a conversation with the agent: that
// we're reviewing the PR, what the reviewer asked, then the PR's description
// and diff, so the agent has them in context from the start. A chat whose
// earlier conversation the agent no longer has (see Manager) passes what was
// said as history, which goes before the new question.
//
// It reports whether the diff had to be cut to fit.
func openingPrompt(pr PRContext, history []historyEntry, message string) (string, bool) {
	var b strings.Builder
	b.WriteString(header(pr))
	b.WriteString("\n")
	if len(history) > 0 {
		b.WriteString("This conversation started earlier, but you no longer have it. What was said so far:\n\n")
		for _, h := range history {
			who := "**You:**"
			if h.reviewer {
				who = "**Reviewer:**"
			}
			b.WriteString(who + " " + strings.TrimSpace(h.text) + "\n\n")
		}
		b.WriteString("The reviewer now says:\n\n")
	}
	b.WriteString(strings.TrimSpace(message))
	b.WriteString("\n\n---\n\n## PR description\n\n")
	if body := strings.TrimSpace(pr.Body); body != "" {
		b.WriteString(body + "\n")
	} else {
		b.WriteString("_No description._\n")
	}

	b.WriteString("\n## Diff\n\n")
	diff := strings.TrimRight(pr.Diff, "\n")
	truncated := false
	switch {
	case strings.TrimSpace(diff) == "":
		b.WriteString("_The diff is not available._\n")
	default:
		if len(diff) > maxPromptDiffBytes {
			diff = cutAtLine(diff, maxPromptDiffBytes)
			truncated = true
		}
		fence := codeFence(diff)
		b.WriteString(fence + "diff\n" + diff + "\n" + fence + "\n")
		if truncated {
			b.WriteString(fmt.Sprintf("\n(The diff above stops after %d KB; %s in your working directory has all of it.)\n",
				maxPromptDiffBytes/1000, DiffFile))
		}
	}
	return b.String(), truncated
}

// descriptionFile is DescriptionFile's content.
func descriptionFile(pr PRContext) string {
	var b strings.Builder
	title := pr.Title
	if title == "" {
		title = pr.ref()
	}
	b.WriteString("# " + title + "\n\n")
	b.WriteString("- PR: " + pr.ref() + "\n")
	for _, f := range []struct{ name, value string }{
		{"URL", pr.URL},
		{"Author", pr.Author},
		{"State", pr.State},
		{"Branch", strings.Trim(pr.HeadRef+" → "+pr.BaseRef, " →")},
		{"Head commit", pr.HeadSHA},
	} {
		if f.value != "" {
			b.WriteString("- " + f.name + ": " + f.value + "\n")
		}
	}
	b.WriteString("\n## Description\n\n")
	if body := strings.TrimSpace(pr.Body); body != "" {
		b.WriteString(body + "\n")
	} else {
		b.WriteString("_No description._\n")
	}
	return b.String()
}

// codeFence returns a backtick fence longer than any run of backticks in s, so
// a diff that itself contains fences can't close it early.
func codeFence(s string) string {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// cutAtLine cuts s to at most max bytes, at the end of a line when one ends
// within it, and otherwise on a UTF-8 boundary.
func cutAtLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return cut[:i]
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// titleFor is a chat's title: the start of what the reviewer first asked, on
// one line.
func titleFor(message string) string {
	t := strings.Join(strings.Fields(message), " ")
	const maxTitle = 80
	if utf8.RuneCountInString(t) <= maxTitle {
		return t
	}
	runes := []rune(t)
	return strings.TrimSpace(string(runes[:maxTitle-1])) + "…"
}
