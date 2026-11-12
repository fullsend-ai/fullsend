package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// Each deferred reason is printed on its own line, so a reason that contains
// semicolons cannot be confused with the next one (#9248).
func TestPrintGitLabTriggerDispatchGate_OneLinePerReason(t *testing.T) {
	var buf bytes.Buffer
	printGitLabTriggerDispatchGate(ui.New(&buf), "acme/widgets", repos.GitLabTriggerDispatchGate{
		Deferred: []string{
			"the trigger account lacks merge permission; managed schedules stay installer-owned",
			"the committed workflow does not admit fullsend slash poll",
		},
	})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	assert.Contains(t, lines[0], "[acme/widgets] GitLab trigger dispatch activation deferred. Polling stays the dispatch path. Reasons:")
	assert.Contains(t, lines[1], "[acme/widgets]   - the trigger account lacks merge permission; managed schedules stay installer-owned")
	assert.Contains(t, lines[2], "[acme/widgets]   - the committed workflow does not admit fullsend slash poll")
}

// Reasons can embed repository text such as a CI job key. Terminal control
// sequences and line breaks in it must not reach the install output.
func TestPrintGitLabTriggerDispatchGate_SanitizesReasons(t *testing.T) {
	var buf bytes.Buffer
	printGitLabTriggerDispatchGate(ui.New(&buf), "acme/widgets", repos.GitLabTriggerDispatchGate{
		Deferred: []string{"the committed .gitlab-ci.yml defines its own fullsend \x1b[2J\nfake agent job"},
	})

	out := buf.String()
	assert.NotContains(t, out, "\x1b")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[1], "[acme/widgets]   - the committed .gitlab-ci.yml defines its own fullsend ?[2J?fake agent job")
}

// The repository name and inherited scope names are sanitized too, on both the
// deferred and the ready paths.
func TestPrintGitLabTriggerDispatchGate_SanitizesNames(t *testing.T) {
	var buf bytes.Buffer
	printGitLabTriggerDispatchGate(ui.New(&buf), "acme/wid\x1b[2Jgets", repos.GitLabTriggerDispatchGate{
		Deferred: []string{"a reason"},
	})
	printGitLabTriggerDispatchGate(ui.New(&buf), "acme/wid\x1b[2Jgets", repos.GitLabTriggerDispatchGate{
		Ready:                     true,
		UnverifiedInheritedScopes: []string{"ins\x1b[2Jtance", "ac\nme"},
	})

	out := buf.String()
	assert.NotContains(t, out, "\x1b")
	assert.Contains(t, out, "[acme/wid?[2Jgets]   - a reason")
	assert.Contains(t, out, "ins?[2Jtance, ac?me")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 4)
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
