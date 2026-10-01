package ai

import (
	"crs/config"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Registry holds the features the server knows about. A feature exists by
// being registered; config only switches it on and picks how it runs.
type Registry struct {
	mu       sync.RWMutex
	features map[string]Feature
	order    []string
}

func NewRegistry() *Registry {
	return &Registry{features: make(map[string]Feature)}
}

// DefaultRegistry holds the built-in features. The RPCs, config validation and
// DefaultRunner all read it, so they agree on what exists.
var DefaultRegistry = NewRegistry()

func init() {
	DefaultRegistry.MustRegister(CommentsAddressed{})
	DefaultRegistry.MustRegister(FeatureFlags{})
	DefaultRegistry.MustRegister(ChangeDiagram{})
	DefaultRegistry.MustRegister(FileOrdering{})
	DefaultRegistry.MustRegister(ReviewEase{})
}

// Register adds a feature. IDs must be unique and non-empty.
func (r *Registry) Register(f Feature) error {
	id := f.ID()
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("AI feature has no ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.features[id]; dup {
		return fmt.Errorf("AI feature %q is already registered", id)
	}
	r.features[id] = f
	r.order = append(r.order, id)
	return nil
}

// MustRegister is Register for built-ins, where a clash is a programming error.
func (r *Registry) MustRegister(f Feature) {
	if err := r.Register(f); err != nil {
		panic(err)
	}
}

// Get returns the feature registered under id.
func (r *Registry) Get(id string) (Feature, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.features[id]
	return f, ok
}

// Features returns every registered feature in registration order.
func (r *Registry) Features() []Feature {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Feature, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.features[id])
	}
	return out
}

// modesOf lists the execution modes a feature supports, default first.
func modesOf(f Feature) []string {
	if ms, ok := f.(ModeSupporter); ok && len(ms.Modes()) > 0 {
		return ms.Modes()
	}
	return []string{config.AIModeOneShot}
}

// resolveMode is the mode a feature runs in under its config entry.
func resolveMode(f Feature, entry config.AIFeature) string {
	if entry.Mode != "" {
		return entry.Mode
	}
	return modesOf(f)[0]
}

// Validate checks [[AIFeatures]] entries against the registry: each ID must
// name a registered feature and each mode must be one the feature supports.
// config.Validate covers everything that needs no registry, the same split as
// workflows.ValidateWorkflows.
func (r *Registry) Validate(entries []config.AIFeature) []config.ValidationError {
	var problems []config.ValidationError
	for i, entry := range entries {
		if strings.TrimSpace(entry.ID) == "" {
			continue // config.Validate reports the missing ID
		}
		f, ok := r.Get(entry.ID)
		if !ok {
			problems = append(problems, config.ValidationError{
				Workflow: -1,
				Field:    fmt.Sprintf("AIFeatures[%d].ID", i),
				Message:  fmt.Sprintf("unknown AI feature %q (known: %s)", entry.ID, strings.Join(r.ids(), ", ")),
			})
			continue
		}
		if entry.Mode != "" && !slices.Contains(modesOf(f), entry.Mode) {
			problems = append(problems, config.ValidationError{
				Workflow: -1,
				Field:    fmt.Sprintf("AIFeatures[%d].Mode", i),
				Message:  fmt.Sprintf("%s does not run in mode %q (supported: %s)", entry.ID, entry.Mode, strings.Join(modesOf(f), ", ")),
			})
		}
	}
	return problems
}

func (r *Registry) ids() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.order)
}

// ValidateFeatures checks [[AIFeatures]] entries against the built-in features.
func ValidateFeatures(entries []config.AIFeature) []config.ValidationError {
	return DefaultRegistry.Validate(entries)
}

// Info is a registered feature as clients see it: what it is, merged with how
// config says it runs.
type Info struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Automatic   bool     `json:"automatic"`
	Mode        string   `json:"mode"`
	Modes       []string `json:"modes"`
	// Provider is the provider the feature would run with; it is resolved from
	// config, not checked, so it can name one that isn't set up.
	Provider string `json:"provider"`
	// Applied is set for a feature whose result the server applies to what it
	// already serves (AppliedFeature), so there is no report for a client to
	// open.
	Applied bool `json:"applied"`
}

// TypeInfo is a registered feature as a config editor needs it: what it is and
// which settings it takes, the way workflows.WorkflowTypeInfo describes a
// workflow type. Unlike Info it says nothing about how config sets it up,
// except for LegacyKey and Legacy.
type TypeInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Modes lists the execution modes the feature supports, default first.
	Modes   []string `json:"modes"`
	Applied bool     `json:"applied"`
	// LegacyKey names the root-level key that switches the feature on in the
	// config file (ExperimentalLLMFileOrdering, ExperimentalLLMReviewEase), and
	// Legacy is the [[AIFeatures]] entry it stands for. Both are empty unless
	// such a flag is on. The flag applies only while the file has no entry of
	// its own for the feature.
	LegacyKey string            `json:"legacy_key,omitempty"`
	Legacy    *config.AIFeature `json:"legacy,omitempty"`
}

// Types returns every registered feature for a config editor's pickers, with
// the legacy flags cfg sets.
func (r *Registry) Types(cfg config.Config) []TypeInfo {
	features := r.Features()
	out := make([]TypeInfo, 0, len(features))
	for _, f := range features {
		info := TypeInfo{
			ID:      f.ID(),
			Name:    f.Name(),
			Modes:   modesOf(f),
			Applied: IsApplied(f),
		}
		if d, ok := f.(Describer); ok {
			info.Description = d.Description()
		}
		if key, entry, ok := cfg.LegacyAIFeature(f.ID()); ok {
			info.LegacyKey = key
			info.Legacy = &entry
		}
		out = append(out, info)
	}
	return out
}

// Describe returns every registered feature, enabled or not, as cfg configures
// it. Disabled ones are listed too so a client can say what could be turned on.
func (r *Registry) Describe(cfg config.Config) []Info {
	features := r.Features()
	out := make([]Info, 0, len(features))
	for _, f := range features {
		entry, _ := cfg.AIFeatureSettings(f.ID())
		info := Info{
			ID:        f.ID(),
			Name:      f.Name(),
			Enabled:   entry.Enabled,
			Automatic: entry.Enabled && entry.Automatic,
			Mode:      resolveMode(f, entry),
			Modes:     modesOf(f),
			Provider:  cfg.AIProviderFor(entry).Provider,
			Applied:   IsApplied(f),
		}
		if d, ok := f.(Describer); ok {
			info.Description = d.Description()
		}
		out = append(out, info)
	}
	return out
}
