package agentnew

import (
	"fmt"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
	agentruntime "github.com/fullsend-ai/fullsend/internal/runtime"
)

// Default values for the generated harness.
const (
	DefaultModel          = "opus"
	DefaultEffort         = "high"
	DefaultTimeoutMinutes = 15
	// DefaultOn is the trigger preset used when neither --on nor --trigger
	// is given. A trigger is mandatory: ListTriggeredHarnesses skips a
	// trigger-less harness with a bare `continue` and no annotation, so such
	// an agent registers, validates, lists, and then never fires.
	DefaultOn = PresetCommand
)

// Options is the fully-resolved input to Render, after defaults, spec file
// and command-line flags have been merged. Every field is already validated:
// Render assumes it can use them without further checking.
type Options struct {
	Name           string
	Role           string
	Description    string
	Trigger        string
	Model          string
	Effort         string
	Slug           string
	Image          string
	TimeoutMinutes int
	ValidationLoop bool
	// Runtime is the already-validated, already-resolved runtime the agent
	// will dispatch under (claude, pi, or codex): the --runtime flag or spec
	// value, or — when neither is given — the caller's resolved repo-wide
	// config.yaml default. Callers must resolve that default themselves
	// before constructing Options; leaving this empty when a --runtime flag
	// was not given would make UsesVertex assume claude even in a repo
	// configured to dispatch as codex or pi by default. It is not written
	// into the harness YAML — config.yaml holds the per-agent override, if
	// any — but it decides Vertex host_files/env and whether model: must be
	// an OpenAI id (#7264).
	Runtime string
}

// Validate checks every field that reaches a generated file, and does so
// before anything touches disk. Name is checked first and most strictly: it
// is interpolated into a shell script, so it must satisfy the same pattern
// harness.Validate relies on for shell safety.
func (o *Options) Validate() error {
	// Both rules apply. harness.ValidAgentBasename is the shell-safety check
	// the harness loader relies on; config.ValidConfigAgentName is what
	// registration will demand later. Checking only the first would let a
	// name like "_lint" generate every file and then fail at registration,
	// leaving the directory changed by a run that failed.
	if !harness.ValidAgentBasename(o.Name) || !config.ValidConfigAgentName(o.Name) {
		return fmt.Errorf("agent name %q is not valid: it must start with a letter or digit and contain only letters, digits, underscores and hyphens", o.Name)
	}
	if _, err := LookupRole(o.Role); err != nil {
		return err
	}
	if o.Trigger == "" {
		return fmt.Errorf("a trigger is required: pass --on with a preset, or --trigger with a CEL expression.\n" +
			"An agent with no trigger registers and validates but is silently never dispatched")
	}
	if err := harness.ValidateTriggerExpression(o.Trigger); err != nil {
		return fmt.Errorf("trigger does not compile: %w", err)
	}
	if o.Model != "" && !config.ValidModelRef(o.Model) {
		return fmt.Errorf("model %q contains invalid characters", o.Model)
	}
	// fullsend pins codex to OpenAI; the default opus alias is one of the
	// values it rejects. Fail here so generation cannot emit a harness the
	// runtime will refuse.
	if o.Runtime == "codex" {
		if err := agentruntime.ValidateCodexModel(o.Model); err != nil {
			return err
		}
	}
	if o.Effort != "" && !config.ValidEffort(o.Effort) {
		return fmt.Errorf("effort %q is not valid (allowed: %v)", o.Effort, config.ValidEffortLevels())
	}
	if o.Slug != "" && !harness.ValidSlug(o.Slug) {
		return fmt.Errorf("slug %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -; must start with a letter or digit)", o.Slug)
	}
	if o.TimeoutMinutes < 0 {
		return fmt.Errorf("timeout_minutes must be non-negative, got %d", o.TimeoutMinutes)
	}
	if o.Image == "" {
		return fmt.Errorf("image must not be empty")
	}
	return nil
}

// UsesVertex reports whether the generated harness should carry the GCP
// credential host_files and the Vertex sandbox env. fullsend pins codex to
// OpenAI, so those fields would only fail the run before it starts (#7264).
// pi is multi-provider, so its answer also depends on the model: --runtime
// pi with an OpenAI model calls OpenAI, not Vertex, and would otherwise be
// stranded needing GOOGLE_APPLICATION_CREDENTIALS it will never use for the
// same reason as #7264.
//
// This is deliberately not agentruntime.NeedsOpenAIProvider: that resolves a
// bare pi model id through translatePiModel, which reads the ambient
// FULLSEND_PI_PROVIDER environment variable of the *run*. Harness generation
// happens in a different process (and often a different machine, e.g. CI)
// than the run it generates for, so branching on that variable here would
// make the generated harness depend on whatever happened to be set in the
// generator's environment rather than on Options alone — e.g. a developer
// with FULLSEND_PI_PROVIDER=openai set would get a harness with Vertex
// host_files/env omitted even though the run (with that variable unset, as
// in CI) still needs them. UsesVertex only trusts an explicit "openai/"
// prefix on the model Options itself carries.
func (o Options) UsesVertex() bool {
	switch o.Runtime {
	case "codex":
		return false
	case "pi":
		return !hasOpenAIPrefix(o.Model)
	default:
		return true
	}
}

// hasOpenAIPrefix reports whether model carries an explicit "openai/"
// provider prefix. Matching is case-insensitive because pi resolves
// provider prefixes case-insensitively (see translatePiModel).
func hasOpenAIPrefix(model string) bool {
	prefix, _, ok := strings.Cut(model, "/")
	return ok && strings.EqualFold(prefix, "openai")
}
