// Package envmanifest models the sandbox environment-variable contract:
// which variables the runner injects into the sandbox, under what
// conditions, per which runtime, which are delivered by stock harness
// configuration rather than the runner, which are reserved from harness
// override, and which are deprecated. RenderSchemas publishes the model as
// the JSON Schemas under docs/normative/sandbox-env/v1/.
//
// The model carries variable names and category metadata only — never
// values or secrets. Rendering is deterministic by contract: sorted names,
// fixed key order, and a trailing newline, so regenerating the committed
// schemas without a source change produces byte-identical files. Version
// identity comes from the v1 path and the release tag in the fetch URL; the
// schemas carry no fullsend version, which commit-time stamping would break.
package envmanifest

import "slices"

// Manifest is the sandbox environment-variable contract model.
type Manifest struct {
	Exports          Exports
	HarnessDelivered map[string]HarnessDelivered
	Reserved         []string
	Deprecated       []string
}

// Exports groups the runner-exported variable categories. Always and
// Conditional are runner-guaranteed modulo their gates; RuntimeSpecific is
// per-runtime and a portability gap, not a guarantee.
type Exports struct {
	Always          []string
	Conditional     map[string]string
	RuntimeSpecific map[string][]string
}

// HarnessDelivered describes a variable delivered by stock harness
// configuration rather than by the runner. Such a variable is never a
// runner guarantee: one the runner guarantees belongs in Exports instead.
type HarnessDelivered struct {
	Provenance string
}

// normalized returns a canonical copy of m: name slices sorted and
// deduplicated, nil categories replaced with empty ones so every section
// is non-nil. m itself is not modified.
func (m Manifest) normalized() Manifest {
	conditional := make(map[string]string, len(m.Exports.Conditional))
	for k, v := range m.Exports.Conditional {
		conditional[k] = v
	}
	runtimeSpecific := make(map[string][]string, len(m.Exports.RuntimeSpecific))
	for runtime, names := range m.Exports.RuntimeSpecific {
		runtimeSpecific[runtime] = sortedUnique(names)
	}
	harnessDelivered := make(map[string]HarnessDelivered, len(m.HarnessDelivered))
	for name, delivered := range m.HarnessDelivered {
		harnessDelivered[name] = delivered
	}
	return Manifest{
		Exports: Exports{
			Always:          sortedUnique(m.Exports.Always),
			Conditional:     conditional,
			RuntimeSpecific: runtimeSpecific,
		},
		HarnessDelivered: harnessDelivered,
		Reserved:         sortedUnique(m.Reserved),
		Deprecated:       sortedUnique(m.Deprecated),
	}
}

// sortedUnique returns names deduplicated and sorted, never nil.
func sortedUnique(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	unique := make([]string, 0, len(names))
	for _, name := range names {
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	slices.Sort(unique)
	return unique
}
