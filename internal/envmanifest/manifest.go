// Package envmanifest defines the sandbox environment-variable contract
// manifest: which variables the runner injects into the sandbox, under what
// conditions, per which runtime, which are delivered by stock harness
// configuration rather than the runner, and which are reserved from harness
// override. Schema: docs/normative/sandbox-env/v1/.
//
// The manifest carries variable names and category metadata only — never
// values or secrets. Serialization is deterministic by contract: sorted
// variable names, stable field order, and a trailing newline, so
// regenerating the committed manifest without a source change produces a
// byte-identical file. The manifest deliberately has no fullsend_version
// field; version identity comes from the tag in the fetch URL, and
// commit-time stamping would break the byte-identical invariant.
package envmanifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// SchemaVersion is the schema version Builder stamps into every manifest and
// the version the committed contract file reports.
const SchemaVersion = 1

// Manifest is the sandbox environment-variable contract. Struct field order
// is the stable serialization order; Render canonicalizes content before
// encoding, so equal content renders byte-identically regardless of how the
// value was assembled.
type Manifest struct {
	SchemaVersion    int                         `json:"schema_version"`
	Exports          Exports                     `json:"exports"`
	HarnessDelivered map[string]HarnessDelivered `json:"harness_delivered"`
	Reserved         []string                    `json:"reserved"`
}

// Exports groups the runner-exported variable categories. Always and
// Conditional are runner-guaranteed modulo their gates; RuntimeSpecific is
// per-runtime and a portability gap, not a guarantee.
type Exports struct {
	Always          []string            `json:"always"`
	Conditional     map[string]string   `json:"conditional"`
	RuntimeSpecific map[string][]string `json:"runtime_specific"`
}

// HarnessDelivered describes a variable delivered by stock harness
// configuration rather than by the runner. RunnerGuaranteed is a label, not
// a toggle: it is always false, because a runner-guaranteed variable belongs
// in Exports instead. Render enforces the label so the serialized contract
// always states that harness-delivered variables are not runner guarantees.
type HarnessDelivered struct {
	Provenance       string `json:"provenance"`
	RunnerGuaranteed bool   `json:"runner_guaranteed"`
}

// Render returns the deterministic JSON encoding of m: sorted variable
// names, stable field order, two-space indent, no HTML escaping, and a
// trailing newline. Rendering the same content twice yields identical
// bytes.
func (m Manifest) Render() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m.normalized()); err != nil {
		return nil, fmt.Errorf("rendering sandbox env manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// Parse unmarshals a manifest from its committed JSON form. It is faithful
// to the input: schema_version and every category are preserved as data so
// consumers can branch on manifests fetched from other tags. Treat fetched
// input as untrusted data — parse it, never evaluate it.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing sandbox env manifest: %w", err)
	}
	return &m, nil
}

// normalized returns a canonical copy of m: name slices sorted and
// deduplicated, nil categories replaced with empty ones so every section
// renders as [] or {} instead of null, and the harness-delivered
// not-runner-guaranteed label enforced. m itself is not modified.
func (m Manifest) normalized() Manifest {
	conditional := m.Exports.Conditional
	if conditional == nil {
		conditional = map[string]string{}
	}
	runtimeSpecific := make(map[string][]string, len(m.Exports.RuntimeSpecific))
	for runtime, names := range m.Exports.RuntimeSpecific {
		runtimeSpecific[runtime] = sortedUnique(names)
	}
	harnessDelivered := make(map[string]HarnessDelivered, len(m.HarnessDelivered))
	for name, delivered := range m.HarnessDelivered {
		delivered.RunnerGuaranteed = false
		harnessDelivered[name] = delivered
	}
	return Manifest{
		SchemaVersion: m.SchemaVersion,
		Exports: Exports{
			Always:          sortedUnique(m.Exports.Always),
			Conditional:     conditional,
			RuntimeSpecific: runtimeSpecific,
		},
		HarnessDelivered: harnessDelivered,
		Reserved:         sortedUnique(m.Reserved),
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
