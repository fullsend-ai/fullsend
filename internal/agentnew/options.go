package agentnew

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/harness"
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
}

// Validate checks every field that reaches a generated file, and does so
// before anything touches disk. Name is checked first and most strictly: it
// is interpolated into a shell script, so it must satisfy the same pattern
// harness.Validate relies on for shell safety.
func (o *Options) Validate() error {
	if !harness.ValidAgentBasename(o.Name) {
		return fmt.Errorf("agent name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", o.Name)
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
