package pluginkit

import "flag"

// CallType is the value the server passes to a plugin via --call-type,
// describing why the plugin is being run.
type CallType string

const (
	// CallTypeAutomatic is a run the server triggered on its own, when a PR was
	// fetched or its head SHA changed.
	CallTypeAutomatic CallType = "automatic"
	// CallTypeExplicit is a run of a deferred (OnlyOnDemand) plugin, which only
	// ever executes because someone asked for it by name.
	CallTypeExplicit CallType = "explicit"
	// CallTypeRerun is a requested rerun of a plugin that would otherwise have
	// run automatically.
	CallTypeRerun CallType = "rerun"
)

// ParseCallType turns the raw --call-type value into a CallType, falling back
// to CallTypeAutomatic for an empty or unrecognised value so a plugin built
// against an older or newer server still behaves sensibly.
func ParseCallType(s string) CallType {
	switch CallType(s) {
	case CallTypeExplicit:
		return CallTypeExplicit
	case CallTypeRerun:
		return CallTypeRerun
	default:
		return CallTypeAutomatic
	}
}

// Requested reports whether the run was asked for by a person, rather than
// triggered by the server. Plugins that cache or throttle expensive work
// should treat a requested run as a reason to redo it.
func (c CallType) Requested() bool {
	return c == CallTypeExplicit || c == CallTypeRerun
}

// RegisterCallTypeFlag declares --call-type on the default flag set and returns
// a resolver to call after flag.Parse. Every plugin has to accept the flag,
// since the server always passes it, so plugins that don't act on the value can
// call this and discard the resolver.
func RegisterCallTypeFlag() func() CallType {
	raw := flag.String("call-type", string(CallTypeAutomatic), "why this run was triggered: automatic, explicit or rerun")
	return func() CallType { return ParseCallType(*raw) }
}
