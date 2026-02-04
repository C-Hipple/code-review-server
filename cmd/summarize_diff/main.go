package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
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
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Author      string   `json:"author"`
	BaseRef     string   `json:"base_ref"`
	HeadRef     string   `json:"head_ref"`
	State       string   `json:"state"`
	Milestone   string   `json:"milestone"`
	Labels      []string `json:"labels"`
	Assignees   []string `json:"assignees"`
	Reviewers   []string `json:"reviewers"`
	Draft       bool     `json:"draft"`
	CIStatus    string   `json:"ci_status"`
		CIFailures         []string `json:"ci_failures"`
		Body               string   `json:"body"`
		URL                string   `json:"url"`
		WorktreePath       string   `json:"worktree_path"`
	}

func callGemini(diff string, metadata PRMetadata, geminiToken string) (string, error) {
	// Using gemini-2.0-flash
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=" + geminiToken

	var contextInfo string
	if metadata.Title != "" {
		contextInfo += fmt.Sprintf("PR Title: %s\n", metadata.Title)
	}
	if metadata.Body != "" {
		contextInfo += fmt.Sprintf("PR Description: %s\n", metadata.Body)
	}

	prompt := fmt.Sprintf(`Summarize this PR as briefly as possible.
- 2-4 bullet points on key changes (one line each)
- 1-2 brief suggestions if any

Be terse. No fluff.

%sDiff:
%s
`, contextInfo, diff)

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

	// Suppress unused warnings for now if we don't use them all
	_ = owner
	_ = repo
	_ = number
	_ = commentsJSON

	var metadata PRMetadata
	if *headersJSON != "" {
		if err := json.Unmarshal([]byte(*headersJSON), &metadata); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to parse headers: %v\n", err)
		}
	}

	geminiToken := os.Getenv("GEMINI_API_KEY")
	if geminiToken == "" {
		fmt.Println("Error: GEMINI_API_KEY environment variable not set")
		os.Exit(1)
	}

	if *diff == "" {
		fmt.Println("Error: No diff provided")
		os.Exit(1)
	}

	summary, err := callGemini(*diff, metadata, geminiToken)
	if err != nil {
		fmt.Printf("Error calling Gemini: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(summary)
}
