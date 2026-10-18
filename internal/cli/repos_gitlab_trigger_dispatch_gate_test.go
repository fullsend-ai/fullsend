package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func TestReportGitLabTriggerDispatchGate_DeferredReportsEveryReason(t *testing.T) {
	// A legacy repository: its managed schedule still selects the poll mode
	// with a pipeline variable, overrides are allowed, and no trigger account
	// is installed. The report is informational and changes nothing.
	fc := forge.NewFakeClient()
	fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{
		{ID: 7, Description: "fullsend slash poll", Variables: map[string]string{forge.VarPollMode: "slash"}},
	}
	fc.PipelineVarOverrideRoles["acme/widgets"] = forge.PipelineVarOverrideDeveloper

	var buf bytes.Buffer
	reportGitLabTriggerDispatchGate(context.Background(), fc, nil, ui.New(&buf), "acme", "widgets", "main", nil)

	out := buf.String()
	assert.Contains(t, out, "[acme/widgets] GitLab trigger dispatch activation deferred")
	assert.Contains(t, out, "managed schedule 7")
	assert.Contains(t, out, `ci_pipeline_variables_minimum_override_role is "developer", not no_one_allowed`)
	assert.Contains(t, out, "without the trigger-account API")
	assert.Contains(t, out, "the trigger account is not-installed")
	assert.Contains(t, out, "Polling stays the dispatch path")
	assert.Equal(t, "slash", fc.PipelineSchedules["acme/widgets"][0].Variables[forge.VarPollMode])
	assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["acme/widgets"])
}

func TestReportGitLabTriggerDispatchGate_PrerequisitesVerified(t *testing.T) {
	// The live verification itself is covered in internal/repos; this checks
	// the report for a gate whose prerequisites all passed.
	var buf bytes.Buffer
	printGitLabTriggerDispatchGate(ui.New(&buf), "acme/widgets", repos.GitLabTriggerDispatchGate{Ready: true})

	out := buf.String()
	if !assert.Contains(t, out, "GitLab trigger dispatch prerequisites verified") {
		return
	}
	assert.Contains(t, out, "the dispatch activation is recorded")
	assert.NotContains(t, out, "deferred")
	assert.NotContains(t, out, "Unverified inherited")
}

func TestReportGitLabTriggerDispatchGate_ReportsUnverifiedInheritedScopes(t *testing.T) {
	var buf bytes.Buffer
	printGitLabTriggerDispatchGate(ui.New(&buf), "acme/widgets", repos.GitLabTriggerDispatchGate{
		Ready:                     true,
		UnverifiedInheritedScopes: []string{"instance", "acme"},
	})

	out := buf.String()
	assert.Contains(t, out, "Unverified inherited CI/CD variable scopes")
	assert.Contains(t, out, "instance, acme")
	assert.Contains(t, out, "before the job verifies its creator")
}
