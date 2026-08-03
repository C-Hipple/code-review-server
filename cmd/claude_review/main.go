package main

import (
	"bytes"
	"crs/cmd/internal/pluginkit"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

const defaultModel = "sonnet"
const defaultTimeoutSec = 270 // 4m30s — slightly under server's 5m

const allowedTools = "Read,Grep,Glob,WebFetch," +
	"Bash(git log:*),Bash(git blame:*),Bash(git diff:*),Bash(git show:*)," +
	"Bash(rg:*),Bash(go test:*),Bash(go vet:*),Bash(go build:*)," +
	"Bash(bun test:*),Bash(bun run:*),Bash(gh pr view:*)"

const systemPrompt = `You are a senior engineer reviewing a GitHub pull request. You have
Read, Grep, Glob, Bash, and WebFetch tools available — use them to
investigate, not just skim the diff:
  - ` + "`git log -n 10 --oneline -- <file>`" + ` and ` + "`git blame`" + ` for history on touched code
  - ` + "`rg`" + ` to find callers of changed symbols
  - Read CLAUDE.md if present for project conventions
  - Run the relevant test package (e.g. ` + "`go test ./pkg/...`" + `) and ` + "`go vet`" + `
  - Read related files to gauge impact

Strict output format (Markdown, in this exact order):

## Summary
3-5 bullets describing what this PR does.

## Blockers
Correctness bugs, security issues, broken contracts. Write "None" if empty.

## Suggestions
Non-blocking improvements worth considering. Write "None" if empty.

## Nits
Style and cleanup. Write "None" if empty.

## Findings JSON
` + "```json" + `
[{"file":"path","line":42,"severity":"blocker|major|nit","message":"..."}]
` + "```" + `

Every finding must cite file:line. Prefer specific over general.
If you discovered something via tool use, briefly say how.
Do NOT repeat findings that appear in "Previous Automated Review"
unless they are still present in the current diff.`

func isSafeIdent(s string) bool {
	if len(s) == 0 || len(s) > 200 {
		return false
	}
	for _, c := range s {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '.' && c != '_' && c != '-' && c != '/' {
			return false
		}
	}
	return true
}

func isNumeric(s string) bool {
	if len(s) == 0 || len(s) > 12 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func diffFilePaths(diff string) []string {
	var paths []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			rest := line[len("+++ b/"):]
			if i := strings.IndexByte(rest, '\t'); i >= 0 {
				rest = rest[:i]
			}
			paths = append(paths, rest)
		}
	}
	return paths
}

func isTrivialPath(p string) bool {
	trivialSuffixes := []string{
		".md",
		"/CHANGELOG",
		"/LICENSE",
		".pb.go",
		".gen.go",
		"_generated.go",
		"bun.lock",
		"bun.lockb",
		"package-lock.json",
		"yarn.lock",
		"Cargo.lock",
		"go.sum",
	}
	for _, s := range trivialSuffixes {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
}

func isTrivialDiff(diff string) bool {
	if diff == "" {
		return false
	}
	paths := diffFilePaths(diff)
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if !isTrivialPath(p) {
			return false
		}
	}
	return true
}

func getEnv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func cacheDir() (string, error) {
	if home := os.Getenv("CRS_HOME"); home != "" {
		return filepath.Join(home, "cache", "claude_review"), nil
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", fmt.Errorf("no HOME env var")
	}
	return filepath.Join(home, ".cache", "crs", "claude_review"), nil
}

func cacheFilePath(owner, repo, number string) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s__%s__%s.md", owner, repo, number)
	return filepath.Join(dir, name), nil
}

func loadPrior(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return data
}

func savePrior(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func buildPrompt(owner, repo, number, headers, comments, diff string, prior []byte) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Review PR #%s on `%s/%s`.\n\n", number, owner, repo)

	if headers != "" {
		b.WriteString("## PR Metadata\n```json\n")
		b.WriteString(headers)
		b.WriteString("\n```\n\n")
	}

	if comments != "" && comments != "[]" && comments != "null" {
		b.WriteString("## Existing Review Comments (avoid duplicating these)\n```json\n")
		b.WriteString(comments)
		b.WriteString("\n```\n\n")
	}

	if len(prior) > 0 {
		b.WriteString("## Previous Automated Review\n")
		b.WriteString("Only re-report items still present in the current diff. Skip anything that has been fixed.\n\n")
		b.Write(prior)
		b.WriteString("\n\n")
	}

	if diff != "" {
		b.WriteString("## Diff\n```diff\n")
		b.WriteString(diff)
		b.WriteString("\n```\n\n")
	} else {
		b.WriteString("## Diff\nNot provided directly — fetch via `gh pr view --json files,...`.\n\n")
	}

	b.WriteString("## Instructions\n\n")
	b.WriteString("Be thorough. Use your tools liberally to dig in beyond the diff\n")
	b.WriteString("(history, callers, tests, linters). Cite file:line for every finding.\n")
	b.WriteString("Follow the strict output format from the system prompt exactly.")

	return b.String()
}

func postReview(owner, repo, number, body string) error {
	tmpDir := os.Getenv("TMPDIR")
	if tmpDir == "" {
		tmpDir = "/tmp"
	}
	tmpPath := filepath.Join(tmpDir, fmt.Sprintf("claude_review_%s_%s_%s.md", owner, repo, number))

	if err := os.WriteFile(tmpPath, []byte(body), 0o600); err != nil {
		return err
	}
	defer os.Remove(tmpPath)

	repoArg := fmt.Sprintf("%s/%s", owner, repo)
	cmd := exec.Command("gh", "pr", "review", number, "--repo", repoArg, "--comment", "--body-file", tmpPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func main() {
	owner := flag.String("owner", "", "GitHub owner")
	repo := flag.String("repo", "", "GitHub repo")
	number := flag.String("number", "", "PR number")
	diff := flag.String("diff", "", "Unified diff")
	headers := flag.String("headers", "", "PR metadata JSON")
	comments := flag.String("comments", "", "Review comments JSON")
	postReviewFlag := flag.Bool("post-review", false, "Post review via gh pr review")
	// The review doesn't vary by call type, but the flag still has to be
	// accepted since the server always passes it.
	pluginkit.RegisterCallTypeFlag()
	flag.Parse()

	stderr := os.Stderr

	if *owner == "" || *repo == "" || *number == "" {
		fmt.Fprintln(stderr, "Error: --owner, --repo, and --number are required")
		os.Exit(1)
	}
	if !isSafeIdent(*owner) || !isSafeIdent(*repo) || !isNumeric(*number) {
		fmt.Fprintln(stderr, "Error: invalid characters in --owner, --repo, or --number")
		os.Exit(2)
	}

	if isTrivialDiff(*diff) {
		fmt.Print("## Summary\nDocs / lockfile / generated-file changes only.\n\n## Blockers\nNone\n\n## Suggestions\nNone\n\n## Nits\nNone\n\n## Findings JSON\n```json\n[]\n```\n")
		return
	}

	cachePath, cacheErr := cacheFilePath(*owner, *repo, *number)
	var prior []byte
	if cacheErr == nil {
		prior = loadPrior(cachePath)
	}

	prompt := buildPrompt(*owner, *repo, *number, *headers, *comments, *diff, prior)

	model := getEnv("CRS_CLAUDE_REVIEW_MODEL", defaultModel)

	timeoutSec := uint64(defaultTimeoutSec)
	if t := os.Getenv("CRS_CLAUDE_REVIEW_TIMEOUT_SEC"); t != "" {
		if v, err := strconv.ParseUint(t, 10, 64); err == nil {
			timeoutSec = v
		}
	}

	claudeArgs := []string{
		"-p",
		"--model", model,
		"--output-format", "text",
		"--append-system-prompt", systemPrompt,
		"--allowedTools", allowedTools,
	}

	cmd := exec.Command("claude", claudeArgs...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Fprintf(stderr, "claude_review: failed to create stdout pipe: %v\n", err)
		os.Exit(1)
	}

	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "claude_review: failed to start claude: %v\n", err)
		os.Exit(1)
	}

	var killed atomic.Bool
	timer := time.AfterFunc(time.Duration(timeoutSec)*time.Second, func() {
		killed.Store(true)
		cmd.Process.Kill()
	})

	var captured bytes.Buffer
	mw := io.MultiWriter(os.Stdout, &captured)
	io.Copy(mw, stdoutPipe)

	timer.Stop()
	err = cmd.Wait()

	if killed.Load() {
		fmt.Fprintf(stderr, "\nclaude_review: timed out after %ds; returning partial output\n", timeoutSec)
	} else if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Fprintf(stderr, "claude exited with code %d\n", exitErr.ExitCode())
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(stderr, "claude terminated abnormally: %v\n", err)
		os.Exit(1)
	}

	if cacheErr == nil && captured.Len() > 0 {
		if err := savePrior(cachePath, captured.Bytes()); err != nil {
			fmt.Fprintf(stderr, "claude_review: failed to save cache: %v\n", err)
		}
	}

	if *postReviewFlag && captured.Len() > 0 {
		if err := postReview(*owner, *repo, *number, captured.String()); err != nil {
			fmt.Fprintf(stderr, "claude_review: failed to post review: %v\n", err)
		}
	}
}
