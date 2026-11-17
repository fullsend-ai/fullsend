package config

import "fmt"

// Defaults and bounds of the fork_pr_review_poll block (ADR 0171).
const (
	DefaultForkPRPollMaxStartsPerCycle  = 5
	DefaultForkPRPollMaxAttemptsPerHead = 2

	minForkPRPollMaxStartsPerCycle  = 1
	maxForkPRPollMaxStartsPerCycle  = 20
	minForkPRPollMaxAttemptsPerHead = 1
	maxForkPRPollMaxAttemptsPerHead = 5
)

// ForkPRReviewPollConfig is the fork_pr_review_poll block (ADR 0171): an
// opt-in scheduled poll that starts the built-in review for fork pull
// requests from authorized authors. Each field falls through to the parent
// layer when unset, so config.base.yaml can supply a default that a
// repository overrides key by key.
type ForkPRReviewPollConfig struct {
	// Enabled turns the poll on. Unset is off.
	Enabled *bool `yaml:"enabled,omitempty"`
	// MaxStartsPerCycle caps the reviews one poll cycle starts (1-20,
	// default 5).
	MaxStartsPerCycle *int `yaml:"max_starts_per_cycle,omitempty"`
	// MaxAttemptsPerHead caps the failed review attempts for one head
	// commit before the poll stops starting it (1-5, default 2).
	MaxAttemptsPerHead *int `yaml:"max_attempts_per_head,omitempty"`
}

// PollEnabled reports whether enabled is set to true. A nil block is off.
func (c *ForkPRReviewPollConfig) PollEnabled() bool {
	return c != nil && c.Enabled != nil && *c.Enabled
}

// EffectiveMaxStartsPerCycle returns max_starts_per_cycle or its default.
func (c *ForkPRReviewPollConfig) EffectiveMaxStartsPerCycle() int {
	if c == nil || c.MaxStartsPerCycle == nil {
		return DefaultForkPRPollMaxStartsPerCycle
	}
	return *c.MaxStartsPerCycle
}

// EffectiveMaxAttemptsPerHead returns max_attempts_per_head or its default.
func (c *ForkPRReviewPollConfig) EffectiveMaxAttemptsPerHead() int {
	if c == nil || c.MaxAttemptsPerHead == nil {
		return DefaultForkPRPollMaxAttemptsPerHead
	}
	return *c.MaxAttemptsPerHead
}

// ValidateForkPRReviewPoll checks the bounds of a fork_pr_review_poll
// block; nil is valid.
func ValidateForkPRReviewPoll(c *ForkPRReviewPollConfig) error {
	if c == nil {
		return nil
	}
	if v := c.MaxStartsPerCycle; v != nil && (*v < minForkPRPollMaxStartsPerCycle || *v > maxForkPRPollMaxStartsPerCycle) {
		return fmt.Errorf("fork_pr_review_poll.max_starts_per_cycle must be between %d and %d (got %d)",
			minForkPRPollMaxStartsPerCycle, maxForkPRPollMaxStartsPerCycle, *v)
	}
	if v := c.MaxAttemptsPerHead; v != nil && (*v < minForkPRPollMaxAttemptsPerHead || *v > maxForkPRPollMaxAttemptsPerHead) {
		return fmt.Errorf("fork_pr_review_poll.max_attempts_per_head must be between %d and %d (got %d)",
			minForkPRPollMaxAttemptsPerHead, maxForkPRPollMaxAttemptsPerHead, *v)
	}
	return nil
}

// cloneForkPRReviewPoll returns a deep copy of c, or nil.
func cloneForkPRReviewPoll(c *ForkPRReviewPollConfig) *ForkPRReviewPollConfig {
	if c == nil {
		return nil
	}
	out := &ForkPRReviewPollConfig{}
	if c.Enabled != nil {
		v := *c.Enabled
		out.Enabled = &v
	}
	if c.MaxStartsPerCycle != nil {
		v := *c.MaxStartsPerCycle
		out.MaxStartsPerCycle = &v
	}
	if c.MaxAttemptsPerHead != nil {
		v := *c.MaxAttemptsPerHead
		out.MaxAttemptsPerHead = &v
	}
	return out
}

// mergeForkPRReviewPoll returns parent with every field child sets
// replaced by child's value. Either may be nil.
func mergeForkPRReviewPoll(parent, child *ForkPRReviewPollConfig) *ForkPRReviewPollConfig {
	if child == nil {
		return cloneForkPRReviewPoll(parent)
	}
	out := cloneForkPRReviewPoll(parent)
	if out == nil {
		return cloneForkPRReviewPoll(child)
	}
	c := cloneForkPRReviewPoll(child)
	if c.Enabled != nil {
		out.Enabled = c.Enabled
	}
	if c.MaxStartsPerCycle != nil {
		out.MaxStartsPerCycle = c.MaxStartsPerCycle
	}
	if c.MaxAttemptsPerHead != nil {
		out.MaxAttemptsPerHead = c.MaxAttemptsPerHead
	}
	return out
}

// ForkPRReviewPoll returns the effective fork_pr_review_poll block: this
// layer's fields over the parent chain's, key by key. nil when no layer
// sets the block.
func (c *perRepoConfig) ForkPRReviewPoll() *ForkPRReviewPollConfig {
	var parent *ForkPRReviewPollConfig
	if c.parent != nil {
		parent = c.parent.ForkPRReviewPoll()
	}
	return mergeForkPRReviewPoll(parent, c.ForkPRReviewPollBlock)
}

// ForkPRReviewPoll returns nil — the poll is off by default.
func (d *perRepoDefaults) ForkPRReviewPoll() *ForkPRReviewPollConfig { return nil }
