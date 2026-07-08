package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Gemini is the backend behind DefaultClient today. It talks to the Google
// Generative Language REST API using the GEMINI_API_KEY environment
// variable.

const (
	geminiModel   = "gemini-2.5-flash"
	geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	geminiTimeout = 30 * time.Second
)

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

// GeminiClient implements Client on top of the Gemini REST API.
type GeminiClient struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// NewGeminiClient builds a Gemini client from the GEMINI_API_KEY environment
// variable, failing when the key is not set.
func NewGeminiClient() (*GeminiClient, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, &CallError{Stage: StageClientInit, Err: fmt.Errorf("GEMINI_API_KEY not set")}
	}
	return &GeminiClient{
		apiKey:  apiKey,
		model:   geminiModel,
		baseURL: geminiBaseURL,
		http:    &http.Client{Timeout: geminiTimeout},
	}, nil
}

func (c *GeminiClient) Provider() string { return "gemini" }

func (c *GeminiClient) Model() string { return c.model }

// Generate sends the prompt to Gemini and returns the text of the first
// candidate. Failures come back as *CallError values attributed to the stage
// that failed.
func (c *GeminiClient) Generate(prompt string) (string, error) {
	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", &CallError{Stage: StageRequest, Err: err}
	}

	url := c.baseURL + "/models/" + c.model + ":generateContent?key=" + c.apiKey
	resp, err := c.http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", &CallError{Stage: StageRequest, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &CallError{
			Stage: StageHTTPStatus,
			Err:   fmt.Errorf("Gemini API request failed with status %d: %s", resp.StatusCode, string(body)),
		}
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", &CallError{Stage: StageDecode, Err: err}
	}
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", &CallError{Stage: StageEmptyResponse, Err: fmt.Errorf("no content in Gemini response")}
	}
	return geminiResp.Candidates[0].Content.Parts[0].Text, nil
}
