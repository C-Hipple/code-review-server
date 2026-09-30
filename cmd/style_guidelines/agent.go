package main

import (
	"context"
	"crs/cmd/internal/pluginkit"
	"encoding/json"
	"fmt"
	"time"
)

// conversation is the part of pluginkit.Chat the loop drives, so tests can
// script the model's side.
type conversation interface {
	AddUser(text string)
	AddToolResults(results []pluginkit.ToolResult)
	Next(ctx context.Context, require string) (pluginkit.ChatTurn, error)
}

// tool is a tool the loop runs for the model.
type tool struct {
	pluginkit.ChatTool
	run func(args json.RawMessage) (string, error)
}

// agentTask is one agentic phase: a prompt, the tools the model may call while
// it works, and the submit tool whose call is its answer.
type agentTask struct {
	prompt string
	tools  []tool
	submit pluginkit.ChatTool
	// accept takes the submit tool's arguments. An error goes back to the
	// model as the call's result, so it can fix the answer and submit again;
	// last is set when it can't, and accept should then keep what it can.
	accept   func(args json.RawMessage, last bool) error
	maxTurns int
}

// agentStats describes a finished phase, for the report's footer.
type agentStats struct {
	Turns     int
	ToolCalls int
}

// finishReserve is the time a phase keeps back to submit its answer: once its
// deadline is closer than this, the model is made to submit on the next turn.
const finishReserve = 45 * time.Second

// chatTools is what the conversation offers the model: the task's tools and
// its submit tool.
func (t agentTask) chatTools() []pluginkit.ChatTool {
	specs := make([]pluginkit.ChatTool, 0, len(t.tools)+1)
	for _, tl := range t.tools {
		specs = append(specs, tl.ChatTool)
	}
	return append(specs, t.submit)
}

// runAgent runs the loop: ask the model for a turn, run the tools it calls and
// hand back their results, until it calls the submit tool with an answer
// accept takes. On the last turn — the budget spent, or the deadline near —
// the model is made to call the submit tool.
func runAgent(ctx context.Context, conv conversation, task agentTask) (agentStats, error) {
	byName := make(map[string]tool, len(task.tools))
	for _, tl := range task.tools {
		byName[tl.Name] = tl
	}

	var stats agentStats
	conv.AddUser(task.prompt)
	for turn := 1; turn <= task.maxTurns; turn++ {
		stats.Turns = turn
		last := turn == task.maxTurns
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < finishReserve {
			last = true
		}
		require := ""
		if last {
			require = task.submit.Name
		}

		reply, err := conv.Next(ctx, require)
		if err != nil {
			return stats, err
		}
		if len(reply.ToolCalls) == 0 {
			if last {
				break
			}
			conv.AddUser(fmt.Sprintf("Only a call to %s is read as your answer. Keep working with the tools, or call %s now.",
				task.submit.Name, task.submit.Name))
			continue
		}

		results := make([]pluginkit.ToolResult, 0, len(reply.ToolCalls))
		for _, call := range reply.ToolCalls {
			stats.ToolCalls++
			var content string
			switch tl, known := byName[call.Name]; {
			case call.Name == task.submit.Name:
				if err := task.accept(call.Args, last); err != nil {
					content = fmt.Sprintf("Error: the answer was not accepted: %v\nFix it and call %s again.", err, task.submit.Name)
					break
				}
				return stats, nil
			case !known:
				content = fmt.Sprintf("Error: there is no tool named %q.", call.Name)
			default:
				out, err := tl.run(call.Args)
				if err != nil {
					content = "Error: " + err.Error()
				} else {
					content = out
				}
			}
			results = append(results, pluginkit.ToolResult{Call: call, Content: content})
		}
		if last {
			break
		}
		conv.AddToolResults(results)
	}
	return stats, fmt.Errorf("the model did not submit an answer with %s within %d turns", task.submit.Name, stats.Turns)
}
