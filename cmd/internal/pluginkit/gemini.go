package pluginkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

const geminiEndpoint = "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?key="

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

// Schema is the subset of the OpenAPI schema dialect Gemini accepts as a
// response schema.
type Schema struct {
	Type             string             `json:"type"`
	Description      string             `json:"description,omitempty"`
	Enum             []string           `json:"enum,omitempty"`
	Items            *Schema            `json:"items,omitempty"`
	Properties       map[string]*Schema `json:"properties,omitempty"`
	PropertyOrdering []string           `json:"propertyOrdering,omitempty"`
	Required         []string           `json:"required,omitempty"`
}

type geminiGenerationConfig struct {
	ResponseMimeType string  `json:"responseMimeType,omitempty"`
	ResponseSchema   *Schema `json:"responseSchema,omitempty"`
}

type geminiRequest struct {
	Contents         []geminiContent         `json:"contents"`
	GenerationConfig *geminiGenerationConfig `json:"generationConfig,omitempty"`
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

// AnnotationsSchema describes an annotations array constrained to the fields
// of Annotation, for a plugin asking Gemini for structured output.
func AnnotationsSchema(max int) *Schema {
	return &Schema{
		Type:        "ARRAY",
		Description: "Line-level remarks, at most " + strconv.Itoa(max) + ".",
		Items: &Schema{
			Type: "OBJECT",
			Properties: map[string]*Schema{
				"filename": {
					Type:        "STRING",
					Description: "Path of the annotated file, copied exactly from the diff.",
				},
				"line": {
					Type:        "INTEGER",
					Description: "Head-side line number shown beside the annotated line.",
				},
				"severity": {
					Type: "STRING",
					Enum: []string{SeverityInfo, SeverityWarning, SeverityError},
				},
				"content": {
					Type:        "STRING",
					Description: "One or two sentences about that line.",
				},
			},
			PropertyOrdering: []string{"filename", "line", "severity", "content"},
			Required:         []string{"filename", "line", "severity", "content"},
		},
	}
}

// Generate sends a prompt to Gemini 2.5 Flash and returns the text of the
// first candidate. A non-nil schema asks for a JSON reply matching it.
func Generate(prompt string, schema *Schema, apiKey string) (string, error) {
	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}
	if schema != nil {
		reqBody.GenerationConfig = &geminiGenerationConfig{
			ResponseMimeType: "application/json",
			ResponseSchema:   schema,
		}
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	resp, err := http.Post(geminiEndpoint+apiKey, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var geminiResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&geminiResp); err != nil {
		return "", err
	}

	if len(geminiResp.Candidates) > 0 && len(geminiResp.Candidates[0].Content.Parts) > 0 {
		return geminiResp.Candidates[0].Content.Parts[0].Text, nil
	}

	return "", fmt.Errorf("no content in response")
}
