package chat

import (
	"crs/config"
	"encoding/json"
	"strings"
)

// Antigravity is Google's Antigravity CLI, `agy`, which speaks ACP as `agy
// acp`. It is the config.ChatProtocolAntigravity protocol, run for the
// built-in [Chat.Antigravity] agent and any [Chat.Agents] table naming it: an
// ACPBackend with the antigravityDialect, which covers what Antigravity does
// its own way.
//
//   - It answers initialize naming protocol version 2, in version 1's shape
//     (acpVersion1Shape lets any agent do that).
//   - It reports some of its own tools with kind "other": list_dir lists a
//     directory, update_steps keeps its checklist, invoke_subagent hands a
//     task to a subagent. Those that only read, plan or hand work on are let
//     through (antigravityTools); anything else of kind "other" still stops
//     the turn, as for any agent. Its titles only name the tool ("Running
//     list_dir"), so what a call is on is read from its arguments.
//   - Each conversation is switched to its "default" mode, the one that asks
//     before it edits, runs a command or calls an MCP tool, which the server
//     refuses. Its other modes act unasked.
//   - It asks the reviewer questions through permission requests whose tool
//     call IDs start "interaction_", the options its answers. The server
//     can't answer for them, so the question goes in the reply, for the
//     reviewer to answer in their next message.
//   - Its "refusal" stop reason means the account's quota ran out.
//   - It is signed in from a terminal: `agy` signs in with Google, and keeps
//     the credentials `agy acp` uses. One that isn't says so with ACP's
//     auth_required, with an internal error saying no browser is available,
//     or — where it would sign in over SSH — by printing a link on stdout and
//     waiting for a code it can never be given on an ACP connection, so the
//     server stops it there and says how to sign in.
//   - The server's own Google credentials (GEMINI_API_KEY, which the AI
//     features read, the Pub/Sub ones) would decide which account it bills,
//     so it doesn't get them unless its table's Env sets them.
//   - It is a one-file bundle that unpacks about 1 GB into TMPDIR each time it
//     starts, and leaves it there when killed, so each process gets a
//     temporary directory of its own, removed once it has exited.
//   - A session it says takes a system prompt (_meta.antigravity.systemPrompt)
//     is given the chat's with session/new rather than ahead of the first
//     message.
//
// Older releases (before 1.5.1) offer models other than Gemini only to the
// editors they know, which the server doesn't pretend to be.

// NewAntigravityBackend is the Antigravity agent with the given ID, reading
// its settings from settings.
func NewAntigravityBackend(id string, settings func() config.ChatBackendSettings) *ACPBackend {
	b := NewACPBackend(id, settings)
	b.dialect = antigravityDialect
	return b
}

// Antigravity's messages that say it isn't signed in.
const (
	// antigravitySignInLink starts the line it prints on stdout where it
	// would sign in over SSH, the link after it.
	antigravitySignInLink = "Open the following link to authenticate the ACP server:"
	// antigravityNoBrowser starts the internal error it answers authenticate
	// with where it has no sign-in and no browser to get one.
	antigravityNoBrowser = "No browser is available here"
)

var antigravityDialect = acpDialect{
	name:           "Antigravity",
	command:        "agy acp",
	mode:           "default",
	tools:          antigravityTools,
	questionPrefix: "interaction_",
	refusal: "Antigravity stopped: your Antigravity quota is used up for now. " +
		"Try again once it resets, or start a session with another model.",
	signIn: "running `agy` in a terminal and signing in with Google",
	needsSignIn: func(err *acpError) bool {
		return err.Code == acpErrInternal && strings.HasPrefix(strings.TrimSpace(err.Message), antigravityNoBrowser)
	},
	stdoutLine: func(line string) error {
		if strings.HasPrefix(line, antigravitySignInLink) {
			return &acpError{Code: acpErrAuthRequired, Message: "Antigravity is waiting for a Google sign-in, which it can't get over ACP"}
		}
		return nil
	},
	dropEnv: []string{
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI", "GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_QUOTA_PROJECT",
	},
	privateTemp: true,
	sessionMeta: antigravitySessionMeta,
}

// antigravityTools are Antigravity's own tools, by name: those that only
// read, plan or hand work on, let through whatever kind it reports them as,
// and how a call to each is shown. A subagent is one more Antigravity, in the
// same mode: it asks before it acts, and the server answers it as it does the
// agent itself.
var antigravityTools = map[string]acpNamedTool{
	"list_dir":          {tool: "List", readOnly: true, detail: antigravityInput("DirectoryPath", "directory_path")},
	"view_file":         {tool: "Read", readOnly: true, detail: antigravityInput("AbsolutePath", "absolute_path")},
	"client_view_file":  {tool: "Read", readOnly: true, detail: antigravityInput("AbsolutePath", "absolute_path")},
	"view_file_outline": {tool: "Read", readOnly: true, detail: antigravityInput("AbsolutePath", "absolute_path")},
	"view_code_item":    {tool: "Read", readOnly: true, detail: antigravityInput("File", "file")},
	"grep_search":       {tool: "Search", readOnly: true, detail: antigravityInput("Query", "query")},
	"find_by_name":      {tool: "Search", readOnly: true, detail: antigravityInput("Pattern", "pattern")},
	"codebase_search":   {tool: "Search", readOnly: true, detail: antigravityInput("Query", "query")},
	"update_steps":      {tool: "Plan", readOnly: true, detail: antigravityStep},
	"invoke_subagent":   {tool: "Subagent", readOnly: true, detail: antigravityInput("Role", "role")},
	"start_subagent":    {tool: "Subagent", readOnly: true, detail: antigravityInput("Role", "role")},
	// Refused, and shown by the command they would run.
	"run_command": {detail: antigravityInput("CommandLine", "command_line", "commandLine", "command")},
	"command":     {detail: antigravityInput("CommandLine", "command_line", "commandLine", "command")},
}

// antigravityInput picks the first of fields a call's arguments give as text.
func antigravityInput(fields ...string) func(map[string]json.RawMessage) string {
	return func(input map[string]json.RawMessage) string {
		for _, f := range fields {
			var s string
			if json.Unmarshal(input[f], &s) == nil && strings.TrimSpace(s) != "" {
				return s
			}
		}
		return ""
	}
}

// antigravityStep is the step of Antigravity's checklist it is on, from an
// update_steps call's arguments.
func antigravityStep(input map[string]json.RawMessage) string {
	var steps []struct {
		Title  string `json:"title"`
		Status string `json:"status"`
	}
	if json.Unmarshal(input["steps"], &steps) != nil {
		return ""
	}
	for _, s := range steps {
		if s.Status == "in_progress" {
			return s.Title
		}
	}
	return ""
}

// antigravitySessionMeta gives a session the chat's system prompt, when
// Antigravity's initialize says it takes one
// (agentCapabilities._meta.antigravity.systemPrompt.append).
func antigravitySessionMeta(caps acpAgentCapabilities, systemPrompt string) map[string]any {
	var meta struct {
		Antigravity struct {
			SystemPrompt struct {
				Append bool `json:"append"`
			} `json:"systemPrompt"`
		} `json:"antigravity"`
	}
	if json.Unmarshal(caps.Meta, &meta) != nil || !meta.Antigravity.SystemPrompt.Append {
		return nil
	}
	return map[string]any{"antigravity": map[string]any{"systemPrompt": map[string]any{"append": systemPrompt}}}
}
