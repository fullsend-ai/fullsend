package config

import (
	"fmt"
	"regexp"
	"strings"
)

// PollSchedulesConfig is the poll_schedules: block of config.yaml (#9527).
// It sets the cron expressions of the GitLab pipeline schedules that run
// fullsend's polling jobs. Each schedule's run spends CI compute minutes, so
// a namespace with a small quota (GitLab.com Free) can poll less often.
//
// An empty field inherits the parent layer; with no layer setting it, the
// schedule keeps its compiled-in cron. `fullsend repos install` reads the
// block and applies it to the managed schedules; the agent runtime never
// reads it, and GitHub repositories ignore it.
type PollSchedulesConfig struct {
	// SlashPoll is the cron of the "fullsend slash poll" schedule, which
	// picks up slash commands.
	SlashPoll string `yaml:"slash_poll,omitempty"`
	// EventPoll is the cron of the "fullsend event poll" schedule, which
	// discovers issue and merge request events.
	EventPoll string `yaml:"event_poll,omitempty"`
}

// IsZero reports whether the block sets nothing.
func (p *PollSchedulesConfig) IsZero() bool {
	return p == nil || (p.SlashPoll == "" && p.EventPoll == "")
}

// cronFieldPattern matches one field of a five-field cron expression:
// numbers, names (MON, JAN), and the *, ?, ",", "-" and "/" operators.
var cronFieldPattern = regexp.MustCompile(`^[0-9A-Za-z*?,/-]+$`)

// ValidatePollSchedules checks that every cron set in the block is a
// five-field cron expression. GitLab validates the expression's values when
// the schedule is created; this rejects malformed input before any forge
// write.
func ValidatePollSchedules(p *PollSchedulesConfig) error {
	if p == nil {
		return nil
	}
	for _, f := range []struct{ key, cron string }{
		{"poll_schedules.slash_poll", p.SlashPoll},
		{"poll_schedules.event_poll", p.EventPoll},
	} {
		if f.cron == "" {
			continue
		}
		fields := strings.Fields(f.cron)
		if len(fields) != 5 {
			return fmt.Errorf("invalid %s %q: must be a cron expression with five fields (minute hour day-of-month month day-of-week)", f.key, f.cron)
		}
		for _, field := range fields {
			if !cronFieldPattern.MatchString(field) {
				return fmt.Errorf("invalid %s %q: field %q is not a cron field", f.key, f.cron, field)
			}
		}
	}
	return nil
}

// NormalizeCron returns cron with its fields separated by single spaces, the
// form fullsend creates schedules with and compares them against.
func NormalizeCron(cron string) string {
	return strings.Join(strings.Fields(cron), " ")
}

// ConfigPollSchedules returns the effective poll_schedules block, merged per
// key through the parent chain: a locally set cron overrides the parent's,
// and an unset one inherits it. A field left empty means the compiled-in
// cron.
func (c *perRepoConfig) ConfigPollSchedules() PollSchedulesConfig {
	var out PollSchedulesConfig
	if c.parent != nil {
		out = c.parent.ConfigPollSchedules()
	}
	if c.PollSchedules != nil {
		if c.PollSchedules.SlashPoll != "" {
			out.SlashPoll = c.PollSchedules.SlashPoll
		}
		if c.PollSchedules.EventPoll != "" {
			out.EventPoll = c.PollSchedules.EventPoll
		}
	}
	return out
}

// clonePollSchedules copies a poll_schedules block.
func clonePollSchedules(src *PollSchedulesConfig) *PollSchedulesConfig {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}

// mergePollSchedules layers child over parent per key.
func mergePollSchedules(parent, child *PollSchedulesConfig) *PollSchedulesConfig {
	out := PollSchedulesConfig{}
	if parent != nil {
		out = *parent
	}
	if child.SlashPoll != "" {
		out.SlashPoll = child.SlashPoll
	}
	if child.EventPoll != "" {
		out.EventPoll = child.EventPoll
	}
	return &out
}
