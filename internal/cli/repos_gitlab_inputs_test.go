package cli

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// seedGitLabInputPrerequisites models an explicitly secured project and an
// available pinned scaffold. Production cannot assume either prerequisite.
func seedGitLabInputPrerequisites(fc *forge.FakeClient, repo, ref string) {
	fc.PipelineVarOverrideRoles[repo] = forge.PipelineVarOverrideNoOneAllowed
	err := scaffold.WalkGitLabPerRepo(func(path string, content []byte) error {
		fc.FileContentsRef["fullsend-ai/fullsend/internal/scaffold/fullsend-repo-gitlab/"+path+"@"+ref] = content
		return nil
	})
	if err != nil {
		panic(err)
	}
}

func TestCLIActivationDefersUntilRepairTemplatesLand(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "dry-run"}[dryRun], func(t *testing.T) {
			t.Setenv(repos.GitLabPipelineVarRestrictionEnv, "enforced")
			fc := forge.NewFakeClient()
			root, err := scaffold.GitLabPerRepoFile(".gitlab-ci.yml")
			require.NoError(t, err)
			wrapper, err := scaffold.GitLabPerRepoFile(".gitlab/ci/fullsend-pipeline.yml")
			require.NoError(t, err)
			agent, err := scaffold.GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
			require.NoError(t, err)
			poll, err := scaffold.GitLabPerRepoFile(".gitlab/ci/fullsend-poll.yml")
			require.NoError(t, err)
			// The root .gitlab-ci.yml must already declare and forward the
			// full pipeline-input contract so this test exercises the
			// intended sibling-template (poll) and restriction checks
			// instead of tripping the separate root-contract deferral in
			// gitlabRootDeclaresDispatchInputs.
			fc.FileContents["group/project/.gitlab-ci.yml"] = root
			fc.FileContents["group/project/.gitlab/ci/fullsend-pipeline.yml"] = wrapper
			fc.FileContents["group/project/.gitlab/ci/fullsend-agent.yml"] = agent
			fc.FileContents["group/project/.gitlab/ci/fullsend-poll.yml"] = []byte("fullsend-poll:\n  variables: {FULLSEND_POLL_MODE: events}\n")
			fc.PipelineVarOverrideRoles["group/project"] = forge.PipelineVarOverrideDeveloper
			fc.PipelineSchedules["group/project"] = []forge.PipelineSchedule{{ID: 1, Description: "fullsend slash poll", Variables: map[string]string{forge.VarPollMode: "slash"}}}
			var output bytes.Buffer
			err = ensureGitLabPipelineVariableOverrideRole(context.Background(), fc, ui.New(&output), "group", "project", dryRun)
			require.NoError(t, err)
			assert.Contains(t, output.String(), "deferred")
			assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["group/project"])
			assert.Equal(t, "slash", fc.PipelineSchedules["group/project"][0].Variables[forge.VarPollMode])

			// Even after the repair MR merges, a weaker restriction is not
			// permission to activate. An administrator must secure it first.
			fc.FileContents["group/project/.gitlab/ci/fullsend-poll.yml"] = poll
			err = ensureGitLabPipelineVariableOverrideRole(context.Background(), fc, ui.New(io.Discard), "group", "project", dryRun)
			require.ErrorContains(t, err, "refusing to deliver runnable typed GitLab jobs")
			assert.Equal(t, "slash", fc.PipelineSchedules["group/project"][0].Variables[forge.VarPollMode])
			fc.PipelineVarOverrideRoles["group/project"] = forge.PipelineVarOverrideNoOneAllowed
			err = ensureGitLabPipelineVariableOverrideRole(context.Background(), fc, ui.New(io.Discard), "group", "project", dryRun)
			require.NoError(t, err)
			if dryRun {
				assert.Equal(t, "slash", fc.PipelineSchedules["group/project"][0].Variables[forge.VarPollMode])
			} else {
				assert.Empty(t, fc.PipelineSchedules["group/project"][0].Variables)
			}
		})
	}
}

func TestLegacyGitLabWrapperKeepsVariableCompatibleSetting(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.FileContents["group/project/.gitlab/ci/fullsend-pipeline.yml"] = []byte("include:\n  - local: .gitlab/ci/fullsend-agent.yml\n")
	fc.PipelineVarOverrideRoles["group/project"] = forge.PipelineVarOverrideDeveloper
	err := ensureGitLabPipelineVariableOverrideRole(context.Background(), fc, ui.New(io.Discard), "group", "project", false)
	require.NoError(t, err)
	assert.Equal(t, forge.PipelineVarOverrideDeveloper, fc.PipelineVarOverrideRoles["group/project"])
}
