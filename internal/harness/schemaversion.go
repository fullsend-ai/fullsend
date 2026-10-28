package harness

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// MaxSupportedSchemaVersion is the highest harness schema_version this build
// interprets (ADR 0127 Decision 1). Version 1 is the implicit contract of
// every harness that predates the field; a harness declaring a higher version
// is rejected at load time with an upgrade remedy rather than misinterpreted
// under version-1 semantics.
const MaxSupportedSchemaVersion = 1

// schemaVersionValue is the declared schema_version integer carried by
// Harness.SchemaVersion. It is a named type solely to carry UnmarshalYAML:
// yaml.v3 silently truncates fractional floats when decoding into integer
// fields (schema_version: 1.5 would decode as 1), which would misinterpret a
// malformed version instead of rejecting it during parsing (ADR 0127
// Decision 1). The unmarshaler closes that gap so every non-integer value
// fails at parse with the offending line identified, matching the error
// shape yaml.v3 itself produces for strings and mappings. A nil pointer
// (absent) never reaches the unmarshaler — yaml.v3 maps an explicit null to
// a nil pointer, the package's null-equals-unset convention.
type schemaVersionValue int

// UnmarshalYAML implements yaml.Unmarshaler for schemaVersionValue.
func (v *schemaVersionValue) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!float" {
		// Reproduce yaml.v3's own type-error shape, including its long-value
		// truncation, so every malformed-version failure reads alike.
		value := node.Value
		if len(value) > 10 {
			value = value[:7] + "..."
		}
		return &yaml.TypeError{Errors: []string{
			fmt.Sprintf("line %d: cannot unmarshal !!float `%s` into int", node.Line, value),
		}}
	}
	var iv int
	if err := node.Decode(&iv); err != nil {
		// Native "cannot unmarshal ..." error naming the offending line.
		return err
	}
	*v = schemaVersionValue(iv)
	return nil
}

// EffectiveSchemaVersion returns the harness's effective schema version: the
// declared schema_version, or 1 when the field is absent.
// Absent is the correct spelling of version 1, so every harness written before
// the field existed reports 1.
func (h *Harness) EffectiveSchemaVersion() int {
	if h.SchemaVersion == nil {
		return 1
	}
	return int(*h.SchemaVersion)
}

// validateSchemaVersion rejects an explicitly declared schema_version that is
// zero, negative, or unsupported. A nil pointer (absent) is version 1 and
// valid. Non-integer values never reach this check:
// schemaVersionValue.UnmarshalYAML fails them during unmarshaling with the
// offending line identified.
func validateSchemaVersion(v *schemaVersionValue) error {
	if v == nil {
		return nil
	}
	if *v < 1 {
		return fmt.Errorf("schema_version must be a positive integer, got %d", *v)
	}
	if *v > MaxSupportedSchemaVersion {
		return fmt.Errorf("schema_version %d is not supported by this version of fullsend (maximum supported schema_version: %d); upgrade fullsend to a release that supports it, or pin harness content declaring a supported version", *v, MaxSupportedSchemaVersion)
	}
	return nil
}

// schemaVersionsCompose reports whether a base layer declaring baseVer can
// compose into a layer declaring childVer. Equal versions always compose;
// no cross-version rule exists yet, so mixed-version layers are rejected.
func schemaVersionsCompose(childVer, baseVer int) bool {
	return childVer == baseVer
}

// checkSchemaVersionsCompose rejects composing child with a base layer
// whose effective schema version does not compose with the child's.
// layerLabel names the base reference (path or URL) in the error so the
// harness author can locate the offending layer.
func checkSchemaVersionsCompose(child, base *Harness, layerLabel string) error {
	childVer := child.EffectiveSchemaVersion()
	baseVer := base.EffectiveSchemaVersion()
	if schemaVersionsCompose(childVer, baseVer) {
		return nil
	}
	return fmt.Errorf("base layer %q declares schema_version %d, which is incompatible with schema_version %d of the harness composing it; mixed-version base composition is not supported", layerLabel, baseVer, childVer)
}
