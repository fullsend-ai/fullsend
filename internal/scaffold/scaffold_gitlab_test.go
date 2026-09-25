package scaffold

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitLabPerRepoFilesExist(t *testing.T) {
	expected := []string{
		".gitignore",
		".gitlab-ci.yml",
		".fullsend/config.yaml",
		".gitlab/ci/fullsend-pipeline.yml",
		".gitlab/ci/fullsend-dispatch.yml",
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/fullsend-agent.yml",
		".gitlab/ci/scripts/trust-ci-server-ca.sh",
		".gitlab/ci/scripts/select-gitlab-role-token.sh",
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

func TestGitLabGitignoreExcludesOutput(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitignore")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "output/")
}

func TestCollectGitLabPerRepoInstallFiles_SkipsGitignore(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "", "")
	require.NoError(t, err)
	for _, f := range files {
		assert.NotEqual(t, ".gitignore", f.Path,
			"install must not overwrite consumer .gitignore with the scaffold fragment")
	}
}

func TestCollectGitLabPerRepoInstallFiles_SkipsRootPipeline(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "", "")
	require.NoError(t, err)
	for _, f := range files {
		assert.NotEqual(t, ".gitlab-ci.yml", f.Path,
			"install must not overwrite consumer .gitlab-ci.yml — "+
				"root file is merged dynamically by the install flow")
	}
	// Pipeline wrapper must still be present.
	var hasPipeline bool
	for _, f := range files {
		if f.Path == ".gitlab/ci/fullsend-pipeline.yml" {
			hasPipeline = true
		}
	}
	assert.True(t, hasPipeline, "pipeline wrapper must be in install files")
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
	// Native MR dispatch jobs were removed in #7322. The file is kept
	// as the version-marker carrier and must not define CI jobs.
	assert.Contains(t, s, "# fullsend-stage: dispatch")
	assert.Contains(t, s, "#7322")
	assert.Contains(t, s, "cron poller")
	assert.NotContains(t, s, "dispatch-mr-agents:")
	assert.NotContains(t, s, "mr-dispatch-pipeline.yml")
	assert.NotContains(t, s, "__RUNNER_TAGS__")
	assert.NotRegexp(t, `(?m)^dispatch:`, s, "must not define a dispatch job")
	assert.True(t, strings.HasPrefix(s, "---\n"),
		"must start with YAML document start marker (---)")
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
	assert.Contains(t, s, `fullsend eval-measure`)
	assert.Contains(t, s, "RUN_STATUS=$?")
	assert.Contains(t, s, `exit "${RUN_STATUS}"`)
	assert.Contains(t, s, "|| true")
	assert.Contains(t, s, "60 req/hr")
	assert.Contains(t, s, "artifacts:")
	assert.Contains(t, s, "when: always")
	assert.Contains(t, s, `${CI_PROJECT_DIR}/output`)
	assert.NotContains(t, s, "/tmp/fullsend-output")
	// Measurement override from default branch tip — never MR-tree --fullsend-dir.
	assert.Contains(t, s, "DEFAULT_BRANCH_SHA")
	assert.Contains(t, s, `git show "${DEFAULT_BRANCH_SHA}:.fullsend/eval/measurements/${STAGE}.yaml"`)
	assert.Contains(t, s, `MEASURE_ARGS+=(--registry "${MEASURE_FILE}")`)
	assert.Contains(t, s, `fullsend eval-measure "${MEASURE_ARGS[@]}"`)
	assert.NotContains(t, s, `eval-measure \
        --agent "${STAGE}" \
        --fullsend-dir .fullsend`)
	// work_item URL must not invent …/issues/0 when IID is missing, but
	// GITLAB_ISSUE_URL must still be exported (empty OK) so harness env
	// validation does not reject a truly unset variable.
	assert.NotContains(t, s, `/-/issues/${STATUS_IID:-0}`)
	assert.Contains(t, s, `"${STATUS_IID}" != "0"`)
	assert.Contains(t, s, `GITLAB_ISSUE_URL=""`)
	assert.Contains(t, s, "export GITLAB_ISSUE_URL")
	assert.Contains(t, s, "--fullsend-dir")
	assert.Contains(t, s, "--target-repo")
	assert.Contains(t, s, "--output-dir")
	assert.Contains(t, s, "--status-repo")
	assert.Contains(t, s, "--status-number")
	// Poll-triggered pipelines use STATUS_IID from child pipeline variables
	assert.Contains(t, s, "STATUS_IID")
	// MR events export FULLSEND_NOTE_TARGET so status comments target MRs
	assert.Contains(t, s, `FULLSEND_NOTE_TARGET="merge_requests"`)
	// Review stage fetches prior review from MR notes
	assert.Contains(t, s, "PRIOR_REVIEW_FILE")
	assert.Contains(t, s, "PRIOR_REVIEW_SHA")
	assert.Contains(t, s, "PRIOR_REVIEW_PROVENANCE")
	// Fix stage fetches review body from MR notes
	assert.Contains(t, s, "REVIEW_BODY_FILE")
	// Provenance values match the review agent prompt's expected labels
	assert.Contains(t, s, `bot-verified`)
	assert.Contains(t, s, `unverifiable-wrong-user`)
	assert.NotContains(t, s, `author-verified`)
	assert.NotContains(t, s, `unverifiable-wrong-author`)
	// Prior review fetching paginates (while loop, not single page)
	assert.Contains(t, s, `PAGE=1`)
	assert.Contains(t, s, `-le 20`)
	// Prior review uses CI_API_V4_URL consistently (not CI_SERVER_URL/api/v4)
	assert.NotRegexp(t, `CI_SERVER_URL.*/api/v4/projects.*/notes`, s)
	// Reuses BOT_USER_ID from bot identity verification when available
	assert.Contains(t, s, `BOT_USER_ID:-`)
	assert.Contains(t, s, "--run-url")
	assert.Contains(t, s, "--forge gitlab")
	// Should NOT use nonexistent flags
	assert.NotContains(t, s, "--stage")
	assert.NotContains(t, s, "--event-payload-file")
	assert.NotContains(t, s, "--event-type")
	assert.NotContains(t, s, "--source-project")
	assert.NotContains(t, s, "fullsend workspace prepare")
	// Credential selection uses the shared role-token helper.
	assert.Contains(t, s, "select-gitlab-role-token.sh")
	assert.Contains(t, s, "FULLSEND_JOB_KIND=agent")
	assert.Contains(t, s, `PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}`)
	assert.NotContains(t, s, `PRIVATE-TOKEN: ${FULLSEND_FORGE_TOKEN}`)
	// Bot identity verification uses server-side .source from Pipelines API
	// (deny-by-default case statement, not forgeable CI_PIPELINE_SOURCE env var)
	assert.Contains(t, s, `jq -r '.source // empty'`)
	assert.Contains(t, s, `parent_pipeline)`)
	assert.Contains(t, s, "unexpected pipeline source")
	assert.Contains(t, s, "rejecting forged dispatch")
	// Generic runner image, not agent-specific
	assert.Contains(t, s, "fullsend-runner:dev")
	assert.NotContains(t, s, "fullsend-code:latest")
	// Resource group parameterized by STAGE
	assert.Contains(t, s, `fullsend-${STAGE}-${RESOURCE_KEY}`)
	// Rules gate on STAGE being set (truthy form — `$STAGE != ""` would
	// match when STAGE is undefined because GitLab evaluates null != "" as true)
	assert.Contains(t, s, "if: $STAGE")
	assert.NotContains(t, s, `$STAGE != ""`)
	// ENTRYPOINT override for runner image
	assert.Contains(t, s, `entrypoint: [""]`)
	// Uses python3 for YAML parsing (yq not in runner image)
	assert.Contains(t, s, "python3")
	assert.NotContains(t, s, "yq")
	// No fallback to working-tree config (untrusted in MR context)
	assert.NotContains(t, s, "cat .fullsend/config.yaml")
	// Back-link from dispatched pipelines to poll job
	assert.Contains(t, s, "FULLSEND_POLL_JOB_URL")
	assert.Contains(t, s, "Dispatched by:")
	// Harness passthrough variables must be declared so os.Expand doesn't
	// reject unset variables during harness env validation (#6273).
	assert.Contains(t, s, "CODE_ALLOWED_TARGET_BRANCHES")
	// RUNNER_TEMP must be exported with /tmp fallback so harness host_files
	// paths that reference ${RUNNER_TEMP} resolve on GitLab CI (#6460).
	assert.Contains(t, s, `export RUNNER_TEMP="${RUNNER_TEMP:-/tmp}"`)
	// Runtime CLI install via before_script (#6445)
	assert.Contains(t, s, "before_script:")
	// CI_DEBUG_TRACE guard must be in before_script, before token-bearing commands
	assert.Contains(t, s, "CI_DEBUG_TRACE")
	// Private-CA trust from CI_SERVER_TLS_CA_FILE before GitLab network ops.
	assert.Contains(t, s, "trust-ci-server-ca.sh")
	assert.Contains(t, s, "__FULLSEND_VERSION__")
	assert.Contains(t, s, "fullsend-ai/fullsend")
	assert.Contains(t, s, "fullsend --version")
	// Release path downloads pre-built binary with checksum verification
	assert.Contains(t, s, "github.com/${FULLSEND_REPO}/releases/download")
	assert.Contains(t, s, "checksums.txt")
	assert.Contains(t, s, "sha256sum -c")
	// Source-build path clones and builds from source (direct go build, no make)
	assert.Contains(t, s, "go build")
	assert.Contains(t, s, "./cmd/fullsend/")
	// Source-build path sets GOPATH and GOCACHE so go build works on
	// non-root runners where /root/go is not writable (#6477).
	assert.Contains(t, s, `export GOPATH="${RUNNER_TEMP:-/tmp}/go"`)
	assert.Contains(t, s, `export GOCACHE="${RUNNER_TEMP:-/tmp}/go-cache"`)
	// "latest" resolution via GitHub API
	assert.Contains(t, s, "releases/latest")
	// tar extraction uses --no-same-owner for non-root containers (#6720)
	assert.Contains(t, s, "tar --no-same-owner")
	assert.NotContains(t, s, "tar xzf /tmp/fullsend.tar.gz")
}

// TestGitLabAgentTemplateBotIdentityNotLeakedPastRoleReselection guards
// against the poller's bot identity (BOT_USER_ID/BOT_RESPONSE, populated
// by the /user call made with the FULLSEND_JOB_KIND=poller bootstrap
// token) leaking into downstream blocks after FULLSEND_JOB_KIND=agent
// re-selects FULLSEND_JOB_TOKEN for the stage's actual role. GitLab
// assigns a distinct bot user per project access token, so the poller's
// identity differs from the analyst/coder identity used for the rest of
// the job — reusing it would misattribute prior-review lookups and
// GIT_BOT_EMAIL to the wrong bot user.
func TestGitLabAgentTemplateBotIdentityNotLeakedPastRoleReselection(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)

	reselectIdx := strings.Index(s, "FULLSEND_JOB_KIND=agent")
	require.NotEqual(t, -1, reselectIdx, "agent role re-selection marker not found")

	unsetIdx := strings.Index(s, "unset BOT_USER_ID BOT_RESPONSE")
	require.NotEqual(t, -1, unsetIdx,
		"expected BOT_USER_ID/BOT_RESPONSE to be discarded after the stage-role re-selection")
	assert.Greater(t, unsetIdx, reselectIdx,
		"BOT_USER_ID/BOT_RESPONSE must be discarded after (not before) the agent role re-selection")

	// Downstream reuse points must come after the discard, so they always
	// re-fetch /user with the re-selected FULLSEND_JOB_TOKEN instead of
	// silently reusing the poller's stale identity.
	reviewReuseIdx := strings.Index(s, `BOT_ID="${BOT_USER_ID:-}"`)
	require.NotEqual(t, -1, reviewReuseIdx, "review/fix-stage BOT_ID reuse not found")
	assert.Greater(t, reviewReuseIdx, unsetIdx,
		"prior-review lookups must run after the poller identity is discarded")

	sharedBlockReuseIdx := strings.Index(s, `if [ -n "${BOT_RESPONSE:-}" ]; then`)
	require.NotEqual(t, -1, sharedBlockReuseIdx, "shared-block BOT_RESPONSE reuse not found")
	assert.Greater(t, sharedBlockReuseIdx, unsetIdx,
		"shared code|fix|review block must run after the poller identity is discarded")
}

// TestGitLabAgentTemplateStageReselectRequiresVerifiedDispatch guards
// against re-selecting the STAGE-derived role token (analyst/coder)
// before the dispatch has actually been authenticated. A forged
// dispatch that spoofs the pipeline source (via an overridden
// CI_API_V4_URL) must not walk away with a higher-privilege credential
// than the poller bootstrap token just because the HMAC check was
// skipped rather than passed.
func TestGitLabAgentTemplateStageReselectRequiresVerifiedDispatch(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)

	// An explicit flag, set only on a successful HMAC check, gates the
	// re-select — not merely "the HMAC block didn't error".
	assert.Contains(t, s, "DISPATCH_VERIFIED=false")
	assert.Contains(t, s, "DISPATCH_VERIFIED=true")

	verifiedIdx := strings.Index(s, "DISPATCH_VERIFIED=true")
	require.NotEqual(t, -1, verifiedIdx)
	reselectGateIdx := strings.Index(s, `if [ "${DISPATCH_VERIFIED}" = "true" ]`)
	require.NotEqual(t, -1, reselectGateIdx, "role reselect must be gated on DISPATCH_VERIFIED")
	assert.Greater(t, reselectGateIdx, verifiedIdx,
		"DISPATCH_VERIFIED must be computed before the reselect gate reads it")

	// The reselect (FULLSEND_JOB_KIND=agent / FULLSEND_JOB_AGENT=STAGE)
	// must be inside the DISPATCH_VERIFIED-gated branch, not unconditional.
	stageReselectIdx := strings.Index(s, `FULLSEND_JOB_AGENT="${STAGE:-}"`)
	require.NotEqual(t, -1, stageReselectIdx)
	assert.Greater(t, stageReselectIdx, reselectGateIdx,
		"STAGE-derived reselect must come after the DISPATCH_VERIFIED gate")

	// A missing FULLSEND_DISPATCH_SECRET in migrating/enforced mode must
	// fail closed, not silently skip verification (the pre-fix behavior).
	assert.Contains(t, s, "ROLE_AWARE")
	assert.Contains(t, s, "FULLSEND_GITLAB_ROLE_MIGRATION")
	assert.Contains(t, s, "FULLSEND_DISPATCH_SECRET is not configured")
	assert.NotContains(t, s, "verification is skipped (backward compat during migration)")

	// An unverified STAGE in role-aware mode must abort the job outright
	// (fail closed) rather than merely continuing on the poller bootstrap
	// token — the CLI's own role selection (gitlabroles.SelectAgent) reads
	// STAGE straight from the process environment and does not consult the
	// shell-local DISPATCH_VERIFIED flag, so continuing on the poller
	// credential at the shell level does not stop a later STAGE-derived
	// re-select from happening anyway.
	abortConditionIdx := strings.Index(s, `if [ "${ROLE_AWARE}" = "true" ] && [ "${DISPATCH_VERIFIED}" != "true" ]`)
	require.NotEqual(t, -1, abortConditionIdx, "fail-closed abort must check both ROLE_AWARE and DISPATCH_VERIFIED")
	assert.Greater(t, abortConditionIdx, verifiedIdx,
		"fail-closed abort must be checked after DISPATCH_VERIFIED has been computed")

	assert.Contains(t, s, "STAGE could not be cryptographically verified")
	abortErrIdx := strings.Index(s, "STAGE could not be cryptographically verified")
	assert.Greater(t, abortErrIdx, abortConditionIdx,
		"fail-closed error message must follow the fail-closed condition")

	// The abort must actually exit the job, and must do so before either
	// the STAGE-derived reselect gate or the reselect itself runs.
	require.Less(t, abortConditionIdx, reselectGateIdx,
		"fail-closed abort must precede the STAGE reselect gate")
	require.Less(t, abortConditionIdx, stageReselectIdx,
		"fail-closed abort must precede the STAGE-derived reselect")
	abortBlock := s[abortConditionIdx:reselectGateIdx]
	assert.Contains(t, abortBlock, "exit 1",
		"fail-closed condition must actually abort the job, not just log")
}

func TestGitLabAgentTemplateFixReviewBodyPreFetch(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	// Fix stage exports REVIEW_BODY_FILE for the fix harness
	assert.Contains(t, s, "REVIEW_BODY_FILE")
	assert.Contains(t, s, `"fix"`)
	// Uses the review-agent marker to find the review note
	assert.Contains(t, s, "fullsend:review-agent")
	// Validates size (1 MB limit, matching GitHub reusable-fix.yml)
	assert.Contains(t, s, "1048576")
	// Bot-triggered runs require non-empty review body (checks is_bot, not PIPELINE_SOURCE)
	assert.Contains(t, s, "Bot-triggered run but review body is empty")
	assert.Contains(t, s, "_IS_BOT_TRIGGER")
	// Uses BOT_USER_ID (numeric ID) for author verification (#5550)
	assert.Contains(t, s, "BOT_USER_ID")
	assert.Contains(t, s, "BOT_ID")
	// Paginates through MR notes (same pattern as review pre-fetch)
	assert.Contains(t, s, "notes?sort=desc&per_page=100")

	// Defense-in-depth author mismatch logs a warning (not silent)
	assert.Contains(t, s, "does not match bot")
	// Fix-stage environment variables (parallel to reusable-fix.yml)
	assert.Contains(t, s, "export TARGET_BRANCH")
	assert.Contains(t, s, "export TRIGGER_SOURCE")
	assert.Contains(t, s, "export HUMAN_INSTRUCTION")
	assert.Contains(t, s, "export FIX_ITERATION")
	assert.Contains(t, s, "export PRE_AGENT_HEAD")
	// Trigger source resolves from event payload (bot vs human)
	assert.Contains(t, s, "EVENT_PAYLOAD_B64")
	assert.Contains(t, s, "is_bot")
	assert.Contains(t, s, "note_author_id")
	// Numeric validation on note_author_id before curl URL interpolation
	assert.Contains(t, s, `*[!0-9]*`)
	// Human instruction extracted from /fs-fix note body
	assert.Contains(t, s, "/fs-fix")
	assert.Contains(t, s, "note_body")
	// Fix iteration counts prior fix-agent commits
	assert.Contains(t, s, "fullsend-fix")
	assert.Contains(t, s, "author_name")
	// Pre-agent HEAD recorded before agent runs
	assert.Contains(t, s, "git rev-parse HEAD")
	// Forge.gitlab env vars for fix post-script — REPO_FULL_NAME is now
	// set by run.go from --status-repo (#6865); PUSH_TOKEN, PUSH_TOKEN_SOURCE,
	// GIT_BOT_EMAIL, MR_NUMBER, and GITLAB_MR_URL are in the shared
	// code|fix|review block (#6865).
	assert.NotContains(t, s[strings.Index(s, `"fix"`):], "export REPO_FULL_NAME")
	// MR_NUMBER and GITLAB_MR_URL should NOT be in the fix-only block —
	// they moved to the shared block.
	fixOnlyMarker := `if [ "${STAGE}" = "fix" ]; then`
	fixOnlyIdx := strings.Index(s, fixOnlyMarker)
	require.NotEqual(t, -1, fixOnlyIdx, "fix-only block marker not found")
	fixBlock := s[fixOnlyIdx:]
	runIdx := strings.Index(fixBlock, "fullsend run")
	require.NotEqual(t, -1, runIdx, "fullsend run not found after fix block")
	fixBlock = fixBlock[:runIdx]
	assert.NotContains(t, fixBlock, "export MR_NUMBER")
	assert.NotContains(t, fixBlock, "export GITLAB_MR_URL")
}

// TestGitLabAgentTemplateFixStageReviewNoteUsesAnalystIdentity guards
// against the fix-stage review-note lookup resolving BOT_ID from this
// stage's own (coder) identity or from the discarded poller identity.
// Review notes are always authored by the analyst identity (STAGE=review
// maps to analyst), so the fix stage must temporarily re-select the
// analyst credential for this one lookup and restore its own
// FULLSEND_JOB_TOKEN before GITLAB_TOKEN/PUSH_TOKEN and the rest of the
// job run.
func TestGitLabAgentTemplateFixStageReviewNoteUsesAnalystIdentity(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)

	fixOnlyMarker := `if [ "${STAGE}" = "fix" ]; then`
	fixOnlyIdx := strings.Index(s, fixOnlyMarker)
	require.NotEqual(t, -1, fixOnlyIdx, "fix-only block marker not found")
	fixBlock := s[fixOnlyIdx:]
	runIdx := strings.Index(fixBlock, "fullsend run")
	require.NotEqual(t, -1, runIdx, "fullsend run not found after fix block")
	fixBlock = fixBlock[:runIdx]

	// The fix stage resolves the analyst identity (FULLSEND_JOB_AGENT=review)
	// specifically for the review-note author lookup, not its own STAGE.
	saveIdx := strings.Index(fixBlock, `_FIX_STAGE_JOB_TOKEN="${FULLSEND_JOB_TOKEN}"`)
	require.NotEqual(t, -1, saveIdx, "fix stage must save its own token before switching identity")
	agentReviewIdx := strings.Index(fixBlock, "FULLSEND_JOB_AGENT=review")
	require.NotEqual(t, -1, agentReviewIdx, "fix stage must select the analyst (review) identity for BOT_ID")
	assert.Greater(t, agentReviewIdx, saveIdx,
		"own token must be saved before switching to the analyst identity")

	restoreIdx := strings.Index(fixBlock, `export FULLSEND_JOB_TOKEN="${_FIX_STAGE_JOB_TOKEN}"`)
	require.NotEqual(t, -1, restoreIdx, "fix stage must restore its own token after the analyst lookup")
	assert.Greater(t, restoreIdx, agentReviewIdx,
		"own token must be restored after the analyst identity is used")

	// The restore must happen before the TARGET_BRANCH lookup (later in
	// the same fix-only block), which authenticates with FULLSEND_JOB_TOKEN
	// and must use the restored coder token, not the analyst token.
	targetBranchIdx := strings.Index(fixBlock, "TARGET_BRANCH=$(curl")
	require.NotEqual(t, -1, targetBranchIdx, "TARGET_BRANCH lookup not found in fix block")
	assert.Greater(t, targetBranchIdx, restoreIdx,
		"TARGET_BRANCH lookup must run after the coder token is restored")
}

// TestGitLabAgentTemplateSharedCodeFixEnvVars verifies that PUSH_TOKEN,
// PUSH_TOKEN_SOURCE, GIT_BOT_EMAIL, MR_NUMBER, and GITLAB_MR_URL are
// exported in the shared code|fix|review block so all three stages
// receive them (#6865).
func TestGitLabAgentTemplateSharedCodeFixEnvVars(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)

	// Shared block guards on code, fix, or review
	assert.Contains(t, s, `"${STAGE}" = "code"`)
	assert.Contains(t, s, `"${STAGE}" = "fix"`)
	assert.Contains(t, s, `"${STAGE}" = "review"`)

	// PUSH_TOKEN, PUSH_TOKEN_SOURCE, GIT_BOT_EMAIL are in the shared block
	assert.Contains(t, s, `export PUSH_TOKEN="${FULLSEND_JOB_TOKEN}"`)
	assert.Contains(t, s, "export PUSH_TOKEN_SOURCE")
	assert.Contains(t, s, "export GIT_BOT_EMAIL")
	assert.Contains(t, s, `export GITLAB_TOKEN="${FULLSEND_JOB_TOKEN}"`)

	// MR_NUMBER and GITLAB_MR_URL are in the shared block
	assert.Contains(t, s, "export MR_NUMBER")
	assert.Contains(t, s, "export GITLAB_MR_URL")

	// Bot username resolution for GIT_BOT_EMAIL (reuses BOT_RESPONSE
	// from bot identity verification when available)
	assert.Contains(t, s, "_BOT_USERNAME")
	assert.Contains(t, s, "BOT_RESPONSE")

	// REPO_FULL_NAME is NOT exported in the scaffold — run.go sets it
	// from --status-repo (#6865). Verify it does not appear as an
	// export outside the fullsend run invocation line.
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "export REPO_FULL_NAME") {
			t.Error("REPO_FULL_NAME should not be exported in the scaffold — run.go sets it from --status-repo")
		}
	}
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
	assert.Contains(t, s, "DEFAULT_BRANCH_SHA")
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
	// Fork check uses IS_FORK variable, not jq-parsing the event payload.
	// EVENT_PAYLOAD_B64 is referenced in the HMAC signed message (not for fork detection).
	assert.NotRegexp(t, `jq.*EVENT_PAYLOAD`, s)
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
	// ACTOR_ID fallback to MR_AUTHOR_ID for backward compatibility
	assert.Contains(t, s, "ACTOR_ID")
	assert.Contains(t, s, "MR_AUTHOR_ID")
	// Distinct warning when no actor identity is available
	assert.Contains(t, s, "No actor identity available")
	// Read-only stages exempt from Developer-access gate
	assert.Contains(t, s, `"retro"`)
	assert.Contains(t, s, `"prioritize"`)
}

func TestGitLabAgentTemplateCredentialValidation(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "CI_DEBUG_TRACE")
	assert.Contains(t, s, "select-gitlab-role-token.sh")
	assert.Contains(t, s, "FULLSEND_JOB_KIND=agent")
	// Inference WIF setup is unconditional when FULLSEND_GCP_WIF_PROVIDER is set
	assert.Contains(t, s, "FULLSEND_GCP_WIF_PROVIDER")
}

func TestGitLabPollContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-poll.yml")
	require.NoError(t, err)
	s := string(content)
	assert.Contains(t, s, "# fullsend-stage: poll")
	assert.Contains(t, s, "fullsend poll")
	assert.Contains(t, s, "schedule")
	assert.Contains(t, s, "CI_COMMIT_REF_PROTECTED")
	// Credential selection uses the shared role-token helper.
	assert.Contains(t, s, "select-gitlab-role-token.sh")
	assert.Contains(t, s, "FULLSEND_JOB_KIND=poller")
	assert.Contains(t, s, `PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}`)
	assert.NotContains(t, s, `PRIVATE-TOKEN: ${FULLSEND_FORGE_TOKEN}`)
	// Defaults to CI_SERVER_URL, not hardcoded gitlab.com
	assert.Contains(t, s, "CI_SERVER_URL")
	assert.NotContains(t, s, "https://gitlab.com")
	// ENTRYPOINT override for runner image
	assert.Contains(t, s, `entrypoint: [""]`)
	// No bridge job — poller dispatches pipelines directly via API
	assert.NotContains(t, s, "dispatch-agents")
	assert.NotContains(t, s, "trigger:")
	assert.NotContains(t, s, "child-pipeline.yml")
	assert.NotContains(t, s, "generate-child-pipeline")
	assert.NotContains(t, s, "dispatches.json")
	// No dotenv gating
	assert.NotContains(t, s, "dispatch.env")
	assert.NotContains(t, s, "HAS_DISPATCHES")
	// Runtime CLI install via before_script (#6445)
	assert.Contains(t, s, "before_script:")
	assert.Contains(t, s, "__FULLSEND_VERSION__")
	assert.Contains(t, s, "fullsend-ai/fullsend")
	assert.Contains(t, s, "fullsend --version")
	assert.Contains(t, s, "checksums.txt")
	assert.Contains(t, s, "sha256sum -c")
	assert.Contains(t, s, "releases/latest")
	// Private-CA trust from CI_SERVER_TLS_CA_FILE before GitLab network ops.
	assert.Contains(t, s, "trust-ci-server-ca.sh")
	// Source-build path sets GOPATH and GOCACHE so go build works on
	// non-root runners where /root/go is not writable (#6477).
	assert.Contains(t, s, `export GOPATH="${RUNNER_TEMP:-/tmp}/go"`)
	assert.Contains(t, s, `export GOCACHE="${RUNNER_TEMP:-/tmp}/go-cache"`)
	// tar extraction uses --no-same-owner for non-root containers (#6720)
	assert.Contains(t, s, "tar --no-same-owner")
	assert.NotContains(t, s, "tar xzf /tmp/fullsend.tar.gz")
}

// TestGitLabPollBlanksSiblingRoleSecrets guards against the poller job
// leaving higher-privileged analyst/coder PATs (or a custom
// FULLSEND_GITLAB_ROLE_*_TOKEN, or FULLSEND_FORGE_TOKEN once unused) sitting
// in the process environment for its full lifetime after
// select-gitlab-role-token.sh selects the poller credential. Mirrors
// clearSiblingGitLabRoleSecrets in internal/cli/gitlab_role.go, applied
// directly in the template rather than in the shared helper script (the
// agent template still needs those siblings present until its own HMAC
// reselect and the STAGE=fix analyst-identity lookup).
func TestGitLabPollBlanksSiblingRoleSecrets(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-poll.yml")
	require.NoError(t, err)
	s := string(content)

	selectIdx := strings.Index(s, "select-gitlab-role-token.sh")
	require.NotEqual(t, -1, selectIdx, "expected select-gitlab-role-token.sh to be sourced")
	unsetIdx := strings.Index(s, "unset \"${_fs_sibling}\"")
	require.NotEqual(t, -1, unsetIdx, "expected sibling-secret unset loop")
	pollIdx := strings.Index(s, "fullsend poll \\")
	require.NotEqual(t, -1, pollIdx, "expected fullsend poll invocation")

	assert.Less(t, selectIdx, unsetIdx, "sibling secrets must be blanked after role selection")
	assert.Less(t, unsetIdx, pollIdx, "sibling secrets must be blanked before running fullsend poll")

	// Only unset for migrating/enforced; disabled/rollback share one token
	// across every role so there is nothing to blank.
	assert.Contains(t, s, "migrating|enforced")
	// Covers the builtin roles, any custom registered role, and the
	// shared fallback token — but never the credential this job selected.
	assert.Contains(t, s, "FULLSEND_(GITLAB_(ANALYST|CODER|POLLER|ROLE_[A-Z0-9_]+)_TOKEN|FORGE_TOKEN)")
	assert.Contains(t, s, `"${_fs_sibling}" != "${FULLSEND_JOB_TOKEN_NAME:-}"`)
}

func TestGitLabRootPipelineContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab-ci.yml")
	require.NoError(t, err)
	s := string(content)
	// Root file now includes only the pipeline wrapper and workflow block.
	// Stage declarations and component includes are in fullsend-pipeline.yml.
	assert.Contains(t, s, "fullsend-pipeline.yml",
		"root must include the fullsend pipeline wrapper")
	assert.NotContains(t, s, "stages:",
		"stages moved to fullsend-pipeline.yml")
	assert.NotContains(t, s, "fullsend-dispatch.yml",
		"component includes moved to fullsend-pipeline.yml")
	assert.NotContains(t, s, "fullsend-poll.yml",
		"component includes moved to fullsend-pipeline.yml")
	assert.NotContains(t, s, "fullsend-agent.yml",
		"component includes moved to fullsend-pipeline.yml")
	// Auto-cancel disabled to prevent queued agent pipelines from being killed
	assert.Contains(t, s, "auto_cancel")
	assert.Contains(t, s, "on_new_commit: none")
	// API-triggered pipeline rule for cron-poller dispatched pipelines
	// Requires API source + protected branch + STAGE variable
	assert.Contains(t, s, `$CI_PIPELINE_SOURCE == "api"`)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "merge_request_event"`,
		"native MR dispatch was removed in #7322")
	assert.NotContains(t, s, `$STAGE != ""`)
	// push pipelines intentionally excluded — documented in workflow comment
	assert.Contains(t, s, "Push-triggered pipelines are intentionally excluded")
	// no catch-all rule — workflow:rules is open-ended so adopters
	// can add push rules without needing to remove a never gate
	assert.NotContains(t, s, "- when: never")
	// parent_pipeline rule removed (child pipelines don't inherit workflow:rules)
	assert.NotContains(t, s, `$CI_PIPELINE_SOURCE == "parent_pipeline"`)
}

func TestGitLabPipelineWrapperContent(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-pipeline.yml")
	require.NoError(t, err)
	s := string(content)
	// Pipeline wrapper contains includes and stages moved from root.
	// Native MR dispatch was removed in #7322; the dispatch file is
	// retained on disk as a version-marker carrier but is not included.
	assert.NotContains(t, s, "local: '.gitlab/ci/fullsend-dispatch.yml'")
	assert.Contains(t, s, "fullsend-poll.yml")
	assert.Contains(t, s, "fullsend-agent.yml")
	assert.Contains(t, s, "stages:")
	assert.NotContains(t, s, "- dispatch", "dispatch stage was removed in #7337")
	assert.Contains(t, s, "- poll")
	assert.Contains(t, s, "- agent")
	assert.NotContains(t, s, "- generate")
	// Pipeline wrapper must NOT define a workflow: YAML key (stays in root).
	// The comment text mentions "workflow:" for documentation, so we check
	// for the YAML key pattern rather than the bare string.
	assert.NotContains(t, s, "\nworkflow:",
		"pipeline wrapper must not define workflow: key — it stays in root")
	// Must start with YAML document start marker.
	assert.True(t, strings.HasPrefix(s, "---\n"),
		"must start with YAML document start marker (---)")
}

func TestGitLabRunnerTagsPlaceholder(t *testing.T) {
	taggedFiles := []string{
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/fullsend-agent.yml",
	}
	for _, path := range taggedFiles {
		content, err := GitLabPerRepoFile(path)
		require.NoError(t, err, path)
		assert.Contains(t, string(content), "__RUNNER_TAGS__", "%s must contain __RUNNER_TAGS__ placeholder", path)
	}
}

func TestCollectGitLabPerRepoInstallFiles_WithTags(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles([]string{"docker", "linux"}, "", "")
	require.NoError(t, err)

	for _, f := range files {
		if strings.HasSuffix(f.Path, ".yml") {
			s := string(f.Content)
			assert.NotContains(t, s, "__RUNNER_TAGS__", "%s should have tags substituted", f.Path)
			if strings.Contains(s, "tags:") {
				assert.Contains(t, s, `["docker", "linux"]`, "%s should contain formatted tags", f.Path)
			}
		}
	}
}

func TestCollectGitLabPerRepoInstallFiles_NoTags(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "", "")
	require.NoError(t, err)

	for _, f := range files {
		if strings.HasSuffix(f.Path, ".yml") {
			s := string(f.Content)
			assert.NotContains(t, s, "__RUNNER_TAGS__", "%s should have tags substituted", f.Path)
		}
	}
}

func TestCollectGitLabPerRepoInstallFiles_VersionMarker(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "v0.34.0", "v0.34.0")
	require.NoError(t, err)

	var dispatchContent string
	for _, f := range files {
		if f.Path == ".gitlab/ci/fullsend-dispatch.yml" {
			dispatchContent = string(f.Content)
			break
		}
	}
	require.NotEmpty(t, dispatchContent, "dispatch file should exist")
	assert.Contains(t, dispatchContent, "# fullsend-ref: v0.34.0",
		"dispatch file should contain version marker")
	// Marker must appear after YAML document start marker
	assert.True(t, strings.HasPrefix(dispatchContent, "---\n"),
		"dispatch file must start with YAML document start marker")
	idx := strings.Index(dispatchContent, "# fullsend-ref: v0.34.0")
	assert.Greater(t, idx, 0, "version marker should appear after ---")

	// Other files should NOT contain the version marker
	for _, f := range files {
		if f.Path != ".gitlab/ci/fullsend-dispatch.yml" {
			assert.NotContains(t, string(f.Content), "fullsend-ref:",
				"%s should not contain version marker", f.Path)
		}
	}
}

func TestCollectGitLabPerRepoInstallFiles_NoVersionMarkerWhenEmpty(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "", "")
	require.NoError(t, err)

	for _, f := range files {
		assert.NotContains(t, string(f.Content), "fullsend-ref:",
			"%s should not contain version marker when ref is empty", f.Path)
	}
}

func TestCollectGitLabPerRepoInstallFiles_SHAWithTagAnnotation(t *testing.T) {
	// When both ref (SHA) and tag differ, marker includes both
	files, err := CollectGitLabPerRepoInstallFiles(nil, "abc123def", "v0.35.0")
	require.NoError(t, err)

	for _, f := range files {
		if f.Path == ".gitlab/ci/fullsend-dispatch.yml" {
			s := string(f.Content)
			assert.Contains(t, s, "# fullsend-ref: abc123def (v0.35.0)",
				"version marker should include SHA with tag annotation")
			return
		}
	}
	t.Fatal("dispatch file not found in collected files")
}

func TestCollectGitLabPerRepoInstallFiles_RefUsedWhenNoTag(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "v0.34.0", "")
	require.NoError(t, err)

	for _, f := range files {
		if f.Path == ".gitlab/ci/fullsend-dispatch.yml" {
			assert.Contains(t, string(f.Content), "# fullsend-ref: v0.34.0",
				"version marker should use ref when tag is empty")
			return
		}
	}
	t.Fatal("dispatch file not found in collected files")
}

func TestFormatRunnerTags(t *testing.T) {
	assert.Equal(t, "[]", FormatRunnerTags(nil))
	assert.Equal(t, "[]", FormatRunnerTags([]string{}))
	assert.Equal(t, `["docker"]`, FormatRunnerTags([]string{"docker"}))
	assert.Equal(t, `["docker", "linux"]`, FormatRunnerTags([]string{"docker", "linux"}))
}

func TestFormatVersionMarker(t *testing.T) {
	assert.Equal(t, "", FormatVersionMarker("", ""))
	assert.Equal(t, "# fullsend-ref: v0.34.0", FormatVersionMarker("v0.34.0", ""))
	assert.Equal(t, "# fullsend-ref: v0.34.0", FormatVersionMarker("", "v0.34.0"))
	assert.Equal(t, "# fullsend-ref: abc123 (v0.35.0)", FormatVersionMarker("abc123", "v0.35.0"))
	assert.Equal(t, "# fullsend-ref: abc123", FormatVersionMarker("abc123", "abc123"))
}

func TestResolveFullsendVersion(t *testing.T) {
	assert.Equal(t, "latest", ResolveFullsendVersion("", ""))
	assert.Equal(t, "v0.42.0", ResolveFullsendVersion("abc123", "v0.42.0"))
	assert.Equal(t, "v0.42.0", ResolveFullsendVersion("", "v0.42.0"))
	assert.Equal(t, "abc123", ResolveFullsendVersion("abc123", ""))
}

func TestCollectGitLabPerRepoInstallFiles_VersionPlaceholderReplaced(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "abc123def", "v0.42.0")
	require.NoError(t, err)

	for _, f := range files {
		s := string(f.Content)
		assert.NotContains(t, s, "__FULLSEND_VERSION__",
			"%s should have __FULLSEND_VERSION__ replaced", f.Path)
	}

	// Agent and poll templates should contain the rendered version
	for _, f := range files {
		if f.Path == ".gitlab/ci/fullsend-agent.yml" || f.Path == ".gitlab/ci/fullsend-poll.yml" {
			s := string(f.Content)
			assert.Contains(t, s, `FULLSEND_VERSION="v0.42.0"`,
				"%s should contain the rendered version tag", f.Path)
		}
	}
}

func TestCollectGitLabPerRepoInstallFiles_SHAFallbackWhenNoTag(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "abc123def", "")
	require.NoError(t, err)

	for _, f := range files {
		if f.Path == ".gitlab/ci/fullsend-agent.yml" || f.Path == ".gitlab/ci/fullsend-poll.yml" {
			s := string(f.Content)
			assert.Contains(t, s, `FULLSEND_VERSION="abc123def"`,
				"%s should contain the SHA when no tag is available", f.Path)
		}
	}
}

func TestCollectGitLabPerRepoInstallFiles_LatestWhenNoVersion(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "", "")
	require.NoError(t, err)

	for _, f := range files {
		if f.Path == ".gitlab/ci/fullsend-agent.yml" || f.Path == ".gitlab/ci/fullsend-poll.yml" {
			s := string(f.Content)
			assert.Contains(t, s, `FULLSEND_VERSION="latest"`,
				"%s should fall back to latest when no ref/tag provided", f.Path)
		}
	}
}

func TestInsertAfterDocStart(t *testing.T) {
	t.Run("with document start", func(t *testing.T) {
		result := InsertAfterDocStart("---\ncontent", "# marker")
		assert.Equal(t, "---\n# marker\ncontent", result)
	})
	t.Run("without document start", func(t *testing.T) {
		result := InsertAfterDocStart("content", "# marker")
		assert.Equal(t, "# marker\ncontent", result)
	})
}

func TestGitLabAgentTemplateRunnerTempBeforeRun(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)

	exportIdx := strings.Index(s, "export RUNNER_TEMP=")
	require.Greater(t, exportIdx, 0, "RUNNER_TEMP export must exist")

	runIdx := strings.Index(s, "fullsend run")
	require.Greater(t, runIdx, 0, "fullsend run must exist")

	assert.Less(t, exportIdx, runIdx,
		"RUNNER_TEMP must be exported before fullsend run is invoked")
}

// TestGitLabAgentTemplateHarnessPassthroughVars validates that harness
// passthrough variables declared in the GitHub reusable workflows are also
// present in the GitLab agent template's variables: section. When a harness
// YAML uses ${VAR} passthrough syntax, the harness engine's os.Expand rejects
// unset variables. GitHub workflows set these to " in their env: blocks; the
// GitLab template must do the same or the agent aborts at env validation (#6273).
func TestGitLabAgentTemplateHarnessPassthroughVars(t *testing.T) {
	// Variables that GitHub reusable workflows set for harness passthrough.
	// When adding a new ${VAR} passthrough to a multi-forge harness, add it
	// here so the test catches a missing GitLab declaration.
	passthroughVars := []string{
		"CODE_ALLOWED_TARGET_BRANCHES",
	}

	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)

	for _, v := range passthroughVars {
		assert.Contains(t, s, v,
			"GitLab agent template must declare %s in variables: section — "+
				"harness env.runner uses ${%s} passthrough which fails on unset vars (#6273)", v, v)
	}
}

func TestGitLabTemplatesSourceRoleTokenHelper(t *testing.T) {
	for _, path := range []string{
		".gitlab/ci/fullsend-poll.yml",
		".gitlab/ci/fullsend-agent.yml",
	} {
		content, err := GitLabPerRepoFile(path)
		require.NoError(t, err, path)
		s := string(content)
		assert.Contains(t, s, "select-gitlab-role-token.sh", path)
		assert.Contains(t, s, `PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}`, path)
		assert.NotContains(t, s, `PRIVATE-TOKEN: ${FULLSEND_FORGE_TOKEN}`, path)
		assert.NotContains(t, s, `export GITLAB_TOKEN="${FULLSEND_FORGE_TOKEN}"`, path)
		assert.NotContains(t, s, `export PUSH_TOKEN="${FULLSEND_FORGE_TOKEN}"`, path)
	}
}

func TestCollectGitLabPerRepoInstallFiles_IncludesRoleTokenScript(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, "", "")
	require.NoError(t, err)
	var found bool
	for _, f := range files {
		if f.Path == ".gitlab/ci/scripts/select-gitlab-role-token.sh" {
			found = true
			assert.Contains(t, string(f.Content), "FULLSEND_GITLAB_POLLER_TOKEN")
			assert.Contains(t, string(f.Content), "FULLSEND_GITLAB_ANALYST_TOKEN")
			assert.Contains(t, string(f.Content), "FULLSEND_GITLAB_CODER_TOKEN")
			break
		}
	}
	assert.True(t, found, "install files must include select-gitlab-role-token.sh")
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

// TestGitLabAgentTemplateExportsGoogleCloudProject guards the Vertex ADC
// contract for Pi's google-vertex (Gemini) provider. GitHub Actions gets
// GOOGLE_CLOUD_PROJECT from google-github-actions/auth; the GitLab scaffold
// does a manual WIF exchange and must export it itself. Claude-on-Vertex
// still uses ANTHROPIC_VERTEX_PROJECT_ID; Pi only maps that onto
// GOOGLE_CLOUD_PROJECT on the anthropic-vertex path (#7577).
func TestGitLabAgentTemplateExportsGoogleCloudProject(t *testing.T) {
	content, err := GitLabPerRepoFile(".gitlab/ci/fullsend-agent.yml")
	require.NoError(t, err)
	s := string(content)

	assert.Contains(t, s, `export GOOGLE_CLOUD_PROJECT="${FULLSEND_GCP_PROJECT_ID}"`)
	assert.Contains(t, s, `export ANTHROPIC_VERTEX_PROJECT_ID="${FULLSEND_GCP_PROJECT_ID}"`)
	assert.Contains(t, s, `export GOOGLE_APPLICATION_CREDENTIALS="${GCP_CRED_CONFIG_FILE}"`)
	assert.Contains(t, s, `export CLOUD_ML_REGION="${FULLSEND_GCP_REGION}"`)

	projectIdx := strings.Index(s, `export GOOGLE_CLOUD_PROJECT="${FULLSEND_GCP_PROJECT_ID}"`)
	runIdx := strings.Index(s, `fullsend run "${STAGE}"`)
	require.Greater(t, projectIdx, 0, "GOOGLE_CLOUD_PROJECT export must exist")
	require.Greater(t, runIdx, 0, "fullsend run must exist")
	assert.Less(t, projectIdx, runIdx,
		"GOOGLE_CLOUD_PROJECT must be exported before fullsend run is invoked")
}
