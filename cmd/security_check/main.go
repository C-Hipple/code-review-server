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
	CIFailures  []string `json:"ci_failures"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
}

func callGemini(diff string, metadata PRMetadata, geminiToken string) (string, error) {
	// Using gemini-2.5-flash as per the example
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key=" + geminiToken

	var contextInfo string
	if metadata.Title != "" {
		contextInfo += fmt.Sprintf("PR Title: %s\n", metadata.Title)
	}
	if metadata.Description != "" {
		contextInfo += fmt.Sprintf("PR Description: %s\n", metadata.Description)
	}

	prompt := fmt.Sprintf(`Analyze the following PR diff for potential security issues, specifically focusing on endpoints.

Tasks:
1. Identify any new or modified API endpoints.
2. Check if these endpoints expose sensitive or critical information.
3. Verify if they are protected by security decorators (e.g., @authenticated, auth middleware, etc.) or other mitigation strategies.
4. Flags any endpoints that appear to be unprotected or insufficiently protected.
5. Identify any hardcoded secrets or credentials if present.

Format:
- List each potential issue briefly.
- Suggest a mitigation for each issue.
- If no issues are found, simply say "Security check passed: No unprotected sensitive endpoints identified."

Be terse and professional. No fluff.

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

	// Suppress unused warnings
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

	result, err := callGemini(*diff, metadata, geminiToken)
	if err != nil {
		fmt.Printf("Error calling Gemini: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(result)
}
