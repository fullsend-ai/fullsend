package scaffold

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeGitLabScript(t *testing.T, dir, relPath string) string {
	t.Helper()
	content, err := GitLabPerRepoFile(relPath)
	require.NoError(t, err)
	require.NotEmpty(t, content)
	path := filepath.Join(dir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, content, 0o644))
	return path
}

// setupPinnedConfigOrigin makes root a git checkout with an "origin"
// remote on branch "main", so run-agent-job.sh's trusted-config fetch
// (`git fetch origin "${FULLSEND_PINNED_REF}" --depth=1`) succeeds.
// That fetch is unconditional (fail-closed on FULLSEND_PINNED_REF, not
// gated on the overridable CI_DEFAULT_BRANCH), so any run-agent-job.sh
// test whose STAGE reaches past HMAC dispatch verification needs this,
// even when the test has nothing to do with the trusted-config content
// itself. Callers that do care about the config content should commit
// their own .fullsend/config.yaml into the upstream repo instead of
// calling this helper directly.
func setupPinnedConfigOrigin(t *testing.T, root string) {
	t.Helper()
	upstream := t.TempDir()
	initGitRepo(t, upstream)
	commitFile(t, upstream, ".fullsend/config.yaml", "kill_switch: false\n", "trusted config")
	initGitRepo(t, root)
	gitCmd(t, root, "remote", "add", "origin", upstream)
}

func TestRunPollJobScript_RejectsInvalidPollMode(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"schedule"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_POLL_MODE=bogus",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=https://gitlab.example",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "FULLSEND_POLL_MODE must be 'slash' or 'events'")
}

func TestRunPollJobScript_DebugTraceAborts(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
}

// TestRunPollJobScript_DebugTraceAbortsOnTruthyVariants covers non-"true"
// values gitlab-runner also treats as enabling debug tracing (Go
// strconv.ParseBool accepts "1", "t"/"T", "true"/"TRUE"/"True"). An
// exact-match guard would miss these and let a debug-trace pipeline dump
// secrets at job init.
func TestRunPollJobScript_DebugTraceAbortsOnTruthyVariants(t *testing.T) {
	for _, v := range []string{"1", "TRUE", "T"} {
		t.Run(v, func(t *testing.T) {
			root := t.TempDir()
			writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
			script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

			cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
			cmd.Env = []string{
				"SCRIPT=" + script,
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
				"CI_PROJECT_DIR=" + root,
				"CI_DEBUG_TRACE=" + v,
				"FULLSEND_FORGE_TOKEN=shared-pat",
			}
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
		})
	}
}

// TestRunAgentJobScript_DebugTraceAbortsOnTruthyVariants is the
// run-agent-job.sh counterpart of TestRunPollJobScript_DebugTraceAbortsOnTruthyVariants.
func TestRunAgentJobScript_DebugTraceAbortsOnTruthyVariants(t *testing.T) {
	for _, v := range []string{"1", "TRUE", "T"} {
		t.Run(v, func(t *testing.T) {
			root := t.TempDir()
			writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
			script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

			cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
			cmd.Env = []string{
				"SCRIPT=" + script,
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
				"CI_PROJECT_DIR=" + root,
				"CI_DEBUG_TRACE=" + v,
				"FULLSEND_FORGE_TOKEN=shared-pat",
			}
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
		})
	}
}

func TestRunPollJobScript_BlanksSiblingSecretsBeforePoll(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"schedule"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	bin := t.TempDir()
	writeStub := func(name, body string) {
		path := filepath.Join(bin, name)
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	}
	writeStub("fullsend", "echo POLL_RAN\nexit 0\n")

	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"; echo ANALYST="${FULLSEND_GITLAB_ANALYST_TOKEN-unset}"; echo CODER="${FULLSEND_GITLAB_CODER_TOKEN-unset}"; echo SHARED="${FULLSEND_FORGE_TOKEN-unset}"; echo JOB="${FULLSEND_JOB_TOKEN-unset}"`)
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poll-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"FULLSEND_POLL_MODE=events",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=https://gitlab.example",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)
	assert.Contains(t, got, "POLL_RAN")
	assert.Contains(t, got, "ANALYST=unset")
	assert.Contains(t, got, "CODER=unset")
	assert.Contains(t, got, "SHARED=unset")
	assert.Contains(t, got, "JOB=poll-pat")
}

func TestRunAgentJobScript_DebugTraceAborts(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
}

// TestRunAgentJobScript_RejectsUserinfoAPIURL proves that a userinfo-bearing
// CI_API_V4_URL is rejected before the identity pin trusts it. run-agent-job.sh
// sources pin-ci-job-identity.sh (gitlabPinCIJobIdentityScriptPath) first, and
// that helper — not run-agent-job.sh itself — is what validates CI_API_V4_URL,
// so the fixture must install it (plus trust-ci-server-ca.sh, which it
// sources) and supply CI_JOB_TOKEN so execution actually reaches that
// validation instead of failing earlier for unrelated reasons.
func TestRunAgentJobScript_RejectsUserinfoAPIURL(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"CI_SERVER_URL=https://gitlab.example",
		"CI_SERVER_HOST=gitlab.example",
		// "${host%:*}" alone strips everything up to the last colon, so
		// this would previously resolve to hostname "gitlab.example" (a
		// match against CI_SERVER_HOST) even though the request actually
		// goes to "attacker.example" — "gitlab.example:443" is HTTP
		// userinfo here, not the host.
		"CI_API_V4_URL=https://gitlab.example:443@attacker.example/api/v4",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_API_V4_URL contains characters not permitted in a GitLab API root")
}

func TestInstallFullsendCLIScript_DebugTraceAborts(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	script := writeGitLabScript(t, root, gitlabInstallCLIScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
}

func TestRunPollJobScript_DeniesNonSchedulePinnedSource(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"api"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_POLL_MODE=events",
		"CI_PIPELINE_SOURCE=schedule",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=https://gitlab.example",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "disjoint allowlist deny")
	assert.NotContains(t, string(out), "fullsend poll")
}

func TestRunAgentJobScript_DeniesParentPipelinePinnedSource(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"parent_pipeline"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"STAGE=triage",
		"CI_PIPELINE_SOURCE=api",
		"CI_PROJECT_ID=1",
		"CI_PIPELINE_ID=99",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "disjoint allowlist deny")
	assert.NotContains(t, string(out), "HMAC")
}

func TestRunAgentJobScript_DebugTraceAbortsBeforePin(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
	assert.NotContains(t, string(out), "CI_JOB_TOKEN job lookup")
}

func TestRunPollJobScript_DebugTraceAbortsBeforePin(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = []string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"CI_DEBUG_TRACE=true",
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_FORGE_TOKEN=shared-pat",
	}
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
	assert.NotContains(t, string(out), "CI_JOB_TOKEN job lookup")
}

// TestRunAgentJobScript_UserlessPipelineRefetchesWithPinnedIDs exercises
// the run-agent-job.sh bot-identity block's PAT re-fetch path (some
// GitLab versions omit .user on the job-token pipeline GET). It also
// exercises the pin-ci-job-identity.sh project/branch/pipeline GET
// routes with a fail-closed-relevant assertion: the re-fetch must key
// off the pinned project/pipeline IDs, never the overridable
// CI_PROJECT_ID / CI_PIPELINE_ID pipeline variables.
func TestRunAgentJobScript_UserlessPipelineRefetchesWithPinnedIDs(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	var sawPinnedRefetch atomic.Bool
	var sawUnpinnedRefetch atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			if strings.Contains(path, "/projects/1/pipelines/99") {
				sawUnpinnedRefetch.Store(true)
			}
			if strings.Contains(path, "/projects/42/pipelines/100") && r.Header.Get("PRIVATE-TOKEN") != "" {
				sawPinnedRefetch.Store(true)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
				return
			}
			// The JOB-TOKEN pin call (and any other pipeline GET) gets the
			// user-less payload that forces the PAT re-fetch below.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"STAGE=triage",
		"RESOURCE_KEY=test-key",
		"CI_PIPELINE_SOURCE=api",
		// Deliberately different from the pinned project/pipeline (42/100)
		// so a passing assertion below proves the re-fetch used the pinned
		// IDs, not these overridable CI variables.
		"CI_PROJECT_ID=1",
		"CI_PIPELINE_ID=99",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "FULLSEND_DISPATCH_SECRET is not configured")
	assert.True(t, sawPinnedRefetch.Load(), "PAT re-fetch must use the pinned project/pipeline IDs")
	assert.False(t, sawUnpinnedRefetch.Load(), "PAT re-fetch must not use the overridable CI_PROJECT_ID/CI_PIPELINE_ID")
}

// computeDispatchHMAC builds the HMAC run-agent-job.sh's dispatch
// verification checks, matching the field order and printf format of
// HMAC_MESSAGE in run-agent-job.sh.
func computeDispatchHMAC(secret string, actorID, eventPayloadB64, eventType, pollJobURL, isFork, mrAuthorID, originatingURL, repoFullName, resourceKey, stage, statusIID string) string {
	message := strings.Join([]string{
		"ACTOR_ID=" + actorID,
		"EVENT_PAYLOAD_B64=" + eventPayloadB64,
		"EVENT_TYPE=" + eventType,
		"FULLSEND_POLL_JOB_URL=" + pollJobURL,
		"IS_FORK=" + isFork,
		"MR_AUTHOR_ID=" + mrAuthorID,
		"ORIGINATING_URL=" + originatingURL,
		"REPO_FULL_NAME=" + repoFullName,
		"RESOURCE_KEY=" + resourceKey,
		"STAGE=" + stage,
		"STATUS_IID=" + statusIID,
	}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// TestRunAgentJobScript_UsesPinnedProjectIDForPATCalls is a regression
// test for the secret-exposure finding: after a successful identity pin,
// project-scoped PAT-bearing calls must use FULLSEND_PINNED_PROJECT_ID
// (from the CI_JOB_TOKEN job record), never the overridable CI_PROJECT_ID
// pipeline variable. Exercises the resource-group self-heal PUT (runs
// pre-HMAC-verification, on the poller PAT) and the Developer-access
// members lookup (runs post-HMAC-verification, on the role PAT) — the
// two call sites reachable without also standing up the fix-stage MR
// checkout (checkout-mr-source.sh).
func TestRunAgentJobScript_UsesPinnedProjectIDForPATCalls(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	var sawPinnedResourceGroup, sawUnpinnedResourceGroup atomic.Bool
	var sawPinnedMembers, sawUnpinnedMembers atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			// Includes .user so the bot-identity block does not need the
			// userless-pipeline PAT re-fetch (covered by a separate test).
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
		case strings.Contains(path, "/resource_groups/"):
			if strings.Contains(path, "/projects/1/resource_groups/") {
				sawUnpinnedResourceGroup.Store(true)
			}
			if strings.Contains(path, "/projects/42/resource_groups/") {
				sawPinnedResourceGroup.Store(true)
			}
			w.WriteHeader(http.StatusOK)
		case strings.Contains(path, "/members/all/"):
			if strings.Contains(path, "/projects/1/members/all/") {
				sawUnpinnedMembers.Store(true)
			}
			if strings.Contains(path, "/projects/42/members/all/") {
				sawPinnedMembers.Store(true)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	bin := t.TempDir()
	fullsendStub := filepath.Join(bin, "fullsend")
	require.NoError(t, os.WriteFile(fullsendStub, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	const secret = "test-hmac-secret"
	hmacHex := computeDispatchHMAC(secret, "5", "", "", "", "", "", "", "", "test-key", "triage", "")

	// The trusted-config fetch (CONFIG_YAML/kill-switch) now always runs
	// against FULLSEND_PINNED_REF, so `root` must be a real git checkout
	// with an "origin" remote for `git fetch origin main --depth=1` to
	// succeed.
	setupPinnedConfigOrigin(t, root)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Dir = root
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"STAGE=triage",
		"RESOURCE_KEY=test-key",
		"ACTOR_ID=5",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=https://gitlab.example",
		"CI_PIPELINE_URL=https://gitlab.example/group/project/-/pipelines/100",
		// Deliberately different from the pinned project (42) so a passing
		// assertion below proves these calls used the pinned project id,
		// not this overridable CI variable.
		"CI_PROJECT_ID=1",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)

	assert.True(t, sawPinnedResourceGroup.Load(), "resource-group PUT must use the pinned project id")
	assert.False(t, sawUnpinnedResourceGroup.Load(), "resource-group PUT must not use the overridable CI_PROJECT_ID")
	assert.True(t, sawPinnedMembers.Load(), "members lookup must use the pinned project id")
	assert.False(t, sawUnpinnedMembers.Load(), "members lookup must not use the overridable CI_PROJECT_ID")
}

// TestRunPollJobScript_UsesPinnedIdentityForCLIArgs is a regression test
// for the secret-exposure finding on the `fullsend poll` invocation: after
// a successful identity pin, --project and --gitlab-url must come from
// FULLSEND_PINNED_PROJECT_PATH / FULLSEND_PINNED_GITLAB_URL (the
// CI_JOB_TOKEN job record via pin-ci-job-identity.sh), never the
// overridable CI_PROJECT_PATH / CI_SERVER_URL / FULLSEND_GITLAB_URL
// pipeline variables.
func TestRunPollJobScript_UsesPinnedIdentityForCLIArgs(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"pinned/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"schedule"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte("#!/bin/sh\necho FULLSEND_ARGS: \"$@\"\n"), 0o755))

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_POLL_MODE=events",
		// Deliberately distinct from the pinned project path/API root so a
		// passing assertion below proves the poll CLI args use the pinned
		// identity, not these overridable pipeline variables.
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=unpinned/project",
		"CI_SERVER_URL=https://unpinned.example",
		"FULLSEND_GITLAB_URL=https://also-unpinned.example",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)
	assert.Contains(t, got, "--project pinned/project")
	assert.Contains(t, got, "--gitlab-url "+srv.URL)
	assert.NotContains(t, got, "unpinned/project")
	assert.NotContains(t, got, "unpinned.example")
}

// TestRunPollJobScript_UsesPinnedRefForDispatch is a regression test for
// the runtime-mechanism finding on the `fullsend poll` invocation:
// internal/cli/poll.go's pipelineRef (the ref new dispatched pipelines
// target) comes from CI_COMMIT_REF_NAME, falling back to
// CI_DEFAULT_BRANCH — both overridable pipeline variables, unlike this
// poller job's own identity, which pin-ci-job-identity.sh already pins
// to FULLSEND_PINNED_REF. run-poll-job.sh must export CI_COMMIT_REF_NAME
// from FULLSEND_PINNED_REF before invoking `fullsend poll`, overriding
// whatever CI_COMMIT_REF_NAME/CI_DEFAULT_BRANCH a trigger-influenced
// pipeline/schedule variable set it to.
func TestRunPollJobScript_UsesPinnedRefForDispatch(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunPollJobScriptPath)

	st := &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", "main"),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"pinned/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: `{"id":100,"source":"schedule"}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(
		"#!/bin/sh\necho CI_COMMIT_REF_NAME: \"$CI_COMMIT_REF_NAME\"\n",
	), 0o755))

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_POLL_MODE=events",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=pinned/project",
		// Deliberately distinct from the pinned ref (main) so a passing
		// assertion below proves the dispatch ref `fullsend poll` sees
		// comes from FULLSEND_PINNED_REF, not these overridable pipeline
		// variables — including the poller job's own predefined
		// CI_COMMIT_REF_NAME, which normally matches the pinned ref but
		// is still an overridable variable an attacker-controlled
		// project/schedule variable of the same name could shadow.
		"CI_COMMIT_REF_NAME=attacker-ref",
		"CI_DEFAULT_BRANCH=attacker-ref",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)
	assert.Contains(t, got, "CI_COMMIT_REF_NAME: main")
	assert.NotContains(t, got, "CI_COMMIT_REF_NAME: attacker-ref")
}

// TestRunAgentJobScript_UsesPinnedIdentityForStatusRepo is a regression
// test for the secret-exposure finding on the `fullsend run` invocation:
// after a successful identity pin, --status-repo must come from
// FULLSEND_PINNED_PROJECT_PATH (the CI_JOB_TOKEN job record), never the
// overridable CI_PROJECT_PATH pipeline variable.
func TestRunAgentJobScript_UsesPinnedIdentityForStatusRepo(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
		case strings.Contains(path, "/resource_groups/"):
			w.WriteHeader(http.StatusOK)
		case strings.Contains(path, "/members/all/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"pinned/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte("#!/bin/sh\necho FULLSEND_ARGS: \"$@\"\n"), 0o755))

	const secret = "test-hmac-secret"
	hmacHex := computeDispatchHMAC(secret, "5", "", "", "", "", "", "", "", "test-key", "triage", "")

	// The trusted-config fetch (CONFIG_YAML/kill-switch) now always runs
	// against FULLSEND_PINNED_REF, so `root` must be a real git checkout
	// with an "origin" remote for `git fetch origin main --depth=1` to
	// succeed.
	setupPinnedConfigOrigin(t, root)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Dir = root
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"STAGE=triage",
		"RESOURCE_KEY=test-key",
		"ACTOR_ID=5",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PIPELINE_URL=https://gitlab.example/pinned/project/-/pipelines/999",
		// Deliberately distinct from the pinned project path so a passing
		// assertion below proves --status-repo uses the pinned identity,
		// not the overridable CI_PROJECT_PATH pipeline variable.
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=unpinned/project",
		"CI_SERVER_URL=https://unpinned.example",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)
	assert.Contains(t, got, "--status-repo pinned/project")
	assert.NotContains(t, got, "--status-repo unpinned/project")
	assert.NotContains(t, got, "unpinned.example")
}

// TestRunAgentJobScript_UsesPinnedGitLabURLForFullsendRun is a regression
// test for the secret-exposure finding on the `fullsend run` invocation:
// `fullsend run` has no --gitlab-url flag, so its GitLab client
// (newGitLabClientFromEnv in internal/cli/reconcilestatus.go) resolves
// the API host from FULLSEND_GITLAB_URL, then GITLAB_API_URL, then
// CI_SERVER_URL — all overridable pipeline variables in the same
// outrankable class as CI_PROJECT_ID (ADR 0125). run-agent-job.sh must
// export FULLSEND_GITLAB_URL from FULLSEND_PINNED_GITLAB_URL before
// invoking `fullsend run`, so that resolution never falls through to one
// of those instead.
func TestRunAgentJobScript_UsesPinnedGitLabURLForFullsendRun(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
		case strings.Contains(path, "/resource_groups/"):
			w.WriteHeader(http.StatusOK)
		case strings.Contains(path, "/members/all/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"pinned/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(
		`#!/bin/sh
if [ "$1" = "run" ]; then
  echo "FULLSEND_GITLAB_URL: $FULLSEND_GITLAB_URL"
fi
`), 0o755))

	const secret = "test-hmac-secret"
	hmacHex := computeDispatchHMAC(secret, "5", "", "", "", "", "", "", "", "test-key", "triage", "")

	setupPinnedConfigOrigin(t, root)

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Dir = root
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"STAGE=triage",
		"RESOURCE_KEY=test-key",
		"ACTOR_ID=5",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PIPELINE_URL=https://gitlab.example/pinned/project/-/pipelines/999",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=pinned/project",
		// Deliberately distinct from the pin-validated API root so a
		// passing assertion below proves the `fullsend run` child
		// environment's FULLSEND_GITLAB_URL comes from
		// FULLSEND_PINNED_GITLAB_URL, not these overridable variables.
		"CI_SERVER_URL=https://unpinned.example",
		"GITLAB_API_URL=https://also-unpinned.example",
		"FULLSEND_GITLAB_URL=https://also-unpinned.example",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)
	assert.Contains(t, got, "FULLSEND_GITLAB_URL: "+srv.URL)
	assert.NotContains(t, got, "unpinned.example")
}

// TestRunAgentJobScript_UsesPinnedRefForTrustedConfigAndTargetBranch is a
// regression test for the fail-open finding: run-agent-job.sh's
// trusted-config fetch (kill-switch/role-enablement) and its
// STAGE=fix TARGET_BRANCH fallback must both derive from
// FULLSEND_PINNED_REF (the CI_JOB_TOKEN job record's ref, already
// proven equal to the enrolled protected default branch), never the
// overridable CI_DEFAULT_BRANCH pipeline variable.
//
// The enrolled project has two branches: "main" (the pinned default
// branch, kill_switch: false) and "malicious" (kill_switch: true).
// CI_DEFAULT_BRANCH is set to "malicious". If run-agent-job.sh ever
// again reads CI_DEFAULT_BRANCH instead of FULLSEND_PINNED_REF for the
// trusted-config fetch, it fetches the malicious config, trips the
// kill switch, and the job aborts (require.NoError below fails). A
// third "feature" branch supplies the MR source revision checked out
// by checkout-mr-source.sh.
func TestRunAgentJobScript_UsesPinnedRefForTrustedConfigAndTargetBranch(t *testing.T) {
	// Seed the enrolled project's three branches and a bare "GitLab
	// server" repo, mirroring seedSameProjectOrigin in
	// checkout_mr_source_test.go.
	work := t.TempDir()
	initGitRepo(t, work)
	commitFile(t, work, ".fullsend/config.yaml", "kill_switch: false\n", "trusted config")
	gitCmd(t, work, "checkout", "-b", "malicious")
	commitFile(t, work, ".fullsend/config.yaml", "kill_switch: true\n", "malicious config")
	gitCmd(t, work, "checkout", "-b", "feature", "main")
	sourceSHA := commitFile(t, work, "fix.txt", "fix content\n", "feature commit")

	serverRoot := filepath.Join(t.TempDir(), "gitlab")
	bare := filepath.Join(serverRoot, "group/project.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(t.TempDir(), "runner")
	gitCmd(t, t.TempDir(), "clone", "--depth=1", "--branch", "main", bare, runner)

	writeGitLabScript(t, runner, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, runner, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, runner, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	writeGitLabScript(t, runner, gitlabCheckoutMRSourceScriptPath)
	script := writeGitLabScript(t, runner, gitlabRunAgentJobScriptPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
		case strings.Contains(path, "/resource_groups/"):
			w.WriteHeader(http.StatusOK)
		case strings.Contains(path, "/members/all/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		case strings.Contains(path, "/merge_requests/") && strings.Contains(path, "/notes"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(path, "/merge_requests/") && strings.Contains(path, "/commits"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(path, "/merge_requests/"):
			// No target_branch — forces the fallback under test.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`))
		}
	})
	// checkout-mr-source.sh now fetches the MR source from
	// FULLSEND_PINNED_GITLAB_URL (derived from the pin-validated
	// CI_API_V4_URL, i.e. this same TLS server), not the overridable
	// CI_SERVER_URL — so the bare "group/project.git" repo must be
	// reachable over smart HTTP from this server too, alongside the
	// JSON API mocks above.
	mux.Handle("/group/", &cgi.Handler{
		Path: gitHTTPBackendPath(t),
		Env: []string{
			"GIT_PROJECT_ROOT=" + serverRoot,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH=" + os.Getenv("PATH"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"HOME=" + t.TempDir(),
		},
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	resolvedJSON, err := json.Marshal(map[string]any{
		"source_branch":       "feature",
		"source_sha":          sourceSHA,
		"source_project_path": "group/project",
	})
	require.NoError(t, err)
	resolvedPath := filepath.Join(t.TempDir(), "resolve.json")
	require.NoError(t, os.WriteFile(resolvedPath, resolvedJSON, 0o644))

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(
		`#!/bin/sh
case "$1" in
  resolve-mr-source)
    cat "$RESOLVE_JSON_FILE"
    ;;
  check-protected-branch)
    exit 0
    ;;
  run)
    echo "TARGET_BRANCH: $TARGET_BRANCH"
    ;;
  eval-measure)
    exit 0
    ;;
  *)
    echo "unexpected fullsend subcommand: $1" >&2
    exit 1
    ;;
esac
`), 0o755))

	const secret = "test-hmac-secret"
	hmacHex := computeDispatchHMAC(secret, "5", "", "", "", "false", "", "", "", "test-key", "fix", "7")

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Dir = runner
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + runner,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
		"STAGE=fix",
		"RESOURCE_KEY=test-key",
		"ACTOR_ID=5",
		"STATUS_IID=7",
		"IS_FORK=false",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PROJECT_ID=42",
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=" + serverRoot,
		"CI_PIPELINE_URL=" + serverRoot + "/group/project/-/pipelines/100",
		"RESOLVE_JSON_FILE=" + resolvedPath,
		// The attack this test defends against: an ordinary
		// project/schedule CI/CD variable overriding CI_DEFAULT_BRANCH
		// to a branch that exists (so the fetch itself doesn't just
		// fail) but carries a different trusted-config payload and a
		// different TARGET_BRANCH fallback value than the pinned ref.
		"CI_DEFAULT_BRANCH=malicious",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)
	assert.Contains(t, got, "TARGET_BRANCH: main")
	assert.NotContains(t, got, "malicious")
}

// TestRunAgentJobScript_RejectsDisagreeingMergeRequestTargetBranchName is a
// regression test for the logic-error finding: this job's admit source is
// api-only (parent_pipeline is not an admitted arm — see FULLSEND_ADMIT_SOURCE
// in run-agent-job.sh), so GitLab never natively populates
// CI_MERGE_REQUEST_TARGET_BRANCH_NAME here. A non-empty value is therefore an
// ordinary, overridable project/group/pipeline CI/CD variable — the same
// outrankable class ADR 0125 already establishes for
// CI_PROJECT_ID/CI_DEFAULT_BRANCH. run-agent-job.sh must resolve
// TARGET_BRANCH from the pinned-project MR API (falling back to
// FULLSEND_PINNED_REF) and fail closed when CI_MERGE_REQUEST_TARGET_BRANCH_NAME
// disagrees with that resolved value, instead of trusting it outright.
func TestRunAgentJobScript_RejectsDisagreeingMergeRequestTargetBranchName(t *testing.T) {
	// The failure under test happens before the fix-stage MR source
	// checkout, so only a "main" branch carrying trusted config is
	// needed — no feature branch, resolve-mr-source stub, or git-smart-
	// HTTP backend required.
	work := t.TempDir()
	initGitRepo(t, work)
	commitFile(t, work, ".fullsend/config.yaml", "kill_switch: false\n", "trusted config")

	serverRoot := filepath.Join(t.TempDir(), "gitlab")
	bare := filepath.Join(serverRoot, "group/project.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bare), 0o755))
	gitCmd(t, work, "clone", "--bare", ".", bare)

	runner := filepath.Join(t.TempDir(), "runner")
	gitCmd(t, t.TempDir(), "clone", "--depth=1", "--branch", "main", bare, runner)

	writeGitLabScript(t, runner, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, runner, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, runner, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	writeGitLabScript(t, runner, gitlabCheckoutMRSourceScriptPath)
	script := writeGitLabScript(t, runner, gitlabRunAgentJobScriptPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
		case strings.Contains(path, "/members/all/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		case strings.Contains(path, "/merge_requests/") && strings.Contains(path, "/notes"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case strings.Contains(path, "/merge_requests/"):
			// Pinned-project MR API's target branch: "main" — distinct
			// from the CI_MERGE_REQUEST_TARGET_BRANCH_NAME under test.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"target_branch":"main"}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	const secret = "test-hmac-secret"
	hmacHex := computeDispatchHMAC(secret, "5", "", "", "", "false", "", "", "", "test-key", "fix", "7")

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Dir = runner
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + runner,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
		"STAGE=fix",
		"RESOURCE_KEY=test-key",
		"ACTOR_ID=5",
		"STATUS_IID=7",
		"IS_FORK=false",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PROJECT_ID=42",
		"CI_PROJECT_PATH=group/project",
		"CI_SERVER_URL=" + serverRoot,
		"CI_PIPELINE_URL=" + serverRoot + "/group/project/-/pipelines/100",
		// source=api pipelines never natively populate this variable;
		// a non-empty value here can only come from an ordinary,
		// overridable CI/CD variable, and it disagrees with the
		// pinned-project MR API's target branch ("main") mocked above.
		"CI_MERGE_REQUEST_TARGET_BRANCH_NAME=malicious",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "stdout/stderr: %s", out)
	assert.Contains(t, string(out), "does not match the pinned/API target branch")
}

// TestRunAgentJobScript_IgnoresUnsignedMergeRequestIIDWhenStatusIIDMissing is
// a regression test for the fail-open finding: this job's admit source is
// api-only, so GitLab never natively populates CI_MERGE_REQUEST_IID here —
// any value comes solely from an ordinary, overridable project/group/
// pipeline CI/CD variable. The existing fail-closed check only catches a
// CI_MERGE_REQUEST_IID that *disagrees* with a non-zero, HMAC-signed
// STATUS_IID; when STATUS_IID is empty (the common case for this job, which
// never natively receives it either), the unsigned CI_MERGE_REQUEST_IID must
// not be trusted as a fallback identity source for the GITLAB_ISSUE_URL
// merge-request link, the STAGE=review prior-review Notes API lookup, or the
// shared code|fix|review MR_IID/MR_NUMBER/GITLAB_MR_URL resolution — all
// three must treat the merge request as unidentified (IID "0") instead of
// keying a same-project Notes read or status link on the spoofed value.
func TestRunAgentJobScript_IgnoresUnsignedMergeRequestIIDWhenStatusIIDMissing(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	var sawSpoofedNotesLookup atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
		case strings.Contains(path, "/members/all/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		case strings.Contains(path, "/merge_requests/999/notes"):
			// The spoofed CI_MERGE_REQUEST_IID. Any hit here means the
			// fail-open bug has regressed: the unsigned, unverified
			// CI variable was used to key a role-PAT Notes API read.
			sawSpoofedNotesLookup.Store(true)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"pinned/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	// run-agent-job.sh ends with `exit "${RUN_STATUS}"`, so a sourcing
	// shell never returns control to any commands appended after the
	// `. "$SCRIPT"` call. Observe the resolved identity by having the
	// stub `fullsend` binary — invoked as a child process that inherits
	// the exported environment — dump the variables under test before
	// the script exits.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(
		"#!/bin/sh\n"+
			"echo FULLSEND_ARGS: \"$@\"\n"+
			"echo STUB_ENV GITLAB_ISSUE_URL=\"${GITLAB_ISSUE_URL:-EMPTY}\"\n"+
			"echo STUB_ENV GITLAB_MR_URL=\"${GITLAB_MR_URL:-EMPTY}\"\n"+
			"echo STUB_ENV MR_NUMBER=\"${MR_NUMBER:-EMPTY}\"\n"+
			"exit 0\n"), 0o755))

	const secret = "test-hmac-secret"
	// STATUS_IID is part of the HMAC-signed dispatch message and is
	// deliberately empty here — this job never natively receives an MR
	// IID either, so an empty signed value is the common case, not an
	// edge case.
	hmacHex := computeDispatchHMAC(secret, "5", "", "mr_note", "", "false", "", "", "", "test-key", "review", "")

	setupPinnedConfigOrigin(t, root)

	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"`)
	cmd.Dir = root
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"STAGE=review",
		"EVENT_TYPE=mr_note",
		"RESOURCE_KEY=test-key",
		"ACTOR_ID=5",
		"IS_FORK=false",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PIPELINE_URL=https://gitlab.example/pinned/project/-/pipelines/999",
		// STATUS_IID is deliberately left unset — the signed dispatch
		// carries no MR identity for this run.
		// CI_MERGE_REQUEST_IID is an ordinary, overridable pipeline
		// variable spoofing a merge request the dispatch never signed.
		"CI_MERGE_REQUEST_IID=999",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=unpinned/project",
		"CI_SERVER_URL=https://unpinned.example",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)

	assert.False(t, sawSpoofedNotesLookup.Load(), "must not query the Notes API for the unsigned, spoofed CI_MERGE_REQUEST_IID")
	assert.Contains(t, got, "No prior review found (no bot identity or no MR IID)")
	assert.NotContains(t, got, "/merge_requests/999")
	assert.Contains(t, got, "STUB_ENV GITLAB_ISSUE_URL=EMPTY")
	assert.Contains(t, got, "STUB_ENV GITLAB_MR_URL=EMPTY")
	assert.Contains(t, got, "STUB_ENV MR_NUMBER=0")
	assert.Contains(t, got, "--status-number 0")
	assert.NotContains(t, got, "--status-number 999")
}

// TestRunAgentJobScript_UnsetsMergeRequestIIDForIssueEvent is a regression
// test for the auth-bypass finding: in the issue_* EVENT_TYPE arm, this
// job's admit source is api-only, so GitLab never natively populates
// CI_MERGE_REQUEST_IID — any value present is an ordinary, outrankable
// project/group/pipeline CI/CD variable. newGitLabClientFromEnv
// (internal/cli/reconcilestatus.go) routes status-comment API calls to the
// merge_requests noteable type whenever CI_MERGE_REQUEST_IID is merely
// non-empty, regardless of FULLSEND_NOTE_TARGET — so a stray
// CI_MERGE_REQUEST_IID must not reach the fullsend CLI subprocess on an
// issue event, even when its value happens to equal the signed STATUS_IID
// (mere presence, not value mismatch, is what misdirects the note target).
func TestRunAgentJobScript_UnsetsMergeRequestIIDForIssueEvent(t *testing.T) {
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.Contains(path, "/repository/branches/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":100,"source":"api","user":{"id":7}}`))
		case strings.Contains(path, "/members/all/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		case strings.Contains(path, "/notes"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"pinned/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	// run-agent-job.sh ends with `exit "${RUN_STATUS}"`, so a sourcing
	// shell never returns control to any commands appended after the
	// `. "$SCRIPT"` call. Observe the resolved identity by having the
	// stub `fullsend` binary — invoked as a child process that inherits
	// the exported environment — dump the variable under test before the
	// script exits.
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(
		"#!/bin/sh\n"+
			"echo FULLSEND_ARGS: \"$@\"\n"+
			"echo STUB_ENV GITLAB_ISSUE_URL=\"${GITLAB_ISSUE_URL:-EMPTY}\"\n"+
			"echo STUB_ENV CI_MERGE_REQUEST_IID=\"${CI_MERGE_REQUEST_IID:-UNSET}\"\n"+
			"echo STUB_ENV FULLSEND_NOTE_TARGET=\"${FULLSEND_NOTE_TARGET:-UNSET}\"\n"+
			"exit 0\n"), 0o755))

	const secret = "test-hmac-secret"
	// STATUS_IID carries the signed issue IID for this issue_note dispatch.
	hmacHex := computeDispatchHMAC(secret, "5", "", "issue_note", "", "false", "", "", "", "test-key", "review", "7")

	setupPinnedConfigOrigin(t, root)

	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"`)
	cmd.Dir = root
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"STAGE=review",
		"EVENT_TYPE=issue_note",
		"RESOURCE_KEY=test-key",
		"ACTOR_ID=5",
		"IS_FORK=false",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PIPELINE_URL=https://gitlab.example/pinned/project/-/pipelines/999",
		"STATUS_IID=7",
		// An ordinary, overridable CI/CD variable that happens to equal
		// the signed STATUS_IID. Even though the value "agrees", its mere
		// presence must not reach the fullsend CLI subprocess for an
		// issue event, since newGitLabClientFromEnv treats any non-empty
		// CI_MERGE_REQUEST_IID as a merge_requests note target.
		"CI_MERGE_REQUEST_IID=7",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=unpinned/project",
		"CI_SERVER_URL=https://unpinned.example",
		// An ordinary, overridable CI/CD variable — the same class as
		// CI_MERGE_REQUEST_IID above — pre-set to the value
		// newGitLabClientFromEnv treats as selecting the merge_requests
		// note target. The issue_* arm must override it so the fullsend
		// CLI subprocess never inherits it.
		"FULLSEND_NOTE_TARGET=merge_requests",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)

	assert.Contains(t, got, "STUB_ENV GITLAB_ISSUE_URL=https://")
	assert.Contains(t, got, "/-/issues/7")
	assert.Contains(t, got, "STUB_ENV CI_MERGE_REQUEST_IID=UNSET")
	assert.Contains(t, got, "STUB_ENV FULLSEND_NOTE_TARGET=issues")
}
