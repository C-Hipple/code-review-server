package ai

import (
	"context"
	"crs/llm"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Agent is the seam for multi-turn work: the model may call tools the feature
// provides, see their results, and call more, before it answers. One-shot
// features never touch it; it exists so agentic ones register on the same
// runtime rather than growing a second one.
type Agent interface {
	// Name and Model describe the backend, as for a Provider.
	Name() string
	Model() string
	Run(ctx context.Context, task AgentTask) (AgentResult, error)
}

// Tool is a callback an agentic feature offers the model.
type Tool struct {
	Name        string
	Description string
	// Arguments describes the JSON object the tool takes, shown to the model
	// as is, e.g. `{"path": "file path relative to the repository root"}`.
	Arguments string
	Call      func(ctx context.Context, args json.RawMessage) (string, error)
}

// AgentTask is one agentic job.
type AgentTask struct {
	// Prompt is the job: instructions, inputs, and the answer format wanted.
	Prompt string
	Tools  []Tool
	// MaxTurns bounds the model calls; zero means DefaultMaxTurns.
	MaxTurns int
}

// ToolCall records one call the model made.
type ToolCall struct {
	Name   string
	Args   json.RawMessage
	Result string
	Err    string
}

// AgentResult is what an agent run produced.
type AgentResult struct {
	// Answer is the model's final reply, in the format the task asked for.
	Answer    string
	Turns     int
	ToolCalls []ToolCall
}

// DefaultMaxTurns bounds an agent run that doesn't set its own budget.
const DefaultMaxTurns = 6

// maxToolResultBytes caps what one tool call can add to the transcript, which
// is resent on every turn.
const maxToolResultBytes = 20000

// toolCallMarker starts the one line a model replies with to call a tool.
const toolCallMarker = "TOOL_CALL"

// NewProviderAgent runs the agent loop on a one-shot Provider. Tool calls go
// through a plain-text protocol — the model replies with a TOOL_CALL line, the
// loop runs the tool and asks again with the result appended — so every
// provider can act as an agent, including a CLI that knows nothing about this
// server's tools. Each turn resends the whole transcript.
func NewProviderAgent(p Provider) Agent {
	return &providerAgent{p: p}
}

type providerAgent struct {
	p Provider
}

func (a *providerAgent) Name() string  { return a.p.Name() }
func (a *providerAgent) Model() string { return a.p.Model() }

func (a *providerAgent) Run(ctx context.Context, task AgentTask) (AgentResult, error) {
	maxTurns := task.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	tools := make(map[string]Tool, len(task.Tools))
	for _, t := range task.Tools {
		tools[t.Name] = t
	}

	var res AgentResult
	var transcript strings.Builder
	for turn := 1; turn <= maxTurns; turn++ {
		res.Turns = turn
		text, err := a.p.Generate(ctx, renderAgentPrompt(task, transcript.String(), turn == maxTurns))
		if err != nil {
			return res, err
		}

		line, call, isCall := findToolCall(text)
		if !isCall {
			res.Answer = strings.TrimSpace(text)
			return res, nil
		}
		if turn == maxTurns {
			break
		}

		record := ToolCall{Name: call.Name, Args: call.Arguments}
		tool, known := tools[call.Name]
		switch {
		case call.Name == "":
			record.Err = fmt.Sprintf("could not read the tool call %q: expected %s {\"name\": ..., \"arguments\": {...}}", line, toolCallMarker)
		case !known:
			record.Err = fmt.Sprintf("there is no tool named %q", call.Name)
		default:
			out, err := tool.Call(ctx, call.Arguments)
			if err != nil {
				record.Err = err.Error()
			} else {
				record.Result = truncateText(out, maxToolResultBytes)
			}
		}
		res.ToolCalls = append(res.ToolCalls, record)

		transcript.WriteString("[you] " + line + "\n")
		if record.Err != "" {
			transcript.WriteString(fmt.Sprintf("[%s error] %s\n\n", call.Name, record.Err))
		} else {
			transcript.WriteString(fmt.Sprintf("[%s result]\n%s\n\n", call.Name, record.Result))
		}
	}
	return res, &llm.CallError{Stage: llm.StageParse,
		Err: fmt.Errorf("the model was still calling tools after %d turns", maxTurns)}
}

// renderAgentPrompt is the task with the tool protocol and the transcript so
// far appended.
func renderAgentPrompt(task AgentTask, transcript string, lastTurn bool) string {
	var b strings.Builder
	b.WriteString(task.Prompt)
	b.WriteString("\n\n## Tools\n")
	if len(task.Tools) == 0 {
		b.WriteString("No tools are available for this task: answer from what is above.\n")
	} else {
		b.WriteString("Before answering you may call a tool to gather more information. To call one, reply with nothing but a single line:\n")
		b.WriteString(toolCallMarker + ` {"name": "<tool name>", "arguments": {...}}` + "\n")
		b.WriteString("You will be shown the result and asked again. When you are ready, reply with the answer alone, in the format asked for above.\n\nAvailable tools:\n")
		for _, t := range task.Tools {
			b.WriteString(fmt.Sprintf("- %s: %s Arguments: %s\n", t.Name, t.Description, t.Arguments))
		}
	}
	if transcript != "" {
		b.WriteString("\n## Tool calls so far\n")
		b.WriteString(transcript)
	}
	if lastTurn && len(task.Tools) > 0 {
		b.WriteString("\nThis is your last turn: do not call any more tools; answer now from what you have.\n")
	}
	return b.String()
}

type toolCallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// findToolCall reports whether a reply is a tool call: a line that starts with
// TOOL_CALL (ignoring indentation and code-fence backticks). It returns the
// line as written and the call it names; a call whose JSON doesn't parse comes
// back with an empty Name, so the loop can tell the model what went wrong.
func findToolCall(text string) (string, toolCallRequest, bool) {
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "`"))
		if !strings.HasPrefix(line, toolCallMarker) {
			continue
		}
		var call toolCallRequest
		payload := strings.TrimSpace(strings.TrimPrefix(line, toolCallMarker))
		if err := json.Unmarshal([]byte(payload), &call); err != nil {
			return line, toolCallRequest{}, true
		}
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage("{}")
		}
		return line, call, true
	}
	return "", toolCallRequest{}, false
}

// truncateText cuts s to at most max bytes, on a UTF-8 boundary, and says so.
func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n... (truncated)"
}
