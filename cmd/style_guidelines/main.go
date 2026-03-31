package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	CIFailures  []string `json:"ci_failures"`
	Body        string   `json:"body"`
	URL         string   `json:"url"`
	WorktreePath string  `json:"worktree_path"`
}

func loadStyleGuidelines() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	path := filepath.Join(home, ".config", "style_guidelines.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("could not read style guidelines from %s: %w", path, err)
	}
	return string(data), nil
}

func callGemini(diff string, metadata PRMetadata, styleGuidelines string, geminiToken string) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=" + geminiToken

	var contextInfo string
	if metadata.Title != "" {
		contextInfo += fmt.Sprintf("PR Title: %s\n", metadata.Title)
	}
	if metadata.Body != "" {
		contextInfo += fmt.Sprintf("PR Description: %s\n", metadata.Body)
	}

	prompt := fmt.Sprintf(`You are a code reviewer evaluating a pull request against style guidelines.

## Style Guidelines

%s

## PR Context

%s
## Diff

%s

## Instructions

Evaluate the PR diff against the style guidelines above. Be concise:
- Note any violations with file and line references where possible
- Note areas that follow the guidelines well (briefly)
- Provide a short overall assessment

Focus only on style guideline compliance. Be direct and specific.
`, styleGuidelines, contextInfo, diff)

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

	styleGuidelines, err := loadStyleGuidelines()
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	result, err := callGemini(*diff, metadata, styleGuidelines, geminiToken)
	if err != nil {
		fmt.Printf("Error calling Gemini: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(result)
}
