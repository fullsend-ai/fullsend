package scaffold

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitLabPerRepoFilesExist(t *testing.T) {
	expected := []string{
		".gitlab-ci.yml",
		".fullsend/config.yaml",
		".gitlab/ci/fullsend-dispatch.yml",
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/fullsend-agent.yml",
	}

	for _, path := range expected {
		content, err := GitLabPerRepoFile(path)
		require.NoError(t, err, "reading %s", path)
		assert.NotEmpty(t, content, "%s should not be empty", path)
	}
}

func TestGitLabConfigContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".fullsend/config.yaml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "forge: gitlab")
}

func TestGitLabPerRepoFileNotFound(t *testing.T) {
	_, err := GitLabPerRepoFile("nonexistent-file.yml")
	assert.Error(t, err)
}

func TestWalkGitLabPerRepo(t *testing.T) {
	var paths []string
	err := WalkGitLabPerRepo(func(path string, content []byte) error {
		paths = append(paths, path)
		return nil
	})
	require.NoError(t, err)
	assert.True(t, len(paths) >= 4, "expected at least 4 files, got %d", len(paths))
}

func TestWalkGitLabPerRepoRelativePaths(t *testing.T) {
	err := WalkGitLabPerRepo(func(path string, _ []byte) error {
		assert.False(t, strings.HasPrefix(path, "fullsend-repo-gitlab/"),
			"path should be relative, got %s", path)
		return nil
	})
	require.NoError(t, err)
}

func TestAllGitLabYAMLDocumentStartMarker(t *testing.T) {
	var checked int
	err := WalkGitLabPerRepo(func(path string, content []byte) error {
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		assert.True(t, strings.HasPrefix(string(content), "---\n"),
			"%s must start with YAML document start marker (---)", path)
		checked++
		return nil
	})
	require.NoError(t, err)
	assert.True(t, checked >= 4, "expected at least 4 YAML files, got %d", checked)
}

func TestGitLabDispatchContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-dispatch.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: dispatch")
	assert.Contains(t, s, "merge_request_event")
	assert.Contains(t, s, "MR_STATE")
	assert.Contains(t, s, "mr-dispatch-pipeline.yml")
	assert.Contains(t, s, "stages:")
	assert.Contains(t, s, "- agent")
	// Child pipeline passes IS_FORK for fork protection in agent template
	assert.Contains(t, s, `IS_FORK: "${IS_FORK}"`)
	// Merged MR fallback should no-op (retro via cron-poller)
	assert.Contains(t, s, "retro via cron-poller")
	// Closed MRs dispatch retro (best-effort on GitLab)
	assert.Contains(t, s, "review|retro) ;;")
	assert.Contains(t, s, "Best-effort on GitLab")
	// SECURITY comment on heredoc interpolation
	assert.Contains(t, s, "SECURITY")
	// MR_AUTHOR_ID passed to child pipeline for stage-level authorization
	assert.Contains(t, s, `MR_AUTHOR_ID`)
	// STATUS_IID passed to child pipeline for --status-number resilience
	assert.Contains(t, s, `STATUS_IID: "${CI_MERGE_REQUEST_IID}"`)

	// Authorization gate moved to agent template (Members API requires bot PAT)
	assert.NotContains(t, s, "PRIVATE-TOKEN")
	assert.NotContains(t, s, "Developer access")

	// MR API error handling distinguishes permanent from transient
	assert.Contains(t, s, "job token")
	assert.Contains(t, s, "MR API unavailable")
	// Fork MR API failures are no-op (job-token scope), not hard failure
	assert.Contains(t, s, "fork MR (expected: job-token scope)")
	// Connection-level failures go to no-op
	assert.Contains(t, s, "MR API unreachable")
	// Fork status computed before API call from predefined variables
	assert.Contains(t, s, "Detect fork before API call")
	// Error message points at job token permissions, not a version number
	assert.Contains(t, s, "job token permissions")
	assert.NotContains(t, s, "15.3+")
	assert.NotContains(t, s, "18.4+")

	// ENTRYPOINT override for runner image
	assert.Contains(t, s, `entrypoint: [""]`)
	// NormalizedEvent v1 construction
	assert.Contains(t, s, "NormalizedEvent")
	assert.Contains(t, s, `"change_proposal"`)
	assert.Contains(t, s, "transition_kind")
	assert.Contains(t, s, "actor_role")
	assert.Contains(t, s, `"gitlab"`)
	// head_repo/base_repo use predefined CI_MERGE_REQUEST_* variables
	assert.Contains(t, s, "CI_MERGE_REQUEST_SOURCE_PROJECT_PATH")
	assert.Contains(t, s, "CI_MERGE_REQUEST_PROJECT_PATH")
	assert.NotContains(t, s, `--arg head_repo "${CI_MERGE_REQUEST_SOURCE_PROJECT_ID}"`)
	assert.NotContains(t, s, `--arg base_repo "${CI_MERGE_REQUEST_TARGET_PROJECT_ID}"`)
	// Uses target project ID for API calls (correct in fork context)
	assert.Contains(t, s, "CI_MERGE_REQUEST_PROJECT_ID")
	// head_sha uses CI_MERGE_REQUEST_SOURCE_BRANCH_SHA for merged-results
	assert.Contains(t, s, "CI_MERGE_REQUEST_SOURCE_BRANCH_SHA")
	// Labels extracted from MR API response
	assert.Contains(t, s, "MR_LABELS")
	assert.Contains(t, s, "labels")
	// Bot detection via username heuristic (covers GitLab project/group bot format)
	assert.Contains(t, s, `_bot_`)
	assert.Contains(t, s, `\\[bot\\]`)
	// STAGE allowlist defense-in-depth
	assert.Contains(t, s, "review|retro) ;;")
	// CEL trigger equivalent comments for future fullsend dispatch migration
	assert.Contains(t, s, "CEL:")
	assert.Contains(t, s, "entity.kind")
	assert.Contains(t, s, "transition.kind")
	// Child pipeline includes the generic agent template
	assert.Contains(t, s, "fullsend-agent.yml")
	assert.NotContains(t, s, "fullsend-${STAGE}.yml")
}

func TestGitLabAgentTemplateContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	// Standalone pipeline config: must declare stages for poller path
	assert.Contains(t, s, "stages:")
	assert.Contains(t, s, "- agent")
	// Generic template parameterized by STAGE
	assert.Contains(t, s, `fullsend run "${STAGE}"`)
	assert.Contains(t, s, "--fullsend-dir")
	assert.Contains(t, s, "--target-repo")
	assert.Contains(t, s, "--output-dir")
	assert.Contains(t, s, "--status-repo")
	assert.Contains(t, s, "--status-number")
	// Poll-triggered pipelines use STATUS_IID from child pipeline variables
	assert.Contains(t, s, "STATUS_IID")
	assert.Contains(t, s, "--run-url")
	assert.Contains(t, s, "--forge gitlab")
	// Should NOT use nonexistent flags
	assert.NotContains(t, s, "--stage")
	assert.NotContains(t, s, "--event-payload-file")
	assert.NotContains(t, s, "--event-type")
	assert.NotContains(t, s, "--source-project")
	assert.NotContains(t, s, "fullsend workspace prepare")
	// Credential validation
	assert.Contains(t, s, "FULLSEND_FORGE_TOKEN is not set")
	assert.Contains(t, s, "FULLSEND_CREDENTIAL_MODE must be")
	// Should NOT check CI_PIPELINE_SOURCE — child pipelines see parent_pipeline
	assert.NotContains(t, s, `CI_PIPELINE_SOURCE`)
	// Generic runner image, not agent-specific
	assert.Contains(t, s, "fullsend-runner:latest")
	assert.NotContains(t, s, "fullsend-code:latest")
	// Resource group parameterized by STAGE
	assert.Contains(t, s, `fullsend-${STAGE}-${RESOURCE_KEY}`)
	// Rules gate on STAGE being set
	assert.Contains(t, s, `$STAGE != ""`)
	// ENTRYPOINT override for runner image
	assert.Contains(t, s, `entrypoint: [""]`)
	// Uses python3 for YAML parsing (yq not in runner image)
	assert.Contains(t, s, "python3")
	assert.NotContains(t, s, "yq")
	// No fallback to working-tree config (untrusted in MR context)
	assert.NotContains(t, s, "cat .fullsend/config.yaml")
}

func TestGitLabAgentTemplateKillSwitch(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "kill_switch")
	assert.Contains(t, s, "Kill switch is active")
	assert.Contains(t, s, "kill_switch: false")
	// Config read from default branch (trusted), not MR source
	assert.Contains(t, s, "CI_DEFAULT_BRANCH")
	assert.Contains(t, s, "FETCH_HEAD")
	assert.Contains(t, s, "CONFIG_YAML")
	// Fetch failure fails the job (not silently permissive)
	assert.Contains(t, s, "cannot fetch default branch")
	// Kill switch uses python3 for YAML parsing
	assert.Contains(t, s, "python3")
	assert.Contains(t, s, "yaml.safe_load")
}

func TestGitLabAgentTemplateRoleEnablement(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "STAGE_ROLE")
	assert.Contains(t, s, `code|fix) STAGE_ROLE="coder"`)
	assert.Contains(t, s, "not in configured roles")
	// Backward compat: "fullsend" role implies retro + prioritize
	assert.Contains(t, s, `"fullsend"`)
	assert.Contains(t, s, "retro|prioritize")
}

func TestGitLabAgentTemplateForkProtection(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "Fork MR detected")
	assert.Contains(t, s, "IS_FORK")
	// Fail-closed: default to true when IS_FORK is unset
	assert.Contains(t, s, `IS_FORK:-true`)
	assert.NotContains(t, s, `IS_FORK:-false`)
	// Fork check applies only to code/fix stages
	assert.Contains(t, s, `"code"`)
	assert.Contains(t, s, `"fix"`)
	// Fork check uses IS_FORK variable, not jq on event payload
	assert.NotContains(t, s, "EVENT_PAYLOAD")
	// Fork detection exits with error (visible failure), not silent skip
	assert.Contains(t, s, "exit 1")
}

func TestGitLabAgentTemplateAuthorizationGate(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	// Authorization uses bot PAT (PRIVATE-TOKEN), not job token
	assert.Contains(t, s, "PRIVATE-TOKEN")
	assert.Contains(t, s, "members/all")
	assert.Contains(t, s, "access_level")
	assert.Contains(t, s, "Developer access")
	// Fail-closed on API failure
	assert.Contains(t, s, "fail-closed")
	// Distinct warning when MR_AUTHOR_ID is unset (poller non-MR events)
	assert.Contains(t, s, "MR_AUTHOR_ID not set")
	// Read-only stages exempt from Developer-access gate
	assert.Contains(t, s, `"retro"`)
	assert.Contains(t, s, `"prioritize"`)
}

func TestGitLabAgentTemplateCredentialValidation(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "CI_DEBUG_TRACE")
	assert.Contains(t, s, "FULLSEND_CREDENTIAL_MODE")
	assert.Contains(t, s, "FULLSEND_FORGE_TOKEN is not set")
	assert.Contains(t, s, "'wif' or 'variable'")
	// OIDC token file must NOT be deleted before gcloud secrets
	assert.NotContains(t, s, "trap - EXIT")
}

func TestGitLabPollContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-poll.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: poll")
	assert.Contains(t, s, "fullsend poll")
	assert.Contains(t, s, "dispatches.json")
	assert.Contains(t, s, "child-pipeline.yml")
	assert.Contains(t, s, "schedule")
	assert.Contains(t, s, "CI_COMMIT_REF_PROTECTED")
	// Credential validation
	assert.Contains(t, s, "FULLSEND_FORGE_TOKEN is not set")
	assert.Contains(t, s, "FULLSEND_CREDENTIAL_MODE must be")
	// Defaults to CI_SERVER_URL, not hardcoded gitlab.com
	assert.Contains(t, s, "CI_SERVER_URL")
	assert.NotContains(t, s, "https://gitlab.com")
	// ENTRYPOINT override for runner image
	assert.Contains(t, s, `entrypoint: [""]`)
	// Poll and generate are merged into one job — no separate generate job
	assert.NotContains(t, s, "generate-child-pipelines:")
	// Child pipeline generation happens inside poll-events
	assert.Contains(t, s, "generate-child-pipeline")
	// No-op child pipeline handles empty dispatches (no dotenv gating —
	// GitLab evaluates rules: at pipeline creation, before dotenv is available)
	assert.NotContains(t, s, "dispatch.env")
	assert.NotContains(t, s, "HAS_DISPATCHES")
}

func TestGitLabRootPipelineContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab-ci.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "stages:")
	assert.Contains(t, s, "- dispatch")
	assert.Contains(t, s, "- poll")
	assert.NotContains(t, s, "- generate")
	assert.Contains(t, s, "- agent")
	assert.Contains(t, s, "fullsend-dispatch.yml")
	assert.Contains(t, s, "fullsend-poll.yml")
	// push pipelines intentionally excluded — documented in workflow comment
	assert.Contains(t, s, "Push-triggered pipelines are intentionally excluded")
	// parent_pipeline rule removed (child pipelines don't inherit workflow:rules)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "parent_pipeline"`)
}

func TestGitLabNoPerStageTemplates(t *testing.T) {
	perStageFiles := []string{
		".gitlab/ci/fullsend-review.yml",
		".gitlab/ci/fullsend-code.yml",
		".gitlab/ci/fullsend-fix.yml",
		".gitlab/ci/fullsend-retro.yml",
		".gitlab/ci/fullsend-triage.yml",
		".gitlab/ci/fullsend-prioritize.yml",
	}
	for _, path := range perStageFiles {
		_, err := GitLabPerRepoFile(path)
		assert.Error(t, err, "per-stage template %s should not exist — use fullsend-agent.yml", path)
	}
}
