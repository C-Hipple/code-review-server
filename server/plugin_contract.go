package server

import (
	"crs/config"
	"crs/database"
	"encoding/json"
	"sort"
	"strings"
)

// Plugin response contract
//
// Plugins historically wrote free-form text to stdout and clients rendered it
// verbatim. A plugin may now instead emit a JSON document matching the
// contract below, which lets it declare how its body should be rendered and
// attach line-level annotations to the PR diff:
//
//	{
//	  "body": {
//	    "body_type": "markdown",
//	    "body_content": "This is the response."
//	  },
//	  "annotations": [
//	    {"filename": "test.py", "line": 75, "severity": "warning", "content": "this line looks wrong"}
//	  ]
//	}
//
// Output that does not match the contract — non-JSON output, JSON without a
// body object, or an unknown body_type — is treated as legacy output and
// wrapped as {"body_type": "markdown", "body_content": <raw output>} with no
// annotations, so existing plugins keep working unchanged.

const (
	BodyTypeMarkdown = "markdown"
	BodyTypeHTML     = "html"
)

// PluginBody is the renderable portion of a plugin response.
type PluginBody struct {
	// BodyType is either "markdown" or "html".
	BodyType    string `json:"body_type"`
	BodyContent string `json:"body_content"`
}

// PluginAnnotation anchors a plugin remark to a line of a file in the PR.
// Filename and Line are required for an annotation to be kept; Severity and
// Content are free-form.
type PluginAnnotation struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Content  string `json:"content"`
}

// PluginResponse is the parsed form of a plugin's stdout.
type PluginResponse struct {
	Body        PluginBody         `json:"body"`
	Annotations []PluginAnnotation `json:"annotations"`
}

// rawPluginResponse mirrors PluginResponse with a pointer body so parsing can
// tell "no body key" apart from an empty one.
type rawPluginResponse struct {
	Body        *PluginBody        `json:"body"`
	Annotations []PluginAnnotation `json:"annotations"`
}

// ParsePluginOutput parses raw plugin stdout against the response contract.
// Output that doesn't conform is returned whole as a markdown body with no
// annotations. Annotations missing a filename or a positive line number are
// dropped, since they can't be anchored to the diff.
func ParsePluginOutput(raw string) PluginResponse {
	fallback := PluginResponse{
		Body:        PluginBody{BodyType: BodyTypeMarkdown, BodyContent: raw},
		Annotations: []PluginAnnotation{},
	}

	var parsed rawPluginResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed.Body == nil {
		return fallback
	}
	bodyType := strings.ToLower(strings.TrimSpace(parsed.Body.BodyType))
	if bodyType != BodyTypeMarkdown && bodyType != BodyTypeHTML {
		return fallback
	}

	response := PluginResponse{
		Body:        PluginBody{BodyType: bodyType, BodyContent: parsed.Body.BodyContent},
		Annotations: []PluginAnnotation{},
	}
	for _, a := range parsed.Annotations {
		if a.Filename == "" || a.Line < 1 {
			continue
		}
		response.Annotations = append(response.Annotations, a)
	}
	return response
}

// PluginOutput is the client-facing view of a stored plugin result: the raw
// captured stdout (kept so existing clients keep working) plus the parsed
// response contract.
type PluginOutput struct {
	Result      string             `json:"result"`
	Status      string             `json:"status"`
	Body        PluginBody         `json:"body"`
	Annotations []PluginAnnotation `json:"annotations"`
}

// parsePluginOutputs converts stored plugin results to their client-facing
// view, parsing each stored result against the response contract.
func parsePluginOutputs(results map[string]database.PluginResult) map[string]PluginOutput {
	outputs := make(map[string]PluginOutput, len(results))
	for name, res := range results {
		parsed := ParsePluginOutput(res.Result)
		outputs[name] = PluginOutput{
			Result:      res.Result,
			Status:      res.Status,
			Body:        parsed.Body,
			Annotations: parsed.Annotations,
		}
	}
	return outputs
}

// PRAnnotation is a plugin annotation aggregated into a PR reply, tagged with
// the plugin that produced it.
type PRAnnotation struct {
	PluginAnnotation
	Plugin string `json:"plugin"`
}

// collectPluginAnnotations gathers the annotations from every plugin that has
// already run successfully for the PR, grouped by plugin in name order so the
// result is deterministic.
func collectPluginAnnotations(owner, repo string, number int) ([]PRAnnotation, error) {
	results, err := config.C().DB.GetPluginResults(owner, repo, number)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(results))
	for name, res := range results {
		if res.Status == "success" {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	annotations := []PRAnnotation{}
	for _, name := range names {
		for _, a := range ParsePluginOutput(results[name].Result).Annotations {
			annotations = append(annotations, PRAnnotation{PluginAnnotation: a, Plugin: name})
		}
	}
	return annotations, nil
}
