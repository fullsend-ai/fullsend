package repos

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
	},
	{
		ComponentName: "schedule:event-poll",
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
