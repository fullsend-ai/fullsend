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
	},
	{
		ComponentName: "schedule:event-poll",
		Description:   "fullsend event poll",
		Cron:          "2,17,32,47 * * * *",
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

// legacyPollModeVariables are the FULLSEND_POLL_MODE pipeline variables
// required by the pre-typed-dispatch GitLab templates, keyed by
// ComponentName. Those templates select poll mode from this variable and
// do not derive it from the schedule description, so creating or
// repairing a schedule for a repo that is not yet on the typed
// pipeline-input contract must still set it. Typed installations select
// poll mode from the schedule description instead (see
// GitLabUsesTypedDispatch) and must use variable-free schedules so the
// pipeline-variable override restriction can be enforced.
var legacyPollModeVariables = map[string]map[string]string{
	"schedule:slash-poll": {forge.VarPollMode: "slash"},
	"schedule:event-poll": {forge.VarPollMode: "events"},
}

// ScheduleVariablesFor returns the pipeline variables to submit when
// creating the given schedule spec. When typed reports that the repo's
// effective GitLab wrapper is not yet on the typed pipeline-input
// contract, it returns the legacy FULLSEND_POLL_MODE override so slash
// and event polling keep working; otherwise it returns the spec's
// (variable-free) defaults. Exported so every schedule-creation call
// site — convergeSchedules and the CLI's post-install fresh-install
// path (setupGitLabPipelineSchedules) — selects variables from the same
// transport-aware logic instead of risking drift between two copies.
func ScheduleVariablesFor(spec ScheduleSpec, typed bool) map[string]string {
	if typed {
		return spec.Variables
	}
	if vars, ok := legacyPollModeVariables[spec.ComponentName]; ok {
		return vars
	}
	return spec.Variables
}
