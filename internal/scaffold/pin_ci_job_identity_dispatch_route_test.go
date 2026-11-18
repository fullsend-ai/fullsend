package scaffold

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The agent job admits the "api pipeline" set (#8438): source=api agent
// pipelines started by the Poller through the pipelines API, and
// source=pipeline agent pipelines started by a trigger-account poll or
// dispatcher job through the CI_JOB_TOKEN trigger API. Every other source
// stays denied.
func TestPinCIJobIdentity_AgentSetAdmitsAPIAndPipelineOnly(t *testing.T) {
	for _, tc := range []struct {
		source string
		admit  bool
	}{
		{"api", true},
		{"pipeline", true},
		{"schedule", false},
		{"trigger", false},
		{"parent_pipeline", false},
		{"web", false},
		{"push", false},
		{"", false},
	} {
		t.Run("source="+tc.source, func(t *testing.T) {
			root, script := writePinCIJobIdentityScripts(t)
			st := &pinAPIState{
				jobJSON:      pinJobJSON("42", "100", "main"),
				projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
				branchJSON:   `{"name":"main","protected":true}`,
				pipelineJSON: `{"id":100,"source":"` + tc.source + `","user":{"id":7}}`,
			}
			srv := startPinAPI(t, st)
			t.Cleanup(srv.Close)

			out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv), "FULLSEND_ADMIT_SOURCE=api pipeline"))
			if tc.admit {
				require.NoError(t, err, "stdout/stderr: %s", out)
				assert.Contains(t, out, "PINNED_SOURCE="+tc.source)
				return
			}
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, out, "disjoint allowlist deny")
		})
	}
}

// The single-source allowlists of the poller and dispatcher never admit
// source=pipeline: only the agent job's "api pipeline" set does.
func TestPinCIJobIdentity_SingleSourceAllowlistsDenyPipeline(t *testing.T) {
	for _, admit := range []string{"api", "schedule", "trigger"} {
		t.Run(admit, func(t *testing.T) {
			root, script := writePinCIJobIdentityScripts(t)
			st := &pinAPIState{
				jobJSON:      pinJobJSON("42", "100", "main"),
				projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
				branchJSON:   `{"name":"main","protected":true}`,
				pipelineJSON: `{"id":100,"source":"pipeline","user":{"id":7}}`,
			}
			srv := startPinAPI(t, st)
			t.Cleanup(srv.Close)

			out, err := sourcePinScript(t, root, script, append(pinTLSEnv(t, srv), "FULLSEND_ADMIT_SOURCE="+admit))
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, out, "disjoint allowlist deny")
		})
	}
}

func TestPinCIJobIdentity_RejectsOtherSourceSets(t *testing.T) {
	for _, admit := range []string{"pipeline", "api schedule", "pipeline api", "api  pipeline"} {
		t.Run(admit, func(t *testing.T) {
			root, script := writePinCIJobIdentityScripts(t)
			out, err := sourcePinScript(t, root, script, []string{
				"FULLSEND_ADMIT_SOURCE=" + admit,
				"CI_API_V4_URL=https://gitlab.example/api/v4",
			})
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, out, "disjoint per-job allowlist")
		})
	}
}

// selectDispatchRoute sources the pin script for a schedule pipeline, then
// runs fullsend_select_gitlab_dispatch_route and prints the route.
func selectDispatchRoute(t *testing.T, pipelineJSON, jobJSON string, extraEnv []string) (string, error) {
	t.Helper()
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:      jobJSON,
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:   `{"name":"main","protected":true}`,
		pipelineJSON: pipelineJSON,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"; fullsend_select_gitlab_dispatch_route; echo "ROUTE=${FULLSEND_GITLAB_DISPATCH_ROUTE}"`)
	cmd.Env = append(append([]string{
		"SCRIPT=" + script,
		withCLI(t, true),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"FULLSEND_ADMIT_SOURCE=schedule",
		"CI_JOB_TOKEN=job-token",
	}, pinTLSEnv(t, srv)...), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The poll and dispatcher jobs select the CI_JOB_TOKEN child-pipeline route
// only when the pipeline was created by the ready trigger-only account
// recorded in the ledger and the separate activation record names that exact
// ledger generation; every other case keeps the Poller API route, so
// existing polling continues while activation is deferred (#8438).
func TestSelectGitLabDispatchRoute(t *testing.T) {
	const readyLedger = triggerLedgerGeneration
	const sched = `{"id":100,"source":"schedule","user":{"id":77}}`
	for _, tc := range []struct {
		name       string
		pipeline   string
		ledger     *string
		activation *string
		want       string
	}{
		{"trigger account created the pipeline", sched, ptr(readyLedger), ptr(triggerActivationRecord), "child-pipeline"},
		{"ready ledger without activation record", sched, ptr(readyLedger), nil, "api"},
		{"blank activation record", sched, ptr(readyLedger), ptr(""), "api"},
		{"malformed activation record", sched, ptr(readyLedger), ptr("{not json"), "api"},
		{"activation record followed by trailing garbage", sched, ptr(readyLedger), ptr(triggerActivationRecord + ` {broken`), "api"},
		{"activation record for a rotated generation (older CLI rotated)", sched,
			ptr(`{"version":1,"phase":"ready","branch":"main","nonce":"fedcba9876543210","account_id":78,"deploy_key_id":5,"deploy_key_fingerprint":"SHA256:abc","grant_entry_id":6,"trigger_token_id":10,"trigger_published":true,"publish_started":true}`),
			ptr(triggerActivationRecord), "api"},
		{"activation record for another trigger token", sched, ptr(readyLedger),
			ptr(`{"version":1,"nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":10}`), "api"},
		{"activation record for another account", sched, ptr(readyLedger),
			ptr(`{"version":1,"nonce":"0123456789abcdef","account_id":78,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":9}`), "api"},
		{"activation record with an unknown version", sched, ptr(readyLedger),
			ptr(`{"version":2,"nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":9}`), "api"},
		{"activation record without the ledger", sched, nil, ptr(triggerActivationRecord), "api"},
		{"ledger without a published trigger token", sched,
			ptr(`{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"deploy_key_fingerprint":"SHA256:abc","grant_entry_id":6}`),
			ptr(`{"version":1,"nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":0}`), "api"},
		{"another user created the pipeline", `{"id":100,"source":"schedule","user":{"id":5}}`, ptr(readyLedger), ptr(triggerActivationRecord), "api"},
		{"no ledger", sched, nil, nil, "api"},
		{"blank ledger", sched, ptr(""), ptr(triggerActivationRecord), "api"},
		{"malformed ledger", sched, ptr("{not json"), ptr(triggerActivationRecord), "api"},
		{"valid ledger followed by trailing garbage", sched, ptr(readyLedger + ` {broken`), ptr(triggerActivationRecord), "api"},
		{"two concatenated ledger documents", sched, ptr(readyLedger + ` ` + readyLedger), ptr(triggerActivationRecord), "api"},
		{"ledger not an object", sched, ptr("[77]"), ptr(triggerActivationRecord), "api"},
		{"ledger with an unsupported version", sched, ptr(strings.Replace(readyLedger, `"version":1`, `"version":2`, 1)), ptr(triggerActivationRecord), "api"},
		{"ledger without a version", sched, ptr(strings.Replace(readyLedger, `"version":1,`, ``, 1)), ptr(triggerActivationRecord), "api"},
		{"rotating ledger", sched, ptr(`{"phase":"rotating","account_id":77}`), ptr(triggerActivationRecord), "api"},
		{"bootstrapping ledger", sched, ptr(`{"phase":"bootstrapping","account_id":77}`), ptr(triggerActivationRecord), "api"},
		{"removing ledger", sched, ptr(`{"phase":"removing","account_id":77}`), ptr(triggerActivationRecord), "api"},
		{"string account id", sched, ptr(`{"phase":"ready","account_id":"77"}`), ptr(triggerActivationRecord), "api"},
		{"zero account id", `{"id":100,"source":"schedule","user":{"id":0}}`, ptr(`{"phase":"ready","account_id":0}`), ptr(triggerActivationRecord), "api"},
		{"creator omitted and no PAT to re-read it", `{"id":100,"source":"schedule"}`, ptr(readyLedger), ptr(triggerActivationRecord), "api"},
		{"non-numeric creator", `{"id":100,"source":"schedule","user":{"id":"77x"}}`, ptr(readyLedger), ptr(triggerActivationRecord), "api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var env []string
			if tc.ledger != nil {
				env = append(env, "FULLSEND_GITLAB_TRIGGER_ACCOUNT="+*tc.ledger)
			}
			if tc.activation != nil {
				env = append(env, "FULLSEND_GITLAB_DISPATCH_ACTIVATION="+*tc.activation)
			}
			out, err := selectDispatchRoute(t, tc.pipeline, pinJobJSONWithUser("42", "100", "main", "77"), env)
			require.NoError(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, out, "ROUTE="+tc.want)
		})
	}
}

// A newer scaffold with an older CLI (#8438): the CLI that predates
// FULLSEND_GITLAB_DISPATCH_ROUTE would ignore the selector and fail late on
// the Poller route, after the event was consumed. When the pipeline can only
// dispatch through CI_JOB_TOKEN, route selection must stop explicitly, before
// any dispatch, instead of silently falling back. Pipelines that do not
// select the child route are unaffected by the CLI's age.
func TestSelectGitLabDispatchRoute_OlderCLIFailsBeforeDispatch(t *testing.T) {
	const pipeline = `{"id":100,"source":"schedule","user":{"id":77}}`
	activeEnv := []string{
		"FULLSEND_GITLAB_TRIGGER_ACCOUNT=" + triggerLedgerGeneration,
		"FULLSEND_GITLAB_DISPATCH_ACTIVATION=" + triggerActivationRecord,
	}
	t.Run("older CLI is refused for a trigger-account pipeline", func(t *testing.T) {
		out, err := selectDispatchRoute(t, pipeline, pinJobJSONWithUser("42", "100", "main", "77"), append(activeEnv, withCLI(t, false)))
		require.Error(t, err, "stdout/stderr: %s", out)
		assert.Contains(t, out, "does not support the FULLSEND_GITLAB_DISPATCH_ROUTE child-pipeline route")
		assert.Contains(t, out, "Refusing to dispatch")
		assert.NotContains(t, out, "ROUTE=")
	})
	t.Run("missing CLI is refused for a trigger-account pipeline", func(t *testing.T) {
		out, err := selectDispatchRoute(t, pipeline, pinJobJSONWithUser("42", "100", "main", "77"), append(activeEnv, "PATH="+t.TempDir()+":/usr/bin:/bin"))
		require.Error(t, err, "stdout/stderr: %s", out)
		assert.Contains(t, out, "Refusing to dispatch")
	})
	t.Run("older CLI keeps the api route when the activation is not recorded", func(t *testing.T) {
		out, err := selectDispatchRoute(t, pipeline, pinJobJSONWithUser("42", "100", "main", "77"),
			[]string{"FULLSEND_GITLAB_TRIGGER_ACCOUNT=" + triggerLedgerGeneration, withCLI(t, false)})
		require.NoError(t, err, "stdout/stderr: %s", out)
		assert.Contains(t, out, "ROUTE=api")
	})
	t.Run("older CLI keeps the api route for another creator", func(t *testing.T) {
		out, err := selectDispatchRoute(t, `{"id":100,"source":"schedule","user":{"id":5}}`, pinJobJSONWithUser("42", "100", "main", "77"), append(activeEnv, withCLI(t, false)))
		require.NoError(t, err, "stdout/stderr: %s", out)
		assert.Contains(t, out, "ROUTE=api")
	})
}

// CI_JOB_TOKEN authenticates as the running job's user, not the pipeline
// creator. A trigger-account pipeline whose job was retried by someone else
// (or whose job record omits the user) keeps the Poller API route, because a
// child pipeline created with that token would belong to the retrier and be
// rejected by the agent job.
func TestSelectGitLabDispatchRoute_RequiresJobUserToBeTriggerAccount(t *testing.T) {
	const pipeline = `{"id":100,"source":"schedule","user":{"id":77}}`
	for name, job := range map[string]string{
		"retried by another user": pinJobJSONWithUser("42", "100", "main", "5"),
		"job record omits user":   pinJobJSON("42", "100", "main"),
	} {
		t.Run(name, func(t *testing.T) {
			out, err := selectDispatchRoute(t, pipeline, job, []string{
				"FULLSEND_GITLAB_TRIGGER_ACCOUNT=" + triggerLedgerGeneration,
				"FULLSEND_GITLAB_DISPATCH_ACTIVATION=" + triggerActivationRecord,
			})
			require.NoError(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, out, "ROUTE=api")
		})
	}
}

// When the job-token pipeline GET omits the creator, the route helper
// re-reads it with the selected role PAT using the pinned IDs.
func TestSelectGitLabDispatchRoute_RereadsOmittedCreatorWithPAT(t *testing.T) {
	root, script := writePinCIJobIdentityScripts(t)
	st := &pinAPIState{
		jobJSON:         pinJobJSONWithUser("42", "100", "main", "77"),
		projectJSON:     `{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`,
		branchJSON:      `{"name":"main","protected":true}`,
		pipelineJSON:    `{"id":100,"source":"schedule"}`,
		pipelinePATJSON: `{"id":100,"source":"schedule","user":{"id":77}}`,
	}
	srv := startPinAPI(t, st)
	t.Cleanup(srv.Close)

	cmd := exec.Command("bash", "-c", `set -euo pipefail; . "$SCRIPT"; FULLSEND_JOB_TOKEN=role-pat; fullsend_select_gitlab_dispatch_route; echo "ROUTE=${FULLSEND_GITLAB_DISPATCH_ROUTE}"`)
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		withCLI(t, true),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"FULLSEND_ADMIT_SOURCE=schedule",
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_TRIGGER_ACCOUNT=" + triggerLedgerGeneration,
		"FULLSEND_GITLAB_DISPATCH_ACTIVATION=" + triggerActivationRecord,
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", string(out))
	assert.Contains(t, string(out), "ROUTE=child-pipeline")
	assert.True(t, st.sawPAT.Load(), "the omitted creator must be re-read with the role PAT")
}

func ptr(s string) *string { return &s }
