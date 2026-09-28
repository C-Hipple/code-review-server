package ai

import (
	"context"
	"crs/llm"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func echoTool(calls *[]string) Tool {
	return Tool{
		Name:        "echo",
		Description: "Echo a word.",
		Arguments:   `{"word": "..."}`,
		Call: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Word string `json:"word"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", err
			}
			*calls = append(*calls, a.Word)
			if a.Word == "fail" {
				return "", errors.New("echo refuses")
			}
			return "echo: " + a.Word, nil
		},
	}
}

func TestAgentAnswersWithoutTools(t *testing.T) {
	model := &scriptedProvider{t: t, answers: []string{"  the answer  "}}
	res, err := NewProviderAgent(model).Run(context.Background(), AgentTask{Prompt: "Do it."})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "the answer" || res.Turns != 1 || len(res.ToolCalls) != 0 {
		t.Errorf("unexpected result: %+v", res)
	}
	if !strings.Contains(model.prompts[0], "No tools are available") {
		t.Errorf("a toolless task should say so:\n%s", model.prompts[0])
	}
}

func TestAgentRunsToolsAndFeedsBackTheirResults(t *testing.T) {
	var echoed []string
	model := &scriptedProvider{t: t, answers: []string{
		"```\nTOOL_CALL {\"name\": \"echo\", \"arguments\": {\"word\": \"hello\"}}\n```",
		`TOOL_CALL {"name": "echo", "arguments": {"word": "fail"}}`,
		`TOOL_CALL {"name": "grep", "arguments": {}}`,
		`TOOL_CALL {not json}`,
		"done",
	}}
	res, err := NewProviderAgent(model).Run(context.Background(), AgentTask{
		Prompt:   "Do it.",
		Tools:    []Tool{echoTool(&echoed)},
		MaxTurns: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Answer != "done" || res.Turns != 5 || len(res.ToolCalls) != 4 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if strings.Join(echoed, ",") != "hello,fail" {
		t.Errorf("tool calls = %v", echoed)
	}
	if res.ToolCalls[0].Result != "echo: hello" || res.ToolCalls[1].Err != "echo refuses" ||
		!strings.Contains(res.ToolCalls[2].Err, `no tool named "grep"`) ||
		!strings.Contains(res.ToolCalls[3].Err, "could not read the tool call") {
		t.Errorf("tool call records: %+v", res.ToolCalls)
	}

	last := model.prompts[4]
	for _, want := range []string{
		"- echo: Echo a word. Arguments: {\"word\": \"...\"}",
		"[echo result]\necho: hello",
		"[echo error] echo refuses",
		"[grep error] there is no tool named",
		"This is your last turn",
	} {
		if !strings.Contains(last, want) {
			t.Errorf("final prompt missing %q:\n%s", want, last)
		}
	}
	if strings.Contains(model.prompts[3], "This is your last turn") {
		t.Error("only the final turn says it is the last")
	}
}

func TestAgentGivesUpWhenTheModelNeverAnswers(t *testing.T) {
	var echoed []string
	call := `TOOL_CALL {"name": "echo", "arguments": {"word": "again"}}`
	model := &scriptedProvider{t: t, answers: []string{call, call}}
	res, err := NewProviderAgent(model).Run(context.Background(), AgentTask{
		Prompt: "Do it.", Tools: []Tool{echoTool(&echoed)}, MaxTurns: 2,
	})
	wantStage(t, err, llm.StageParse)
	if res.Turns != 2 || len(echoed) != 1 {
		t.Errorf("turns %d, tool ran %d times: the last turn's call must not run", res.Turns, len(echoed))
	}
}

func TestAgentPassesModelErrorsThrough(t *testing.T) {
	model := &scriptedProvider{t: t, err: &llm.CallError{Stage: llm.StageHTTPStatus, Err: errors.New("429")}}
	_, err := NewProviderAgent(model).Run(context.Background(), AgentTask{Prompt: "Do it."})
	wantStage(t, err, llm.StageHTTPStatus)
}

func TestTruncateTextKeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("é", 10) // 2 bytes each
	got := truncateText(s, 5)
	if !strings.HasPrefix(got, "éé\n") || !strings.HasSuffix(got, "(truncated)") {
		t.Errorf("truncateText = %q", got)
	}
	if truncateText("short", 10) != "short" {
		t.Error("short text should pass through")
	}
}
