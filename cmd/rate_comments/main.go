package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
}

type GeminiRequest struct {
	Contents []GeminiContent `json:"contents"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

type PRMetadata struct {
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	Author       string   `json:"author"`
	BaseRef      string   `json:"base_ref"`
	HeadRef      string   `json:"head_ref"`
	State        string   `json:"state"`
	Milestone    string   `json:"milestone"`
	Labels       []string `json:"labels"`
	Assignees    []string `json:"assignees"`
	Reviewers    []string `json:"reviewers"`
	Draft        bool     `json:"draft"`
	CIStatus     string   `json:"ci_status"`
	CIFailures   []string `json:"ci_failures"`
	Body         string   `json:"body"`
	URL          string   `json:"url"`
	WorktreePath string   `json:"worktree_path"`
}

type CommentJSON struct {
	ID        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Path      string    `json:"path"`
	Position  string    `json:"position"`
	InReplyTo int64     `json:"in_reply_to"`
	CreatedAt time.Time `json:"created_at"`
	Outdated  bool      `json:"outdated"`
	DiffHunk  string    `json:"diff_hunk"`
}

func callGemini(diff string, metadata PRMetadata, comments []CommentJSON, geminiToken string) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=" + geminiToken

	var contextInfo string
	if metadata.Title != "" {
		contextInfo += fmt.Sprintf("PR Title: %s\n", metadata.Title)
	}
	if metadata.Body != "" {
		contextInfo += fmt.Sprintf("PR Description: %s\n", metadata.Body)
	}

	// Build comment list for the prompt
	var commentList string
	for i, c := range comments {
		// Skip replies — rate top-level comments only
		if c.InReplyTo != 0 {
			continue
		}
		commentList += fmt.Sprintf("Comment %d (ID: %s, Author: %s, File: %s):\n", i+1, c.ID, c.Author, c.Path)
		if c.DiffHunk != "" {
			commentList += fmt.Sprintf("  Diff context:\n%s\n", c.DiffHunk)
		}
		commentList += fmt.Sprintf("  Comment: %s\n\n", c.Body)
	}

	if commentList == "" {
		return "No top-level comments to rate.", nil
	}

	prompt := fmt.Sprintf(`You are a senior software engineer reviewing the quality of code review comments left on a pull request.

For each comment below, rate it on three factors (scale 1-10):
- Accuracy: Is the comment technically correct? Does it correctly identify an issue or ask a valid question?
- Importance: How significant is the issue or question raised? Would ignoring it cause real problems?
- Catch Quality: How insightful is the find? Did the reviewer spot something subtle or hard to notice?

Format your response exactly like this for each comment:
Comment <N> (ID: <id>):
  Accuracy:      <score>/10 — <one-line reason>
  Importance:    <score>/10 — <one-line reason>
  Catch Quality: <score>/10 — <one-line reason>
  Overall: <score>/10

After all comments, add a one-paragraph summary of the review quality overall.

Be terse and direct. No fluff.

%s
Comments to rate:
%s
Diff (for context):
%s
`, contextInfo, commentList, diff)

	reqBody := GeminiRequest{
		Contents: []GeminiContent{
			{
				Parts: []GeminiPart{
					{Text: prompt},
				},
			},
		},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var geminiResp GeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", err
	}

	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		return geminiResp.Candidates[0].Content.Parts[0].Text, nil
	}

	return "", fmt.Errorf("no content in response")
}

func main() {
	diff := flag.String("diff", "", "PR diff content")
	owner := flag.String("owner", "", "PR owner")
	repo := flag.String("repo", "", "PR repo")
	number := flag.Int("number", 0, "PR number")
	commentsJSON := flag.String("comments", "", "PR comments JSON")
	headersJSON := flag.String("headers", "", "PR metadata JSON")

	flag.Parse()

	_ = owner
	_ = repo
	_ = number

	var metadata PRMetadata
	if *headersJSON != "" {
		if err := json.Unmarshal([]byte(*headersJSON), &metadata); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse headers: %v\n", err)
		}
	}

	var comments []CommentJSON
	if *commentsJSON != "" {
		if err := json.Unmarshal([]byte(*commentsJSON), &comments); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse comments: %v\n", err)
		}
	}

	if len(comments) == 0 {
		fmt.Println("No comments to rate.")
		return
	}

	geminiToken := os.Getenv("GEMINI_API_KEY")
	if geminiToken == "" {
		fmt.Println("Error: GEMINI_API_KEY environment variable not set")
		os.Exit(1)
	}

	result, err := callGemini(*diff, metadata, comments, geminiToken)
	if err != nil {
		fmt.Printf("Error calling Gemini: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(result)
}
