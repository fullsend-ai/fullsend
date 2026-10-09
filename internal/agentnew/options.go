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
	// Runtime is the runtime the agent will dispatch under: --runtime, the
	// spec's runtime:, or else the repo's config.yaml default, resolved by
	// the caller. It is not written to the harness, but decides which
	// credentials the harness asks for and whether the model must be an
	// OpenAI id.
	Runtime string
	// Workflow is the harness workflow: field (--workflow-source and
	// --workflow, ADR 0130), or nil.
	Workflow *harness.WorkflowSpec
}

// PlaceholderTreeHash is the #sha256= value ParseWorkflow writes for a
// remote source given without one: 64 zeros, which no tree hashes to, so
// `fullsend lock` fails and prints the tree hash the commit has.
var PlaceholderTreeHash = strings.Repeat("0", 64)

// ParseWorkflow reads --workflow-source and --workflow (the workflow
// name) into the harness workflow: field. A remote source without a
// #sha256= pin gets PlaceholderTreeHash, which the caller explains. The
// result is checked like the field itself; which runtime it suits is
// Options.Validate's job.
func ParseWorkflow(source, name string) (*harness.WorkflowSpec, error) {
	if source == "" {
		if name != "" {
			return nil, fmt.Errorf("--workflow %s needs --workflow-source (spec key workflow_source), the workflow-definition repository: a GitHub tree URL at a commit sha or a path in this repository", name)
		}
		return nil, nil
	}
	if harness.IsURL(source) && !strings.Contains(source, "#") {
		source += "#sha256=" + PlaceholderTreeHash
	}
	spec := &harness.WorkflowSpec{Source: source, Name: name}
	if err := harness.ValidateWorkflowSpec(spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// HasPlaceholderPin reports whether the workflow source carries
// PlaceholderTreeHash, so the user still has to pin the real tree hash.
func (o Options) HasPlaceholderPin() bool {
	return o.Workflow != nil && strings.HasSuffix(o.Workflow.Source, "#sha256="+PlaceholderTreeHash)
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
	// Refused here rather than by the runtime after the sandbox is up. The
	// example id is the one docs/runtimes/codex.md uses.
	if o.Runtime == "codex" {
		if err := agentruntime.ValidateCodexModel(o.Model); err != nil {
			return fmt.Errorf("runtime codex takes OpenAI model ids only, and %s: "+
				"use --model openai/gpt-5.6-luna (or another openai/<id>) on the command, "+
				"or model: openai/gpt-5.6-luna in the spec file", describeModel(o.Model))
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
	if o.Workflow != nil {
		if err := harness.ValidateWorkflowSpec(o.Workflow); err != nil {
			return err
		}
		// ADR 0130 rule 3: claude starts a Claude plugin's workflow by
		// name, pi loads a pi extension that takes no name, and fullsend
		// run refuses workflow: on any other runtime, so the generator
		// does too.
		switch o.Runtime {
		case "", "claude":
			if o.Workflow.Name == "" {
				return fmt.Errorf("--workflow-source needs --workflow <name> (spec key workflow) on the claude runtime: the workflows/<name>.js the runner starts; for a pi extension definition pass --runtime pi")
			}
		case "pi":
			if o.Workflow.Name != "" {
				return fmt.Errorf("--workflow %s is set, but on the pi runtime the definition is a pi extension, which takes no workflow name; drop --workflow and keep --workflow-source, or pass --runtime claude", o.Workflow.Name)
			}
		default:
			return fmt.Errorf("a workflow definition runs on claude or pi, and this agent resolves to %s: pass --runtime claude or --runtime pi, or drop --workflow-source", o.Runtime)
		}
	}
	return nil
}

// UsesVertex reports whether the agent calls Vertex, and so whether its
// harness carries the Vertex provider, GCP credentials and Vertex env. codex
// never does; pi does unless its model has an explicit openai/ prefix.
//
// Not agentruntime.NeedsOpenAIProvider: that reads FULLSEND_PI_PROVIDER from
// the environment of the run, and the generator runs elsewhere, so the
// harness would depend on the generating machine.
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

// describeModel names what was wrong with a model codex refused.
func describeModel(model string) string {
	if model == "" {
		return "no model was named"
	}
	return fmt.Sprintf("%q is not one", model)
}
