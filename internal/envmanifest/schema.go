package envmanifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// SchemaBaseURI is the $id prefix of the published sandbox env schemas,
// matching the other normative schemas' https://fullsend.ai/normative/
// identifiers. The schemas are fetched by tag URL, not from this host;
// the $id only names them.
const SchemaBaseURI = "https://fullsend.ai/normative/sandbox-env/v1/"

// The published schema files, under docs/normative/sandbox-env/v1/.
const (
	// HarnessEnvSchemaFile validates a harness YAML's env blocks: no
	// env.sandbox key may be a name the runner reserves.
	HarnessEnvSchemaFile = "harness-env.schema.json"
	// EnvReferencesSchemaFile validates a reachability claim — "code running
	// under this runtime reads these variables" — and carries the full
	// contract inventory in its $defs.
	EnvReferencesSchemaFile = "env-references.schema.json"
)

// PortableRuntime is the env-references runtime value for code that must
// work under every runtime: it may rely only on the shared categories.
const PortableRuntime = "portable"

// envNamePattern is the POSIX shell variable-name grammar, as a JSON Schema
// pattern.
const envNamePattern = "^[A-Za-z_][A-Za-z0-9_]*$"

// RenderSchemas returns the published JSON Schemas generated from m, keyed
// by file name. Each schema is self-contained — its $defs carry every
// category it needs — so a consumer validates against one fetched file with
// no cross-file $ref resolution. Output is deterministic: keys appear in a
// fixed order, names are sorted, and each file ends in a newline.
func (m Manifest) RenderSchemas() (map[string][]byte, error) {
	n := m.normalized()
	out := map[string][]byte{}
	for file, schema := range map[string]object{
		HarnessEnvSchemaFile:    n.harnessEnvSchema(),
		EnvReferencesSchemaFile: n.envReferencesSchema(),
	} {
		data, err := encodeSchema(schema)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", file, err)
		}
		out[file] = data
	}
	return out, nil
}

// Runtimes returns the runtimes the manifest lists, sorted.
func (m Manifest) Runtimes() []string {
	runtimes := make([]string, 0, len(m.Exports.RuntimeSpecific))
	for name := range m.Exports.RuntimeSpecific {
		runtimes = append(runtimes, name)
	}
	slices.Sort(runtimes)
	return runtimes
}

func (m Manifest) harnessEnvSchema() object {
	return object{
		{"$schema", "https://json-schema.org/draft/2020-12/schema"},
		{"$id", SchemaBaseURI + HarnessEnvSchemaFile},
		{"title", "Harness sandbox env contract"},
		{"description", "Validates the env blocks of a fullsend harness YAML (top-level env and forge.<name>.env) against the sandbox environment-variable contract: an env.sandbox key must be a shell variable name and must not be a name the runner reserves, because the runner drops reserved keys instead of exporting them. Other harness fields are not constrained here. Generated from the runner's code (ADR 0146); see README.md."},
		{"type", "object"},
		{"properties", object{
			{"env", ref("#/$defs/env_block")},
			{"forge", object{
				{"type", "object"},
				{"additionalProperties", object{
					{"type", "object"},
					{"properties", object{{"env", ref("#/$defs/env_block")}}},
				}},
			}},
		}},
		{"$defs", object{
			{"env_block", object{
				{"type", "object"},
				{"properties", object{{"sandbox", ref("#/$defs/sandbox_env")}}},
			}},
			{"sandbox_env", object{
				{"description", "Variables a harness delivers into the sandbox. The runner owns the reserved names and refuses them here."},
				{"type", "object"},
				{"propertyNames", object{{"allOf", []any{
					ref("#/$defs/env_name"),
					object{{"not", ref("#/$defs/reserved")}},
				}}}},
			}},
			{"env_name", envNameSchema()},
			{"reserved", m.reservedSchema()},
		}},
	}
}

func (m Manifest) envReferencesSchema() object {
	runtimes := m.Runtimes()
	runtimeEnum := append([]string{PortableRuntime}, runtimes...)
	shared := []any{ref("#/$defs/always"), ref("#/$defs/conditional"), ref("#/$defs/harness_delivered")}

	var perRuntime []any
	perRuntime = append(perRuntime, reachableWhen(PortableRuntime, shared))
	for _, rt := range runtimes {
		perRuntime = append(perRuntime, reachableWhen(rt, append(slices.Clone(shared), ref("#/$defs/runtime_"+rt))))
	}

	defs := object{
		{"env_name", envNameSchema()},
		{"runtime", object{
			{"description", "The runtime the referencing code runs under, or \"" + PortableRuntime + "\" for code that must work under every runtime."},
			{"enum", runtimeEnum},
		}},
		{"always", object{
			{"description", "Exported by the runner into every sandbox it builds."},
			{"enum", m.Exports.Always},
		}},
		{"conditional", object{
			{"description", "Exported by the runner only when the gate in each entry's description holds."},
			{"oneOf", m.conditionalItems()},
		}},
	}
	for _, rt := range runtimes {
		defs = append(defs, member{"runtime_" + rt, nameSet(
			"Exported only when dispatch runs under "+rt+": its EnvExports plus the environment its launch command sets. Not reachable under other runtimes.",
			m.Exports.RuntimeSpecific[rt])})
	}
	defs = append(defs,
		member{"harness_delivered", object{
			{"description", "Delivered by stock harness configuration, not by the runner: reachable only when the harness delivers it."},
			{"oneOf", m.harnessDeliveredItems()},
		}},
		member{"reserved", m.reservedSchema()},
		member{"deprecated", nameSet("Deprecated and scheduled for removal in a future contract version; still reachable through its export category until then. Consumers should warn on a match and migrate.", m.Deprecated)},
	)

	return object{
		{"$schema", "https://json-schema.org/draft/2020-12/schema"},
		{"$id", SchemaBaseURI + EnvReferencesSchemaFile},
		{"title", "Sandbox env references"},
		{"description", "Validates that code running in a fullsend sandbox — a skill script, an agent prompt's tool calls, a sandbox hook — reads only environment variables the contract makes reachable under its runtime. List the variables the code reads, minus those the harness's own env.sandbox declares. The $defs are the full contract inventory. Generated from the runner's code (ADR 0146); see README.md."},
		{"type", "object"},
		{"additionalProperties", false},
		{"required", []string{"runtime", "variables"}},
		{"properties", object{
			{"runtime", ref("#/$defs/runtime")},
			{"variables", object{
				{"type", "array"},
				{"uniqueItems", true},
				{"items", ref("#/$defs/env_name")},
			}},
		}},
		{"allOf", perRuntime},
		{"$defs", defs},
	}
}

// reachableWhen restricts variables to the union of reachable categories
// when runtime equals rt.
func reachableWhen(rt string, reachable []any) object {
	return object{
		{"if", object{
			{"properties", object{{"runtime", object{{"const", rt}}}}},
			{"required", []string{"runtime"}},
		}},
		{"then", object{
			{"properties", object{{"variables", object{{"items", object{{"anyOf", reachable}}}}}}},
		}},
	}
}

func (m Manifest) reservedSchema() object {
	return nameSet("Reserved by the runner: a harness cannot set these through env.sandbox.", m.Reserved)
}

func (m Manifest) conditionalItems() []any {
	names := make([]string, 0, len(m.Exports.Conditional))
	for name := range m.Exports.Conditional {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]any, 0, len(names))
	for _, name := range names {
		items = append(items, object{{"const", name}, {"description", m.Exports.Conditional[name]}})
	}
	return orNothing(items)
}

func (m Manifest) harnessDeliveredItems() []any {
	names := make([]string, 0, len(m.HarnessDelivered))
	for name := range m.HarnessDelivered {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]any, 0, len(names))
	for _, name := range names {
		items = append(items, object{{"const", name}, {"description", m.HarnessDelivered[name].Provenance}})
	}
	return orNothing(items)
}

// orNothing keeps a oneOf valid when a category is empty: oneOf must list
// at least one schema, and false matches no name.
func orNothing(items []any) []any {
	if len(items) == 0 {
		return []any{false}
	}
	return items
}

// nameSet is a category as an enum of names; an empty category is a schema
// matching no name, since an enum must list at least one value.
func nameSet(description string, names []string) object {
	if len(names) == 0 {
		return object{{"description", description}, {"not", object{}}}
	}
	return object{{"description", description}, {"enum", names}}
}

func envNameSchema() object {
	return object{{"type", "string"}, {"pattern", envNamePattern}}
}

func ref(target string) object {
	return object{{"$ref", target}}
}

// object is a JSON object whose keys encode in declaration order, so the
// schemas read $schema, $id, title first and stay byte-stable.
type object []member

type member struct {
	key   string
	value any
}

func (o object) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(m.key)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		value, err := marshalNoEscape(m.value)
		if err != nil {
			return nil, err
		}
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// encodeSchema renders a schema with two-space indentation and a trailing
// newline.
func encodeSchema(schema object) ([]byte, error) {
	compact, err := marshalNoEscape(schema)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
