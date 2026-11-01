package scaffold

import (
	"encoding/base64"
	"net/http"
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

// jiraDispatchPayloadB64 is the EVENT_PAYLOAD_B64 poll.DispatchJiraEvent
// sends for a Jira /fs-triage comment: the event type and the normalized
// Jira event.
var jiraDispatchPayloadB64 = base64.StdEncoding.EncodeToString([]byte(`{"type":"jira_comment","_normalized_event":{"repo":"group/sub/project","entity":{"kind":"work_item","id":10042,"key":"PROJ-123","url":"https://acme.atlassian.net/browse/PROJ-123"},"transition":{"kind":"comment_added","comment":{"body":"/fs-triage","command":"/fs-triage"}},"actor":{"id":"acct-1","kind":"human","role":"write"},"state":{"labels":[]},"source":{"system":"jira","raw_type":"comment","raw_action":"created"}}}`))

type jiraAgentJobRun struct {
	out         string
	err         error
	memberCalls int32
}

// runAgentJobScriptForJira runs run-agent-job.sh for a Jira work-item
// dispatch signed over signedEventType, with the job's EVENT_TYPE set to
// eventType. The stub fullsend binary prints its arguments, the event file
// it was given, and the GitLab entity variables the harness would see.
func runAgentJobScriptForJira(t *testing.T, stage, eventType, signedEventType, payloadB64 string) jiraAgentJobRun {
	t.Helper()
	root := t.TempDir()
	writeGitLabScript(t, root, ".gitlab/ci/scripts/trust-ci-server-ca.sh")
	writeGitLabScript(t, root, gitlabPinCIJobIdentityScriptPath)
	writeGitLabScript(t, root, ".gitlab/ci/scripts/select-gitlab-role-token.sh")
	script := writeGitLabScript(t, root, gitlabRunAgentJobScriptPath)

	var run jiraAgentJobRun
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
			atomic.AddInt32(&run.memberCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_level":40}`))
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":42,"default_branch":"main","path_with_namespace":"group/sub/project"}`))
		}
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "fullsend"), []byte(
		"#!/bin/sh\n"+
			"if [ \"$1\" = \"run\" ]; then\n"+
			"  echo FULLSEND_ARGS: \"$@\"\n"+
			"  while [ $# -gt 0 ]; do\n"+
			"    if [ \"$1\" = \"--event-file\" ]; then echo \"EVENT_FILE: $(cat \"$2\" | tr -d '\\n ')\"; fi\n"+
			"    shift\n"+
			"  done\n"+
			"  echo STUB_ENV GITLAB_ISSUE_URL=\"${GITLAB_ISSUE_URL:-EMPTY}\"\n"+
			"  echo STUB_ENV CI_MERGE_REQUEST_IID=\"${CI_MERGE_REQUEST_IID:-UNSET}\"\n"+
			"  echo STUB_ENV FULLSEND_NOTE_TARGET=\"${FULLSEND_NOTE_TARGET:-UNSET}\"\n"+
			"fi\n"+
			"exit 0\n"), 0o755))

	const secret = "test-hmac-secret"
	const originatingURL = "https://acme.atlassian.net/browse/PROJ-123"
	const jiraResource = "jira-PROJ-123"
	hmacHex := computeDispatchHMAC(secret, "", payloadB64, signedEventType, "", "false", "", originatingURL, "group/sub/project", jiraResource, stage, "")

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
		"FULLSEND_GITLAB_CODER_TOKEN=coder-pat",
		"STAGE=" + stage,
		"EVENT_TYPE=" + eventType,
		"EVENT_PAYLOAD_B64=" + payloadB64,
		"RESOURCE_KEY=" + jiraResource,
		"IS_FORK=false",
		"ORIGINATING_URL=" + originatingURL,
		"REPO_FULL_NAME=group/sub/project",
		"FULLSEND_DISPATCH_SECRET=" + secret,
		"FULLSEND_DISPATCH_HMAC=" + hmacHex,
		"CI_PIPELINE_URL=https://gitlab.example/group/sub/project/-/pipelines/999",
		"CI_PROJECT_ID=1",
		"CI_PROJECT_PATH=group/sub/project",
		// Overridable CI/CD variables a Jira dispatch must not inherit.
		"CI_MERGE_REQUEST_IID=7",
		"FULLSEND_NOTE_TARGET=merge_requests",
	}, pinTLSEnv(t, srv)...)
	out, err := cmd.CombinedOutput()
	run.out = string(out)
	run.err = err
	return run
}

// TestRunAgentJobScript_JiraDispatchRunsWithEventFile checks a signed Jira
// work-item dispatch reaches fullsend run with the Jira normalized event as
// --event-file, without a GitLab issue or merge-request target, and without
// the GitLab members check that a Jira actor has no identity for.
func TestRunAgentJobScript_JiraDispatchRunsWithEventFile(t *testing.T) {
	run := runAgentJobScriptForJira(t, "triage", "jira_comment", "jira_comment", jiraDispatchPayloadB64)
	require.NoError(t, run.err, "stdout/stderr: %s", run.out)

	assert.Contains(t, run.out, "FULLSEND_ARGS: run triage")
	assert.Contains(t, run.out, "--event-file")
	assert.Contains(t, run.out, `"key":"PROJ-123"`)
	assert.Contains(t, run.out, `"url":"https://acme.atlassian.net/browse/PROJ-123"`)
	assert.Contains(t, run.out, `"repo":"group/sub/project"`)
	assert.Contains(t, run.out, "STUB_ENV GITLAB_ISSUE_URL=EMPTY")
	assert.Contains(t, run.out, "STUB_ENV CI_MERGE_REQUEST_IID=UNSET")
	assert.Contains(t, run.out, "STUB_ENV FULLSEND_NOTE_TARGET=issues")
	assert.Contains(t, run.out, "actor authorized by the Jira poller")
	assert.Zero(t, atomic.LoadInt32(&run.memberCalls), "a Jira dispatch has no GitLab actor to look up")
}

func TestRunAgentJobScript_JiraDispatchFailsClosed(t *testing.T) {
	noEvent := base64.StdEncoding.EncodeToString([]byte(`{"type":"jira_comment"}`))
	gitlabEvent := base64.StdEncoding.EncodeToString([]byte(`{"type":"jira_comment","_normalized_event":{"source":{"system":"gitlab"}}}`))
	tests := []struct {
		name            string
		stage           string
		eventType       string
		signedEventType string
		payload         string
		wantOut         string
	}{
		{name: "payload without normalized event", stage: "triage", eventType: "jira_comment", signedEventType: "jira_comment", payload: noEvent, wantOut: "has no Jira normalized event"},
		{name: "payload with non-Jira normalized event", stage: "triage", eventType: "jira_comment", signedEventType: "jira_comment", payload: gitlabEvent, wantOut: "has no Jira normalized event"},
		{name: "merge-request stage", stage: "review", eventType: "jira_comment", signedEventType: "jira_comment", payload: jiraDispatchPayloadB64, wantOut: "needs a merge request"},
		// A forged EVENT_TYPE cannot select the Jira branch: it is part of
		// the signed dispatch message.
		{name: "forged jira event type", stage: "triage", eventType: "jira_comment", signedEventType: "issue_note", payload: jiraDispatchPayloadB64, wantOut: "HMAC verification failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runAgentJobScriptForJira(t, tt.stage, tt.eventType, tt.signedEventType, tt.payload)
			require.Error(t, run.err, "stdout/stderr: %s", run.out)
			assert.Contains(t, run.out, tt.wantOut)
			assert.NotContains(t, run.out, "FULLSEND_ARGS: run")
		})
	}
}
