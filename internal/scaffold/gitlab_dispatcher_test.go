package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Webhook dispatcher scaffold (ADR 0125, #7771): a GitLab native "use a
// webhook" pipeline trigger starts a source=trigger pipeline that runs only
// the dispatcher job, which hands the native TRIGGER_PAYLOAD file to the
// gitlab-webhook input driver.

const (
	gitlabDispatcherTemplatePath     = ".gitlab/ci/fullsend-dispatcher.yml"
	gitlabRunDispatcherJobScriptPath = ".gitlab/ci/scripts/run-dispatcher-job.sh"
	gitlabDispatcherJobName          = "fullsend webhook dispatcher"
	gitlabTriggerAdmitWorkflowRuleIf = `$CI_PIPELINE_SOURCE == "trigger" && $CI_COMMIT_REF_PROTECTED == "true" && $CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH`
	gitlabDebugTraceDenyIf           = `$CI_DEBUG_TRACE =~ /^(1|t|true)$/i`
)

type gitlabCIRule struct {
	If   string `yaml:"if"`
	When string `yaml:"when"`
}

// gitlabLastYAMLDocument decodes the final YAML document of a scaffold file
// (the body after any spec:inputs header document) into out.
func gitlabLastYAMLDocument(t *testing.T, path string, out any) {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(gitlabPerRepoText(t, path)))
	var last yaml.Node
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			break
		}
		last = doc
	}
	require.NotZero(t, last.Kind, "%s has no YAML document", path)
	require.NoError(t, last.Decode(out), "decoding %s", path)
}

func TestGitLabDispatcherJobContent(t *testing.T) {
	var doc map[string]struct {
		Stage        string         `yaml:"stage"`
		Tags         string         `yaml:"tags"`
		Rules        []gitlabCIRule `yaml:"rules"`
		BeforeScript []string       `yaml:"before_script"`
		Script       []string       `yaml:"script"`
		Trigger      any            `yaml:"trigger"`
	}
	gitlabLastYAMLDocument(t, gitlabDispatcherTemplatePath, &doc)
	require.Len(t, doc, 1, "the dispatcher template defines exactly one job")
	job, ok := doc[gitlabDispatcherJobName]
	require.True(t, ok, "missing %q job", gitlabDispatcherJobName)

	assert.Equal(t, "dispatch", job.Stage)
	assert.Equal(t, "__CONTROL_RUNNER_TAGS__", job.Tags,
		"the dispatcher holds the poller credential and must run on control-plane runners")
	assert.Nil(t, job.Trigger, "the dispatcher must not launch agents through a bridge job")

	require.Len(t, job.Rules, 2)
	assert.Equal(t, gitlabDebugTraceDenyIf, job.Rules[0].If, "debug-trace deny must come first")
	assert.Equal(t, "never", job.Rules[0].When)
	admit := job.Rules[1].If
	assert.Empty(t, job.Rules[1].When)
	assert.Contains(t, admit, `$CI_PIPELINE_SOURCE == "trigger"`)
	assert.Contains(t, admit, `$CI_COMMIT_REF_PROTECTED == "true"`)
	assert.Contains(t, admit, `$CI_COMMIT_REF_NAME == $CI_DEFAULT_BRANCH`)
	assert.Contains(t, admit, `$CI_DEBUG_TRACE !~ /^(1|t|true)$/i`)
	assert.NotContains(t, admit, `"schedule"`, "trigger-only allowlist")
	assert.NotContains(t, admit, `"api"`, "trigger-only allowlist")

	assert.Equal(t, []string{`. "${CI_PROJECT_DIR:-.}/` + gitlabInstallCLIScriptPath + `"`}, job.BeforeScript)
	assert.Equal(t, []string{`. "${CI_PROJECT_DIR:-.}/` + gitlabRunDispatcherJobScriptPath + `"`}, job.Script)
}

func TestGitLabDispatcherScriptContent(t *testing.T) {
	s := gitlabPerRepoText(t, gitlabRunDispatcherJobScriptPath)

	debugIdx := strings.Index(s, `case "${CI_DEBUG_TRACE:-}" in`)
	pinIdx := strings.Index(s, gitlabPinCIJobIdentityScriptPath)
	tokenIdx := strings.Index(s, "select-gitlab-role-token.sh")
	pollIdx := strings.Index(s, "\nfullsend poll \\\n")
	require.NotEqual(t, -1, debugIdx)
	require.NotEqual(t, -1, pinIdx)
	require.NotEqual(t, -1, tokenIdx)
	require.NotEqual(t, -1, pollIdx)
	assert.Less(t, debugIdx, pinIdx, "debug-trace guard must run before the identity pin")
	assert.Less(t, pinIdx, tokenIdx, "identity must be pinned before any credential is selected")
	assert.Less(t, tokenIdx, pollIdx)

	assert.Contains(t, s, "FULLSEND_ADMIT_SOURCE=trigger")
	assert.Contains(t, s, "FULLSEND_JOB_KIND=poller")
	routeIdx := strings.Index(s, "\nfullsend_select_gitlab_dispatch_route\n")
	require.NotEqual(t, -1, routeIdx, "the dispatcher must select the agent dispatch route (#8438)")
	assert.Less(t, tokenIdx, routeIdx, "the route is selected after the Poller credential, which may re-read the creator")
	assert.Less(t, routeIdx, pollIdx)
	assert.Contains(t, s, "--input-driver gitlab-webhook")
	assert.Contains(t, s, `--project "${FULLSEND_PINNED_PROJECT_PATH}"`)
	assert.Contains(t, s, `--gitlab-url "${FULLSEND_PINNED_GITLAB_URL}"`)
	assert.Contains(t, s, "export TRIGGER_PAYLOAD")

	// The payload is untrusted: never read or evaluated by the shell.
	assert.NotContains(t, s, `cat "${TRIGGER_PAYLOAD}"`)
	assert.NotContains(t, s, `$(< "${TRIGGER_PAYLOAD}")`)
	// Agent launch goes through typed pipeline inputs, never pipeline
	// variables or the trigger-token API.
	assert.NotContains(t, s, "variables[")
	assert.NotContains(t, s, "/trigger/pipeline")
	assert.NotContains(t, s, "curl ")
}

func TestGitLabPipelineWrapperSourceSeparation(t *testing.T) {
	var wrapper struct {
		Include []struct {
			Local  string         `yaml:"local"`
			Inputs map[string]any `yaml:"inputs"`
			Rules  []gitlabCIRule `yaml:"rules"`
		} `yaml:"include"`
		Stages []string `yaml:"stages"`
	}
	gitlabLastYAMLDocument(t, ".gitlab/ci/fullsend-pipeline.yml", &wrapper)

	// Each include admits a disjoint set of sources, one admit rule per
	// source. The agent template admits api (Poller pipelines API) and
	// pipeline (CI_JOB_TOKEN child dispatch, #8438).
	want := map[string][]string{
		".gitlab/ci/fullsend-poll.yml":  {"schedule"},
		gitlabDispatcherTemplatePath:    {"trigger"},
		".gitlab/ci/fullsend-agent.yml": {"api", "pipeline"},
	}
	sources := []string{"schedule", "trigger", "api", "pipeline"}
	seen := map[string]bool{}
	for _, inc := range wrapper.Include {
		admitted, ok := want[inc.Local]
		require.True(t, ok, "unexpected include %s", inc.Local)
		seen[inc.Local] = true

		require.Len(t, inc.Rules, 1+len(admitted), inc.Local)
		assert.Equal(t, gitlabDebugTraceDenyIf, inc.Rules[0].If, "%s: debug-trace deny must come first", inc.Local)
		assert.Equal(t, "never", inc.Rules[0].When, inc.Local)
		for i, source := range admitted {
			admit := inc.Rules[1+i].If
			assert.Empty(t, inc.Rules[1+i].When, inc.Local)
			assert.Contains(t, admit, `$CI_PIPELINE_SOURCE == "`+source+`"`, inc.Local)
			for _, other := range sources {
				if other != source {
					assert.NotContains(t, admit, `"`+other+`"`, "%s rule %d must admit only %s pipelines", inc.Local, 1+i, source)
				}
			}
		}
	}
	assert.Len(t, seen, len(want), "wrapper must include poll, dispatcher, and agent templates")

	for _, inc := range wrapper.Include {
		if inc.Local == gitlabDispatcherTemplatePath {
			assert.Empty(t, inc.Inputs,
				"the dispatcher reads TRIGGER_PAYLOAD; no dispatch inputs are forwarded to it")
		}
	}
	assert.Equal(t, []string{"dispatch", "poll", "agent"}, wrapper.Stages)
}

func TestGitLabRootCIAdmitsTriggerWithoutWideningInputs(t *testing.T) {
	path := ".gitlab-ci.yml"
	dec := yaml.NewDecoder(strings.NewReader(gitlabPerRepoText(t, path)))
	var header struct {
		Spec struct {
			Inputs yaml.Node `yaml:"inputs"`
		} `yaml:"spec"`
	}
	require.NoError(t, dec.Decode(&header))
	// ADR 0131: 11 scalars + event_payload_chunk_00..08 — the webhook
	// payload rides TRIGGER_PAYLOAD, never extra pipeline inputs.
	assert.Equal(t, 20*2, len(header.Spec.Inputs.Content), "root must keep exactly 20 pipeline inputs")

	var root struct {
		Workflow struct {
			Rules []gitlabCIRule `yaml:"rules"`
		} `yaml:"workflow"`
	}
	require.NoError(t, dec.Decode(&root))
	rules := root.Workflow.Rules
	require.NotEmpty(t, rules)
	assert.Equal(t, gitlabDebugTraceDenyIf, rules[0].If, "debug-trace deny must be the first workflow rule")
	assert.Equal(t, "never", rules[0].When)

	var triggerRules int
	for _, r := range rules[1:] {
		if strings.Contains(r.If, `"trigger"`) {
			triggerRules++
			assert.Equal(t, gitlabTriggerAdmitWorkflowRuleIf, r.If)
			assert.Empty(t, r.When)
		}
	}
	assert.Equal(t, 1, triggerRules, "exactly one trigger admit rule")
}

func TestCollectGitLabPerRepoInstallFiles_IncludesDispatcher(t *testing.T) {
	files, err := CollectGitLabPerRepoInstallFiles(nil, []string{"control-a", "control-b"}, "", "")
	require.NoError(t, err)
	var yml, script string
	for _, f := range files {
		switch f.Path {
		case gitlabDispatcherTemplatePath:
			yml = string(f.Content)
		case gitlabRunDispatcherJobScriptPath:
			script = string(f.Content)
		}
	}
	require.NotEmpty(t, yml, "install files must include %s", gitlabDispatcherTemplatePath)
	require.NotEmpty(t, script, "install files must include %s", gitlabRunDispatcherJobScriptPath)
	assert.NotContains(t, yml, "__CONTROL_RUNNER_TAGS__")
	assert.Contains(t, yml, "control-a")
	assert.Contains(t, yml, "control-b")
}

// --- run-dispatcher-job.sh behavior ---

func writeDispatcherScripts(t *testing.T, root string) string {
	t.Helper()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	return writeGitLabScript(t, root, gitlabRunDispatcherJobScriptPath)
}

func writeTriggerPayload(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "TRIGGER_PAYLOAD")
	require.NoError(t, os.WriteFile(p, []byte(`{"object_kind":"note","$(touch /tmp/pwned)":"x"}`), 0o600))
	return p
}

func dispatcherPinState(source, ref string) *pinAPIState {
	return &pinAPIState{
		jobJSON:      pinJobJSON("42", "100", ref),
		projectJSON:  `{"id":42,"default_branch":"main","path_with_namespace":"pinned/project"}`,
		branchJSON:   `{"name":"` + ref + `","protected":true}`,
		pipelineJSON: `{"id":100,"source":"` + source + `"}`,
		userIDByPAT:  map[string]int{"poller-pat": 10, "analyst-pat": 55},
	}
}

func TestRunDispatcherJobScript_DebugTraceAbortsBeforePin(t *testing.T) {
	for _, v := range []string{"true", "1", "TRUE", "T"} {
		t.Run(v, func(t *testing.T) {
			root := t.TempDir()
			script := writeDispatcherScripts(t, root)

			cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
			cmd.Env = []string{
				"SCRIPT=" + script,
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
				"CI_PROJECT_DIR=" + root,
				"CI_DEBUG_TRACE=" + v,
				"CI_JOB_TOKEN=job-token",
				"CI_PIPELINE_SOURCE=trigger",
				"TRIGGER_PAYLOAD=" + writeTriggerPayload(t),
				"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
			}
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, string(out), "CI_DEBUG_TRACE enabled")
			assert.NotContains(t, string(out), "CI_JOB_TOKEN job lookup")
		})
	}
}

func TestRunDispatcherJobScript_DeniesNonTriggerPinnedSource(t *testing.T) {
	// A forged CI_PIPELINE_SOURCE=trigger on a schedule or api pipeline
	// must not reach the poller credential.
	for _, source := range []string{"schedule", "api", "push", "web"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			script := writeDispatcherScripts(t, root)
			srv := startPinAPI(t, dispatcherPinState(source, "main"))
			t.Cleanup(srv.Close)

			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte("#!/bin/sh\necho FULLSEND_INVOKED\n"), 0o755))

			cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
			cmd.Env = append([]string{
				"SCRIPT=" + script,
				"PATH=" + bin + ":" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
				"CI_PROJECT_DIR=" + root,
				"RUNNER_TEMP=" + t.TempDir(),
				"CI_JOB_TOKEN=job-token",
				"CI_PIPELINE_SOURCE=trigger",
				"TRIGGER_PAYLOAD=" + writeTriggerPayload(t),
				"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
			}, pinTLSEnv(t, srv)...)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, string(out), "disjoint allowlist deny")
			assert.NotContains(t, string(out), "FULLSEND_INVOKED")
		})
	}
}

func TestRunDispatcherJobScript_RequiresReadableTriggerPayload(t *testing.T) {
	for name, payload := range map[string]string{
		"unset":     "",
		"missing":   filepath.Join(t.TempDir(), "does-not-exist"),
		"directory": t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			script := writeDispatcherScripts(t, root)
			srv := startPinAPI(t, dispatcherPinState("trigger", "main"))
			t.Cleanup(srv.Close)

			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte("#!/bin/sh\necho FULLSEND_INVOKED\n"), 0o755))

			env := []string{
				"SCRIPT=" + script,
				"PATH=" + bin + ":" + os.Getenv("PATH"),
				"HOME=" + t.TempDir(),
				"CI_PROJECT_DIR=" + root,
				"RUNNER_TEMP=" + t.TempDir(),
				"CI_JOB_TOKEN=job-token",
				"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
			}
			if payload != "" {
				env = append(env, "TRIGGER_PAYLOAD="+payload)
			}
			cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
			cmd.Env = append(env, pinTLSEnv(t, srv)...)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, string(out), "TRIGGER_PAYLOAD")
			assert.Contains(t, string(out), "fail-closed")
			assert.NotContains(t, string(out), "FULLSEND_INVOKED")
		})
	}
}

func TestRunDispatcherJobScript_HandsNativePayloadToWebhookDriver(t *testing.T) {
	root := t.TempDir()
	script := writeDispatcherScripts(t, root)
	srv := startPinAPI(t, dispatcherPinState("trigger", "main"))
	t.Cleanup(srv.Close)
	payload := writeTriggerPayload(t)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(`#!/bin/sh
echo "FULLSEND_ARGS: $*"
echo "TRIGGER_PAYLOAD=${TRIGGER_PAYLOAD}"
echo "CI_COMMIT_REF_NAME=${CI_COMMIT_REF_NAME}"
echo "JOB_TOKEN=${FULLSEND_JOB_TOKEN:-}"
echo "CODER=${FULLSEND_GITLAB_CODER_TOKEN:-<unset>}"
echo "ANALYST=${FULLSEND_GITLAB_ANALYST_TOKEN:-<unset>}"
echo "ANALYST_USER_ID=${FULLSEND_GITLAB_ANALYST_USER_ID:-<unset>}"
echo "FORGE=${FULLSEND_FORGE_TOKEN:-<unset>}"
echo "TRIGGER=${FULLSEND_TRIGGER_TOKEN:-<unset>}"
echo "WEBHOOK_SECRET=${FULLSEND_WEBHOOK_SECRET:-<unset>}"
`), 0o755))

	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
	cmd.Env = append([]string{
		"SCRIPT=" + script,
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"CI_PROJECT_DIR=" + root,
		"RUNNER_TEMP=" + t.TempDir(),
		"CI_JOB_TOKEN=job-token",
		"CI_PIPELINE_SOURCE=trigger",
		"TRIGGER_PAYLOAD=" + payload,
		"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
		"FULLSEND_GITLAB_ANALYST_TOKEN=analyst-pat",
		"FULLSEND_FORGE_TOKEN=shared-pat",
		"FULLSEND_TRIGGER_TOKEN=trigger-bearer",
		"FULLSEND_WEBHOOK_SECRET=webhook-secret",
		// Deliberately distinct from the pinned identity so passing
		// assertions prove the driver gets the pinned values.
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=unpinned/project",
		"CI_SERVER_URL=https://unpinned.example",
		"FULLSEND_GITLAB_URL=https://also-unpinned.example",
		"CI_COMMIT_REF_NAME=attacker-ref",
		"CI_DEFAULT_BRANCH=attacker-ref",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "stdout/stderr: %s", out)
	got := string(out)

	assert.Contains(t, got, "FULLSEND_ARGS: poll --input-driver gitlab-webhook --project pinned/project --gitlab-url "+srv.URL+" --fullsend-dir .fullsend")
	assert.Contains(t, got, "TRIGGER_PAYLOAD="+payload, "native TRIGGER_PAYLOAD path must be handed over unchanged")
	assert.Contains(t, got, "CI_COMMIT_REF_NAME=main")
	assert.Contains(t, got, "JOB_TOKEN=poller-pat")
	assert.Contains(t, got, "CODER=<unset>")
	assert.Contains(t, got, "ANALYST=<unset>")
	// Only the Analyst's numeric ID reaches the driver (#8807).
	assert.Contains(t, got, "ANALYST_USER_ID=55")
	assert.NotContains(t, got, "analyst-pat")
	assert.Contains(t, got, "FORGE=<unset>")
	assert.Contains(t, got, "TRIGGER=<unset>", "the webhook trigger bearer must not reach the driver")
	assert.Contains(t, got, "WEBHOOK_SECRET=<unset>", "the webhook secret must not reach the driver")
	assert.NotContains(t, got, "trigger-bearer")
	assert.NotContains(t, got, "webhook-secret")
	assert.NotContains(t, got, "unpinned")
	assert.NotContains(t, got, "attacker-ref")
}

// The dispatcher hands `fullsend poll` the CI_JOB_TOKEN child-pipeline
// route only when the webhook pipeline was created by the ready
// trigger-only account recorded in the protected ledger (#8438); a
// pipeline created by any other identity keeps the Poller API route.
func TestRunDispatcherJobScript_SelectsDispatchRouteFromPipelineCreator(t *testing.T) {
	const ledger = triggerLedgerGeneration
	for _, tc := range []struct {
		name       string
		creator    string
		ledger     string
		activation string
		want       string
	}{
		{"trigger account webhook pipeline", "77", ledger, triggerActivationRecord, "child-pipeline"},
		{"ready ledger without dispatch activation", "77", ledger, "", "api"},
		{"activation bound to another generation", "77", ledger, `{"version":1,"nonce":"fedcba9876543210","account_id":77,"deploy_key_id":5,"grant_entry_id":6,"trigger_token_id":9}`, "api"},
		{"Poller-owned webhook pipeline", "5", ledger, triggerActivationRecord, "api"},
		{"no trigger account recorded", "77", "", "", "api"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			script := writeDispatcherScripts(t, root)
			st := dispatcherPinState("trigger", "main")
			st.pipelineJSON = `{"id":100,"source":"trigger","user":{"id":` + tc.creator + `}}`
			st.jobJSON = pinJobJSONWithUser("42", "100", "main", tc.creator)
			srv := startPinAPI(t, st)
			t.Cleanup(srv.Close)

			env := append([]string{
				"SCRIPT=" + script,
				withCLI(t, true),
				"HOME=" + t.TempDir(),
				"CI_PROJECT_DIR=" + root,
				"RUNNER_TEMP=" + t.TempDir(),
				"CI_JOB_TOKEN=job-token",
				"CI_PIPELINE_SOURCE=trigger",
				"TRIGGER_PAYLOAD=" + writeTriggerPayload(t),
				"FULLSEND_GITLAB_POLLER_TOKEN=poller-pat",
			}, pinTLSEnv(t, srv)...)
			if tc.ledger != "" {
				env = append(env, "FULLSEND_GITLAB_TRIGGER_ACCOUNT="+tc.ledger)
			}
			if tc.activation != "" {
				env = append(env, "FULLSEND_GITLAB_DISPATCH_ACTIVATION="+tc.activation)
			}
			cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$SCRIPT\"")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "stdout/stderr: %s", out)
			assert.Contains(t, string(out), "ROUTE="+tc.want)
		})
	}
}
