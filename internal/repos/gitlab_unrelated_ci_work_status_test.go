package repos

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const unguardedBranchJob = "build:\n  script: [./build.sh]\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"main\"\n"

// repos status reports the dispatch gate's unrelated-work scan whatever the
// trigger account's state (#9708). A branch-only job with no pipeline-source
// guard runs in trigger, agent child and scheduled pipelines, and every
// source's finding is listed.
func TestDiagnoseGitLabUnrelatedCIWork_ReportsEverySource(t *testing.T) {
	fc := newFakeClientForBatch("acme/widgets")
	fc.FileContents["acme/widgets/.gitlab-ci.yml"] = []byte(unguardedBranchJob)

	problems := DiagnoseGitLabUnrelatedCIWork(context.Background(), fc, "acme", "widgets")
	require.Len(t, problems, 3, "%v", problems)
	assert.Contains(t, problems[0], `the committed .gitlab-ci.yml has job "build" that a trigger pipeline would run`)
	assert.Contains(t, problems[1], "agent child pipeline (source=pipeline)")
	assert.Contains(t, problems[2], "scheduled pipeline (source=schedule)")
}

func TestDiagnoseGitLabUnrelatedCIWork_CleanReportsNothing(t *testing.T) {
	fc := newFakeClientForBatch("acme/widgets")
	fc.FileContents["acme/widgets/.gitlab-ci.yml"] = []byte("lint:\n  script: [./lint.sh]\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"merge_request_event\"\n")

	assert.Nil(t, DiagnoseGitLabUnrelatedCIWork(context.Background(), fc, "acme", "widgets"))
}

// A scan that cannot complete is reported as unverified, never as clean.
func TestDiagnoseGitLabUnrelatedCIWork_UnverifiedReasons(t *testing.T) {
	t.Run("repository read fails", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/widgets")
		fc.Errors["GetRepo"] = errors.New("boom")
		problems := DiagnoseGitLabUnrelatedCIWork(context.Background(), fc, "acme", "widgets")
		require.Len(t, problems, 1)
		assert.Contains(t, problems[0], "could not verify that unrelated CI jobs are excluded")
		assert.Contains(t, problems[0], "reading the repository")
		assert.NotContains(t, problems[0], "boom")
	})
	t.Run("default branch unknown", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/widgets")
		fc.Repos[0].DefaultBranch = ""
		problems := DiagnoseGitLabUnrelatedCIWork(context.Background(), fc, "acme", "widgets")
		require.Len(t, problems, 1)
		assert.Contains(t, problems[0], "the default branch is unknown")
	})
	t.Run("file read fails", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/widgets")
		fc.Errors["GetFileContent"] = errors.New("boom")
		problems := DiagnoseGitLabUnrelatedCIWork(context.Background(), fc, "acme", "widgets")
		require.Len(t, problems, 1)
		assert.Contains(t, problems[0], "could not verify that unrelated CI jobs are excluded")
	})
}

// repos status carries the findings on the repository row and in JSON, but
// they are informational: they add no drift and do not change the drifted
// count, because polling keeps working.
func TestStatus_GitLab_ReportsUnrelatedCIWork(t *testing.T) {
	full := "acme/widgets"
	fc := newFakeClientForBatch(full)
	populateGitLabInstalled(fc, "acme", "widgets")
	// The installed fixture's wrapper is a stub; the scan reads the real one.
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	fc.FileContents[full+"/"+fullsendPipelineInclude] = wrapper
	m := gitlabConvergeCfg(full).Manifest

	baseline, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, baseline.Repos, 1)
	assert.Empty(t, baseline.Repos[0].GitLabUnrelatedCIWork)

	fc.FileContents[full+"/.gitlab-ci.yml"] = []byte(unguardedBranchJob)
	result, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	st := result.Repos[0]
	require.Len(t, st.GitLabUnrelatedCIWork, 3, "%v", st.GitLabUnrelatedCIWork)
	assert.Contains(t, st.GitLabUnrelatedCIWork[2], "scheduled pipeline (source=schedule)")
	assert.Equal(t, baseline.Repos[0].Drifts, st.Drifts)
	assert.Equal(t, baseline.Summary, result.Summary)

	raw, err := json.Marshal(st)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"gitlab_unrelated_ci_work":[`)
}
