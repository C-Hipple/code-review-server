package chat

import (
	"context"
	"crs/config"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
)

// A Protocol is a way of running a chat agent: the program's command line,
// and the messages it speaks. Each agent the config defines names one ([Chat]
// in config), and the agent's Backend is made by it. Adding a kind of agent is
// registering a Protocol for it (and naming it in config.ChatProtocols, so
// validation knows it), not touching the Manager.
//
// settings returns the agent's table as config has it right now; a Backend
// reads it on every use, so a config change applies from the next turn.
type Protocol func(id string, settings func() config.ChatBackendSettings) Backend

var (
	protocolsMu sync.RWMutex
	protocols   = map[string]Protocol{
		config.ChatProtocolClaudeCode: func(id string, settings func() config.ChatBackendSettings) Backend {
			return NewClaudeBackend(id, settings)
		},
		config.ChatProtocolACP: func(id string, settings func() config.ChatBackendSettings) Backend {
			return NewACPBackend(id, settings)
		},
	}
)

// RegisterProtocol adds (or replaces) the protocol called name.
func RegisterProtocol(name string, p Protocol) {
	protocolsMu.Lock()
	defer protocolsMu.Unlock()
	protocols[name] = p
}

// LookupProtocol finds the protocol called name.
func LookupProtocol(name string) (Protocol, bool) {
	protocolsMu.RLock()
	defer protocolsMu.RUnlock()
	p, ok := protocols[name]
	return p, ok
}

// Protocols are the names of the registered protocols, sorted.
func Protocols() []string {
	protocolsMu.RLock()
	defer protocolsMu.RUnlock()
	return slices.Sorted(maps.Keys(protocols))
}

// Source says which agents chats can talk to.
type Source interface {
	// Backends lists every agent a chat may talk to, in the order to list
	// them. A chat carries on with the agent it started with while that agent
	// is still listed.
	Backends() []Backend
	// Default is the ID of the agent new chats talk to; empty means the first
	// available one.
	Default() string
}

// Fixed is a Source of a fixed list of backends, whose new chats talk to the
// first available one.
type Fixed []Backend

func (f Fixed) Backends() []Backend { return f }
func (f Fixed) Default() string     { return "" }

// ConfigSource is the agents the server's config defines: the built-in Claude
// Code one and each [Chat.Agents] table, new chats talking to [Chat] Agent.
// An agent's Backend is made once per ID and protocol and then kept, since
// one may hold a running agent between turns; it reads its settings from
// config on every use.
type ConfigSource struct {
	// Config returns the configuration as it is now.
	Config func() config.Config

	mu   sync.Mutex
	made map[string]madeBackend
}

type madeBackend struct {
	protocol string
	backend  Backend
}

// NewConfigSource is the agents config.C() defines.
func NewConfigSource() *ConfigSource {
	return &ConfigSource{Config: config.C}
}

func (s *ConfigSource) Default() string { return s.Config().ChatAgent() }

func (s *ConfigSource) Backends() []Backend {
	cfg := s.Config()
	ids := cfg.ChatAgentIDs()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.made == nil {
		s.made = map[string]madeBackend{}
	}
	backends := make([]Backend, 0, len(ids))
	for _, id := range ids {
		protocol := cfg.ChatBackend(id).Protocol
		made, ok := s.made[id]
		if !ok || made.protocol != protocol {
			if ok {
				release(made.backend)
			}
			made = madeBackend{protocol: protocol, backend: s.newBackend(id, protocol)}
			s.made[id] = made
		}
		backends = append(backends, made.backend)
	}
	// An agent taken out of the config lets go of what it holds.
	for id, made := range s.made {
		if !slices.Contains(ids, id) {
			release(made.backend)
			delete(s.made, id)
		}
	}
	return backends
}

func (s *ConfigSource) newBackend(id, protocol string) Backend {
	settings := func() config.ChatBackendSettings { return s.Config().ChatBackend(id) }
	if p, ok := LookupProtocol(protocol); ok {
		return p(id, settings)
	}
	return unknownProtocol{id: id, protocol: protocol, settings: settings}
}

// Releaser is a Backend that holds something for a chat between its turns —
// a running agent — and can let go of it.
type Releaser interface {
	// Release lets go of what is held for the chat whose working directory is
	// workDir.
	Release(workDir string)
	// ReleaseAll lets go of everything held.
	ReleaseAll()
}

func release(b Backend) {
	if r, ok := b.(Releaser); ok {
		r.ReleaseAll()
	}
}

// unknownProtocol stands in for an agent whose Protocol is none the server
// speaks, so it is listed, and says why it can't run.
type unknownProtocol struct {
	id       string
	protocol string
	settings func() config.ChatBackendSettings
}

func (u unknownProtocol) ID() string { return u.id }

func (u unknownProtocol) Info() BackendInfo {
	name := strings.TrimSpace(u.settings().Name)
	if name == "" {
		name = u.id
	}
	return BackendInfo{
		ID: u.id, Name: name,
		Reason: fmt.Sprintf("its protocol %q isn't one the server speaks (%s Protocol: one of %s)",
			u.protocol, config.ChatTable(u.id), strings.Join(Protocols(), ", ")),
		Models: []Model{{ID: "", Name: "Default"}},
	}
}

func (u unknownProtocol) Turn(context.Context, Turn, func(Event)) (TurnResult, error) {
	return TurnResult{}, fmt.Errorf("%s isn't available: %s", u.id, u.Info().Reason)
}

// agentEnv is the server's environment for an agent's run: less the marker a
// Claude Code session sets for its own children (a server started from one
// would otherwise have its chats taken for nested sessions), plus the agent's
// configured Env over it.
func agentEnv(env []string, extra map[string]string) []string {
	out := make([]string, 0, len(env)+len(extra))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if key == "CLAUDECODE" {
			continue
		}
		if _, replaced := extra[key]; replaced {
			continue
		}
		out = append(out, kv)
	}
	for _, key := range slices.Sorted(maps.Keys(extra)) {
		out = append(out, key+"="+extra[key])
	}
	return out
}

// agentName is what clients call an agent: its configured Name, else
// fallback.
func agentName(s config.ChatBackendSettings, fallback string) string {
	if name := strings.TrimSpace(s.Name); name != "" {
		return name
	}
	return fallback
}

// modelChoices are the models an agent offers: its own default first, then
// the configured ones, else builtin.
func modelChoices(s config.ChatBackendSettings, builtin []string) []Model {
	models := []Model{{ID: "", Name: "Default"}}
	ids := s.Models
	if len(ids) == 0 {
		ids = builtin
	}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			models = append(models, Model{ID: id, Name: modelName(id)})
		}
	}
	return models
}
