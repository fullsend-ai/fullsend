package poll

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"gopkg.in/yaml.v3"
)

// jiraTestEvent returns a valid Jira /fs-triage comment event targeting
// projectPath, as the jira-poll CEL matcher embeds it in a dispatch record.
func jiraTestEvent(projectPath string) *normevent.Event {
	return &normevent.Event{
		Repo: projectPath,
		Source: normevent.Source{
			System:    normevent.SystemJira,
			RawType:   "comment",
			RawAction: "created",
		},
		Entity: normevent.Entity{
			Kind: normevent.EntityWorkItem,
			ID:   10042,
			Key:  "PROJ-123",
			URL:  "https://acme.atlassian.net/browse/PROJ-123",
		},
		Transition: normevent.Transition{
			Kind: normevent.TransitionCommentAdded,
			Comment: &normevent.Comment{
				Body:        "/fs-triage please",
				Command:     "/fs-triage",
				Instruction: "please",
			},
		},
		Actor: normevent.Actor{ID: "acct-1", Kind: normevent.ActorHuman, Role: normevent.RoleWrite},
		State: normevent.State{Labels: []string{}},
	}
}

// decodeJiraPayload decodes a dispatched EVENT_PAYLOAD_B64 and parses its
// _normalized_event the way run-agent-job.sh and fullsend run do.
func decodeJiraPayload(t *testing.T, encoded string) (string, *normevent.Event) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var payload struct {
		Type            string          `json:"type"`
		NormalizedEvent json.RawMessage `json:"_normalized_event"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	ev, err := normevent.ParseJSON(payload.NormalizedEvent)
	if err != nil {
		t.Fatalf("parse normalized event: %v", err)
	}
	return payload.Type, ev
}

func TestDispatchJiraEvent_NestedProjectThroughAPIRoute(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, "group/sub/project", withTestSecret(Options{
		GitLabURL:   "https://gitlab.example.com/gitlab/",
		PipelineRef: "main",
		PollJobURL:  "https://gitlab.example.com/gitlab/group/sub/project/-/jobs/9",
	}))

	if err := p.DispatchJiraEvent(context.Background(), "triage", jiraTestEvent("group/sub/project")); err != nil {
		t.Fatalf("DispatchJiraEvent: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("pipeline calls = %d, want 1", len(mc.pipelineCalls))
	}
	call := mc.pipelineCalls[0]
	if call.Owner != "group/sub" || call.Repo != "project" || call.Ref != "main" {
		t.Errorf("pipeline target = %s/%s@%s, want group/sub/project@main", call.Owner, call.Repo, call.Ref)
	}
	if call.JobToken != "" {
		t.Errorf("JobToken = %q, want the Poller API route", call.JobToken)
	}
	if len(mc.wrapperContentRefs) != 1 || mc.wrapperContentRefs[0] != "main" {
		t.Errorf("wrapper reads = %v, want one read at the dispatch ref", mc.wrapperContentRefs)
	}

	vars := call.Variables
	want := map[string]string{
		"STAGE":             "triage",
		"EVENT_TYPE":        "jira_comment",
		"RESOURCE_KEY":      "jira-PROJ-123",
		"IS_FORK":           "false",
		"ORIGINATING_URL":   "https://acme.atlassian.net/browse/PROJ-123",
		"REPO_FULL_NAME":    "group/sub/project",
		forge.VarPollJobURL: "https://gitlab.example.com/gitlab/group/sub/project/-/jobs/9",
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, want %q", k, vars[k], v)
		}
	}
	for _, k := range []string{"ACTOR_ID", "STATUS_IID", "MR_AUTHOR_ID"} {
		if _, ok := vars[k]; ok {
			t.Errorf("%s = %q, want unset for a Jira work item", k, vars[k])
		}
	}
	if got, wantHMAC := vars[forge.VarDispatchHMAC], computeDispatchHMAC(testDispatchSecret, vars); got != wantHMAC {
		t.Errorf("dispatch HMAC = %q, want %q over the delivered variables", got, wantHMAC)
	}

	eventType, ev := decodeJiraPayload(t, vars["EVENT_PAYLOAD_B64"])
	if eventType != "jira_comment" {
		t.Errorf("payload type = %q, want jira_comment", eventType)
	}
	if ev.Source.System != normevent.SystemJira || ev.Entity.Key != "PROJ-123" || ev.Entity.URL != "https://acme.atlassian.net/browse/PROJ-123" {
		t.Errorf("normalized event identity = %s %s %s, want jira PROJ-123 and its browse URL", ev.Source.System, ev.Entity.Key, ev.Entity.URL)
	}
	if ev.Repo != "group/sub/project" {
		t.Errorf("normalized event repo = %q, want the full project path", ev.Repo)
	}
	if ev.Transition.Comment == nil || ev.Transition.Comment.Body != "/fs-triage please" {
		t.Errorf("comment = %+v, want the original short body", ev.Transition.Comment)
	}
}

func TestDispatchJiraEvent_ChildPipelineRoute(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, "group/project", withTestSecret(Options{PipelineRef: "main", ChildDispatchJobToken: "job-token"}))

	if err := p.DispatchJiraEvent(context.Background(), "code", jiraTestEvent("group/project")); err != nil {
		t.Fatalf("DispatchJiraEvent: %v", err)
	}
	if len(mc.pipelineCalls) != 1 || mc.pipelineCalls[0].JobToken != "job-token" {
		t.Fatalf("pipeline calls = %+v, want one child-pipeline call with the job token", mc.pipelineCalls)
	}
}

func TestDispatchJiraEvent_FailsClosed(t *testing.T) {
	// A typed wrapper from before Jira dispatch: every input is declared,
	// but resource_key only accepts issue and MR keys.
	inputs := make(map[string]any, len(dispatchInputNames)+maxEventPayloadChunks)
	for _, name := range dispatchInputNames {
		inputs[name] = map[string]any{"default": ""}
	}
	for i := range maxEventPayloadChunks {
		inputs[dispatchEventPayloadChunkInputName(i)] = map[string]any{"default": ""}
	}
	inputs["resource_key"] = map[string]any{"default": "", "regex": "^((issue|mr)-[0-9]+)?$"}
	header, err := yaml.Marshal(map[string]any{"spec": map[string]any{"inputs": inputs}})
	if err != nil {
		t.Fatal(err)
	}
	oldWrapper := append(header, []byte("---\n{}\n")...)

	tests := []struct {
		name    string
		stage   string
		secret  string
		mutate  func(*normevent.Event)
		client  func(*mockClient)
		nilEv   bool
		wantErr string
	}{
		{name: "nil event", nilEv: true, wantErr: "event is required"},
		{name: "invalid stage", stage: "tri$age", wantErr: "not a valid agent name"},
		{name: "not jira", mutate: func(e *normevent.Event) { e.Source.System = normevent.SystemGitLab }, wantErr: "is not jira"},
		{name: "not a work item", mutate: func(e *normevent.Event) { e.Entity.Kind = normevent.EntityChangeProposal }, wantErr: "not a work item"},
		{name: "other project", mutate: func(e *normevent.Event) { e.Repo = "group/other" }, wantErr: `not the GitLab project "group/project"`},
		{name: "malformed key", mutate: func(e *normevent.Event) { e.Entity.Key = "proj-1; rm" }, wantErr: "malformed Jira key"},
		{name: "http URL", mutate: func(e *normevent.Event) { e.Entity.URL = "http://acme.atlassian.net/browse/PROJ-123" }, wantErr: "non-https URL"},
		{name: "unsupported raw type", mutate: func(e *normevent.Event) { e.Source.RawType = "Comment X" }, wantErr: "unsupported source.raw_type"},
		{name: "missing dispatch secret", secret: "-", wantErr: "FULLSEND_DISPATCH_SECRET is required"},
		{name: "uninstalled project", client: func(m *mockClient) { m.wrapperNotFound = true }, wantErr: "no typed pipeline-input contract"},
		{name: "wrapper read error", client: func(m *mockClient) { m.wrapperErr = errors.New("403 Forbidden") }, wantErr: "403 Forbidden"},
		{name: "pre-Jira scaffold", client: func(m *mockClient) { m.wrapperContent = oldWrapper }, wantErr: "rerun repos install"},
		{name: "pipeline rejected", client: func(m *mockClient) { m.pipelineErr = errors.New("400 Bad Request") }, wantErr: "400 Bad Request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := newMockClient()
			if tt.client != nil {
				tt.client(mc)
			}
			opts := Options{PipelineRef: "main", DispatchSecret: testDispatchSecret}
			if tt.secret == "-" {
				opts.DispatchSecret = ""
			}
			p := New(mc, nil, "group/project", opts)
			stage := tt.stage
			if stage == "" {
				stage = "triage"
			}
			var ev *normevent.Event
			if !tt.nilEv {
				ev = jiraTestEvent("group/project")
				if tt.mutate != nil {
					tt.mutate(ev)
				}
			}
			err := p.DispatchJiraEvent(context.Background(), stage, ev)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
			if mc.pipelineErr == nil && len(mc.pipelineCalls) != 0 {
				t.Errorf("pipeline calls = %d, want none", len(mc.pipelineCalls))
			}
		})
	}
}

// TestDispatchJiraEvent_TruncatesLongCommentToFitBudget checks that a
// worst-case 4096-character comment (HTML-sensitive runes expand 6x under
// JSON escaping) is shortened, with the truncation notice, until the
// payload fits the event_payload_chunk_NN budget instead of failing the
// dispatch.
func TestDispatchJiraEvent_TruncatesLongCommentToFitBudget(t *testing.T) {
	mc := newMockClient()
	p := New(mc, nil, "group/project", withTestSecret(Options{PipelineRef: "main"}))
	ev := jiraTestEvent("group/project")
	long := "/fs-code " + strings.Repeat("<", 4087)
	ev.Transition.Comment.Body = long
	ev.Transition.Comment.Command = "/fs-code"
	ev.Transition.Comment.Instruction = strings.Repeat("<", 4087)

	if err := p.DispatchJiraEvent(context.Background(), "code", ev); err != nil {
		t.Fatalf("DispatchJiraEvent: %v", err)
	}
	encoded := mc.pipelineCalls[0].Variables["EVENT_PAYLOAD_B64"]
	if len(encoded) > maxEventPayloadChunks*dispatchPayloadChunkSize {
		t.Fatalf("payload is %d characters, over the transport budget", len(encoded))
	}
	_, got := decodeJiraPayload(t, encoded)
	if !strings.HasSuffix(got.Transition.Comment.Body, noteBodyTruncationNotice) || !strings.HasPrefix(got.Transition.Comment.Body, "/fs-code ") {
		t.Errorf("comment body was not truncated with the notice: %q", got.Transition.Comment.Body)
	}
	if got.Transition.Comment.Command != "/fs-code" {
		t.Errorf("command = %q, want /fs-code kept intact", got.Transition.Comment.Command)
	}
	if ev.Transition.Comment.Body != long {
		t.Error("DispatchJiraEvent modified the caller's event")
	}
}

func TestEncodeJiraEventPayload_TooLargeWithoutComment(t *testing.T) {
	ev := jiraTestEvent("group/project")
	ev.Transition = normevent.Transition{Kind: normevent.TransitionLabelChanged, Label: &normevent.LabelChange{Name: "x", Action: "added"}}
	ev.State.Labels = []string{strings.Repeat("l", maxEventPayloadChunks*dispatchPayloadChunkSize)}
	if _, err := encodeJiraEventPayload("jira_changelog", ev); err == nil || !strings.Contains(err.Error(), "dispatch budget") {
		t.Fatalf("err = %v, want a budget error", err)
	}
}

// TestGitLabWrapperAcceptsResourceKey_ShippedScaffold pins the shipped
// pipeline wrapper's resource_key regex to accept Jira keys alongside the
// existing issue and MR keys, and the check to reject a pre-Jira regex.
func TestGitLabWrapperAcceptsResourceKey_ShippedScaffold(t *testing.T) {
	shipped, err := scaffold.GitLabPerRepoFile(fullsendPipelineIncludePath)
	if err != nil {
		t.Fatalf("read shipped wrapper: %v", err)
	}
	for _, rk := range []string{"issue-1", "mr-42", "jira-PROJ-123", "jira-AB_C2-9"} {
		if !gitlabWrapperAcceptsResourceKey(shipped, rk) {
			t.Errorf("shipped wrapper rejects %q", rk)
		}
	}
	for _, rk := range []string{"jira-proj-1", "jira-PROJ-0", "jira-$X-1", "issue-x"} {
		if gitlabWrapperAcceptsResourceKey(shipped, rk) {
			t.Errorf("shipped wrapper accepts %q", rk)
		}
	}

	pre := []byte("spec:\n  inputs:\n    resource_key:\n      regex: '^((issue|mr)-[0-9]+)?$'\n---\n{}\n")
	if gitlabWrapperAcceptsResourceKey(pre, "jira-PROJ-1") {
		t.Error("pre-Jira wrapper accepted a Jira resource key")
	}
	for name, content := range map[string]string{
		"no resource_key input": "spec:\n  inputs:\n    stage:\n      default: ''\n",
		"invalid regex":         "spec:\n  inputs:\n    resource_key:\n      regex: '('\n",
		"unparseable":           "spec: [\n",
	} {
		if gitlabWrapperAcceptsResourceKey([]byte(content), "jira-PROJ-1") {
			t.Errorf("%s: accepted a Jira resource key", name)
		}
	}
}
