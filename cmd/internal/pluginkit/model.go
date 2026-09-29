package pluginkit

import (
	"bytes"
	"context"
	"crs/openrouter"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
)

// The LLM backends a bundled plugin can call. The server picks one per plugin
// from its [[Plugins]] entry (Provider, Model) and passes it in ProviderEnv and
// ModelEnv; with neither set, a plugin calls Gemini, as it always has.
const (
	ProviderGemini     = "gemini"
	ProviderOpenRouter = "openrouter"

	ProviderEnv = "CRS_LLM_PROVIDER"
	ModelEnv    = "CRS_LLM_MODEL"

	geminiKeyEnv = "GEMINI_API_KEY"
)

// Model is the LLM a plugin sends its prompt to.
type Model struct {
	provider string

	// Gemini
	geminiEndpoint string
	geminiKey      string

	// OpenRouter
	openrouter *openrouter.Client
}

// ModelFromEnv builds the model the server asked for in ProviderEnv and
// ModelEnv, with the provider's API key from the environment: GEMINI_API_KEY,
// or OPENROUTER_API_KEY. Its error is a sentence to show the user.
func ModelFromEnv() (*Model, error) {
	switch provider := strings.TrimSpace(os.Getenv(ProviderEnv)); provider {
	case "", ProviderGemini:
		key := os.Getenv(geminiKeyEnv)
		if key == "" {
			return nil, errors.New(geminiKeyEnv + " environment variable not set")
		}
		return &Model{provider: ProviderGemini, geminiEndpoint: geminiEndpoint, geminiKey: key}, nil
	case ProviderOpenRouter:
		client, err := openrouter.New(os.Getenv(ModelEnv))
		switch {
		case errors.Is(err, openrouter.ErrNoModel):
			return nil, fmt.Errorf("%s not set: the openrouter provider needs a model, e.g. anthropic/claude-sonnet-4.5", ModelEnv)
		case errors.Is(err, openrouter.ErrNoAPIKey):
			return nil, errors.New(openrouter.APIKeyEnv + " environment variable not set")
		case err != nil:
			return nil, err
		}
		return &Model{provider: ProviderOpenRouter, openrouter: client}, nil
	default:
		return nil, fmt.Errorf("unknown %s %q (expected %q or %q)", ProviderEnv, provider, ProviderGemini, ProviderOpenRouter)
	}
}

// String names the model for messages, e.g. "Gemini" or
// "OpenRouter (anthropic/claude-sonnet-4.5)".
func (m *Model) String() string {
	if m.provider == ProviderOpenRouter {
		return "OpenRouter (" + m.openrouter.Model + ")"
	}
	return "Gemini"
}

// Generate sends a prompt and returns the model's reply. A non-nil schema
// asks for a JSON reply matching it; a model that can't be held to one may
// still answer in prose, so callers keep their fallback for a reply that
// doesn't parse.
func (m *Model) Generate(prompt string, schema *Schema) (string, error) {
	if m.provider != ProviderOpenRouter {
		return generateGemini(m.geminiEndpoint, m.geminiKey, prompt, schema)
	}
	if schema == nil {
		return m.openrouter.Complete(context.Background(), prompt, nil)
	}
	rendered := schema.JSONSchema()
	encoded, err := json.MarshalIndent(rendered, "", "  ")
	if err != nil {
		return "", err
	}
	// Not every model behind OpenRouter supports structured outputs, and one
	// that doesn't ignores the schema; saying it in the prompt as well keeps
	// those answering in the shape the plugin parses.
	prompt += "\nReply with only a JSON document matching this JSON Schema, with no prose or code fence around it:\n" + string(encoded) + "\n"
	return m.openrouter.Complete(context.Background(), prompt, &openrouter.JSONSchema{Name: "response", Schema: rendered})
}

// JSONSchema renders the schema in the JSON Schema dialect OpenRouter's
// structured outputs take: lower-case types, and objects closed to properties
// they don't list, as strict mode requires. Properties keep PropertyOrdering,
// since models write them in schema order and a plugin asks for its summary
// before the annotations that follow from it.
func (s *Schema) JSONSchema() JSONSchema {
	out := JSONSchema{
		Type:        strings.ToLower(s.Type),
		Description: s.Description,
		Enum:        s.Enum,
		Required:    s.Required,
	}
	if s.Items != nil {
		items := s.Items.JSONSchema()
		out.Items = &items
	}
	if out.Type == "object" {
		closed := false
		out.AdditionalProperties = &closed
		names := make([]string, 0, len(s.Properties))
		for name := range s.Properties {
			if !slices.Contains(s.PropertyOrdering, name) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range append(slices.Clone(s.PropertyOrdering), names...) {
			if p, ok := s.Properties[name]; ok {
				out.Properties = append(out.Properties, NamedSchema{Name: name, Schema: p.JSONSchema()})
			}
		}
	}
	return out
}

// JSONSchema is a Schema in the JSON Schema dialect; see Schema.JSONSchema.
type JSONSchema struct {
	Type                 string      `json:"type"`
	Description          string      `json:"description,omitempty"`
	Enum                 []string    `json:"enum,omitempty"`
	Items                *JSONSchema `json:"items,omitempty"`
	Properties           Properties  `json:"properties,omitempty"`
	Required             []string    `json:"required,omitempty"`
	AdditionalProperties *bool       `json:"additionalProperties,omitempty"`
}

// NamedSchema is one entry of Properties.
type NamedSchema struct {
	Name   string
	Schema JSONSchema
}

// Properties is a JSON Schema properties object that keeps its order.
type Properties []NamedSchema

func (p Properties) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, prop := range p {
		if i > 0 {
			buf.WriteByte(',')
		}
		name, err := json.Marshal(prop.Name)
		if err != nil {
			return nil, err
		}
		schema, err := json.Marshal(prop.Schema)
		if err != nil {
			return nil, err
		}
		buf.Write(name)
		buf.WriteByte(':')
		buf.Write(schema)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}
