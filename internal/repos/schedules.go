package repos

import "github.com/fullsend-ai/fullsend/internal/forge"

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
		ComponentName: "schedule:slash-poll",
		Description:   "fullsend slash poll",
		Cron:          "*/5 * * * *",
		Variables:     map[string]string{forge.VarPollMode: "slash"},
	},
	{
		ComponentName: "schedule:event-poll",
		Description:   "fullsend event poll",
		Cron:          "2,17,32,47 * * * *",
		Variables:     map[string]string{forge.VarPollMode: "events"},
	},
}

// PipelineScheduleSpecs returns the canonical list of fullsend pipeline
// schedules for GitLab repos.
func PipelineScheduleSpecs() []ScheduleSpec {
	return pipelineScheduleSpecs
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
