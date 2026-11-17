package repos

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/config"
)

// Component names of the managed polling schedules. poll_schedules keys
// map onto them in PipelineScheduleSpecsFor.
const (
	slashPollComponent = "schedule:slash-poll"
	eventPollComponent = "schedule:event-poll"
)

// ScheduleSpec defines a fullsend pipeline schedule for GitLab repos.
// All sites that create, detect, or match pipeline schedules must use
// PipelineScheduleSpecs() to stay in sync.
type ScheduleSpec struct {
	// ComponentName is the probe component name (e.g., "schedule:slash-poll").
	ComponentName string
	// Description is the human-readable schedule description stored in GitLab.
	Description string
	// Cron is the cron expression for the schedule.
	Cron string
	// Variables are the CI/CD variables set on the schedule trigger.
	Variables map[string]string
}

// pipelineScheduleSpecs is the canonical list of fullsend pipeline
// schedules for GitLab repos. convergeSchedules, ProbeComponents, and
// setupGitLabPipelineSchedules all reference this slice (via
// PipelineScheduleSpecs()) so that schedule definitions stay in one
// place. Unexported to prevent external mutation; mirrors the pattern
// used by requiredSecrets/requiredVariables in install.go.
var pipelineScheduleSpecs = []ScheduleSpec{
	{
		ComponentName: slashPollComponent,
		Description:   "fullsend slash poll",
		Cron:          "*/5 * * * *",
	},
	{
		ComponentName: eventPollComponent,
		Description:   "fullsend event poll",
		Cron:          "2,17,32,47 * * * *",
	},
}

// jiraPollScheduleSpec is the Fullsend-owned GitLab Jira poll schedule
// (#9108). It is deliberately not in pipelineScheduleSpecs: it exists only
// while the repository's effective tracker.jira.poll.enabled is true, and it
// is converged by gitlabJiraPollReconciler alone. The poll template selects
// the Jira poll mode from this description, and the schedule carries no
// pipeline variables: every Jira setting comes from .fullsend/config.yaml.
// It is offset by one minute from the slash poll.
var jiraPollScheduleSpec = ScheduleSpec{
	ComponentName: "schedule:jira-poll",
	Description:   "fullsend jira poll",
	Cron:          "1-59/5 * * * *",
}

// JiraPollScheduleDescription returns the description of the Fullsend-owned
// GitLab Jira poll schedule. Callers that recreate the managed polling
// schedules (PipelineScheduleSpecs) must leave it alone: repos install
// converges it from tracker.jira.poll.enabled.
func JiraPollScheduleDescription() string {
	return jiraPollScheduleSpec.Description
}

// PipelineScheduleSpecs returns the canonical list of fullsend pipeline
// schedules for GitLab repos, with their compiled-in crons. Sites that
// create schedules or compare a schedule's cron use the repository's
// effective list instead (PipelineScheduleSpecsFor).
func PipelineScheduleSpecs() []ScheduleSpec {
	return pipelineScheduleSpecs
}

// PipelineScheduleSpecsFor returns the managed polling schedules with each
// cron that poll_schedules sets replacing the compiled-in one (#9527). The
// returned slice is a copy; descriptions, component names and variables
// are never changed.
func PipelineScheduleSpecsFor(cfg config.PollSchedulesConfig) []ScheduleSpec {
	specs := make([]ScheduleSpec, len(pipelineScheduleSpecs))
	copy(specs, pipelineScheduleSpecs)
	for i := range specs {
		var cron string
		switch specs[i].ComponentName {
		case slashPollComponent:
			cron = cfg.SlashPoll
		case eventPollComponent:
			cron = cfg.EventPoll
		}
		if cron != "" {
			specs[i].Cron = config.NormalizeCron(cron)
		}
	}
	return specs
}

// effectivePollScheduleSpecs returns a repository's managed polling
// schedules with the crons its effective configuration (the managed
// overlay over the configuration preset) sets in poll_schedules, and the
// subset of them whose cron poll_schedules sets explicitly. Only that
// subset has its existing schedules moved to the configured cron, so a
// cron an operator edited by hand is kept while its key is unset.
func effectivePollScheduleSpecs(resolved ResolvedConfig, preset []byte) (specs, configured []ScheduleSpec, err error) {
	layered, err := config.LayerOnBase(resolved.Managed, preset)
	if err != nil {
		return nil, nil, fmt.Errorf("%s/%s: reading the effective configuration: %w", resolved.Owner, resolved.Repo, err)
	}
	cfg := layered.ConfigPollSchedules()
	if err := config.ValidatePollSchedules(&cfg); err != nil {
		return nil, nil, fmt.Errorf("%s/%s: %w", resolved.Owner, resolved.Repo, err)
	}
	specs = PipelineScheduleSpecsFor(cfg)
	for _, spec := range specs {
		if (spec.ComponentName == slashPollComponent && cfg.SlashPoll != "") ||
			(spec.ComponentName == eventPollComponent && cfg.EventPoll != "") {
			configured = append(configured, spec)
		}
	}
	return specs, configured, nil
}

// specByComponent returns the spec in specs with the given component name,
// or nil if there is none.
func specByComponent(specs []ScheduleSpec, name string) *ScheduleSpec {
	for i := range specs {
		if specs[i].ComponentName == name {
			return &specs[i]
		}
	}
	return nil
}

// scheduleSpecByComponent returns the ScheduleSpec for the given
// component name, or nil if not found.
func scheduleSpecByComponent(name string) *ScheduleSpec {
	for i := range pipelineScheduleSpecs {
		if pipelineScheduleSpecs[i].ComponentName == name {
			return &pipelineScheduleSpecs[i]
		}
	}
	return nil
}
