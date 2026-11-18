package scaffold

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// runAgentJobForCreator runs run-agent-job.sh against a mock GitLab whose
// pinned pipeline has the given source and creator, with the Poller PAT
// authenticating as user 7. It returns the combined output; the job is
// expected to stop at or after dispatch authentication. extra environment
// entries are applied last (later entries win), so a test can tamper with
// a signed dispatch field.
func runAgentJobForCreator(t *testing.T, source, creator, ledger string, extra ...string) string {
	t.Helper()
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/job", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(pinJobJSON("42", "100", "main")))
	})
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":7,"username":"fullsend-poller-bot"}`))
	})
	mux.HandleFunc("/api/v4/projects/", func(w http.ResponseWriter, r *http.Request) {
		switch path := r.URL.Path; {
		case strings.Contains(path, "/repository/branches/"):
			_, _ = w.Write([]byte(`{"name":"main","protected":true}`))
		case strings.Contains(path, "/pipelines/"):
			_, _ = w.Write([]byte(`{"id":100,"source":"` + source + `","user":{"id":` + creator + `}}`))
		default:
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"group/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	const secret = "dispatch-secret"
	pollURL := "https://gitlab.example/group/project/-/jobs/1"
	hmac := computeDispatchHMAC(secret, "", "", "issue_note", pollURL, "false", "", "", "", "issue-1", "triage", "1")
	env := append([]string{
		"SCRIPT=" + script,
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmac,
		"STAGE=triage",
		"EVENT_TYPE=issue_note",
		"FULLSEND_POLL_JOB_URL=" + pollURL,
		"IS_FORK=false",
		"RESOURCE_KEY=issue-1",
		"STATUS_IID=1",
	}, pinTLSEnv(t, srv)...)
	if ledger != "" {
		env = append(env, "FULLSEND_GITLAB_TRIGGER_ACCOUNT="+ledger)
		// The activation record is a separate variable bound to the ledger
		// generation; tests that vary it pass an override in extra.
		env = append(env, "FULLSEND_GITLAB_DISPATCH_ACTIVATION="+triggerActivationRecord)
	}
	env = append(env, extra...)
	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = env
	out, _ := cmd.CombinedOutput()
	return string(out)
}

const readyTriggerLedger = triggerLedgerGeneration

// A source=pipeline agent pipeline (#8438) is authenticated only when its
// creator is the ready trigger-only account and its dispatch HMAC is valid;
// it is never accepted on the Poller's identity, and a source=api pipeline
// is still accepted only from the Poller.
func TestRunAgentJobScript_ChildPipelineCreatorCheck(t *testing.T) {
	t.Run("trigger account child pipeline passes authentication", func(t *testing.T) {
		out := runAgentJobForCreator(t, "pipeline", "77", readyTriggerLedger)
		assert.Contains(t, out, "Dispatched by: https://gitlab.example/group/project/-/jobs/1", out)
		assert.NotContains(t, out, "rejecting")
	})

	t.Run("Poller-created child pipeline is rejected", func(t *testing.T) {
		out := runAgentJobForCreator(t, "pipeline", "7", readyTriggerLedger)
		assert.Contains(t, out, "Pipeline created by user 7, expected 77 for source=pipeline")
		assert.NotContains(t, out, "Dispatched by:")
	})

	t.Run("unrelated creator is rejected", func(t *testing.T) {
		out := runAgentJobForCreator(t, "pipeline", "12", readyTriggerLedger)
		assert.Contains(t, out, "rejecting forged dispatch")
		assert.NotContains(t, out, "Dispatched by:")
	})

	for name, ledger := range map[string]string{
		"no ledger":       "",
		"rotating ledger": `{"phase":"rotating","account_id":77}`,
		"malformed":       "{not json",
		// Exactly one JSON document is accepted, as the Go decoder requires.
		"valid ledger followed by trailing garbage": readyTriggerLedger + ` {broken`,
		"two concatenated ledger documents":         readyTriggerLedger + ` ` + readyTriggerLedger,
	} {
		t.Run(name+" rejects child pipelines", func(t *testing.T) {
			out := runAgentJobForCreator(t, "pipeline", "77", ledger)
			assert.Contains(t, out, "no ready trigger-only account is recorded")
			assert.NotContains(t, out, "Dispatched by:")
		})
	}

	// A ready ledger whose activation gate was never recorded as passed, whose
	// activation was withdrawn, or whose activation record is bound to another
	// generation (an older CLI rotated or repaired the account) must not admit
	// a source=pipeline agent job.
	for name, activation := range map[string]string{
		"ready ledger without an activation record": "",
		"malformed activation record":               "{not json",
		"activation record bound to another nonce":  `{"version":1,"nonce":"fedcba9876543210","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":9}`,
		"activation record bound to another token":  `{"version":1,"nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":10}`,
		"activation record with an unknown version": `{"version":2,"nonce":"0123456789abcdef","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":9}`,
	} {
		t.Run(name+" rejects child pipelines", func(t *testing.T) {
			out := runAgentJobForCreator(t, "pipeline", "77", readyTriggerLedger, "FULLSEND_GITLAB_DISPATCH_ACTIVATION="+activation)
			assert.Contains(t, out, "no ready trigger-only account is recorded")
			assert.NotContains(t, out, "Dispatched by:")
		})
	}

	t.Run("trigger account recorded as the Poller is rejected", func(t *testing.T) {
		out := runAgentJobForCreator(t, "pipeline", "7", `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":7,"deploy_key_id":5,"deploy_key_fingerprint":"SHA256:abc","grant_entry_id":6,"trigger_token_id":9,"trigger_published":true,"publish_started":true}`, `FULLSEND_GITLAB_DISPATCH_ACTIVATION={"version":1,"nonce":"0123456789abcdef","account_id":7,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":9}`)
		assert.Contains(t, out, "the recorded trigger-only account is the Poller identity")
		assert.NotContains(t, out, "Dispatched by:")
	})

	t.Run("api pipeline from the trigger account is rejected", func(t *testing.T) {
		out := runAgentJobForCreator(t, "api", "77", readyTriggerLedger)
		assert.Contains(t, out, "Pipeline created by user 77, expected 7 for source=api")
		assert.NotContains(t, out, "Dispatched by:")
	})

	t.Run("api pipeline from the Poller still passes authentication", func(t *testing.T) {
		out := runAgentJobForCreator(t, "api", "7", readyTriggerLedger)
		assert.Contains(t, out, "Dispatched by: https://gitlab.example/group/project/-/jobs/1", out)
	})

	t.Run("tampered child pipeline input fails HMAC verification", func(t *testing.T) {
		out := runAgentJobForCreator(t, "pipeline", "77", readyTriggerLedger, "STAGE=code")
		assert.Contains(t, out, "HMAC verification failed")
		assert.NotContains(t, out, "Dispatched by:")
	})
}
