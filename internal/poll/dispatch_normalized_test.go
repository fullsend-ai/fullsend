package poll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/dispatch"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const testMRHeadSHA = "0123456789abcdef0123456789abcdef01234567"

// syncedMR is a synchronized merge request event as toNormalizedEvent
// builds it, change proposal and head commit included.
func syncedMR() (RoutableEvent, dispatch.NormalizedEvent) {
	event := RoutableEvent{
		Type:      "mr_event",
		Action:    "synchronized",
		IID:       12,
		UpdatedAt: time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		Labels:    []string{"ready-for-review"},
		HeadSHA:   testMRHeadSHA,
	}
	ne := dispatch.NormalizedEvent{
		Repo:       "group/project",
		Source:     dispatch.Source{System: "gitlab", RawType: "merge_request", RawAction: "update"},
		Entity:     dispatch.Entity{Kind: "change_proposal", ID: 12, URL: "https://gitlab.example.com/group/project/-/merge_requests/12"},
		Transition: dispatch.Transition{Kind: "synchronized"},
		Actor:      dispatch.Actor{ID: "dev-user", Kind: "human", Role: "write"},
		State: dispatch.State{
			Labels: []string{"ready-for-review"},
			ChangeProposal: &dispatch.ChangeProposalState{
				ID: 12, HeadRepo: "group/project", BaseRepo: "group/project",
				HeadRef: "feature", BaseRef: "main", HeadSHA: testMRHeadSHA, AuthorID: "dev-user",
			},
		},
	}
	return event, ne
}

// mrNote is a merge request comment event and its normalized form, with
// the 4096-rune display bound convert.go applies to body and instruction.
func mrNote(body string) (RoutableEvent, dispatch.NormalizedEvent) {
	event := RoutableEvent{
		Type:         "mr_note",
		IID:          12,
		UpdatedAt:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
		NoteBody:     body,
		NoteID:       9,
		NoteAuthorID: 3,
		Labels:       []string{},
	}
	cmd, instruction := extractCommand(body)
	_, ne := syncedMR()
	ne.Transition = dispatch.Transition{
		Kind: "comment_added",
		Comment: &dispatch.TransitionComment{
			Body:        truncate(body, 4096),
			Command:     cmd,
			Instruction: truncate(instruction, 4096),
		},
	}
	ne.State.Labels = []string{}
	return event, ne
}

// payloadEvent parses the payload's _normalized_event with the validation
// fullsend run applies to --event-file.
func payloadEvent(t *testing.T, data []byte) *normevent.Event {
	t.Helper()
	var p struct {
		Norm json.RawMessage `json:"_normalized_event"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	ev, err := normevent.ParseJSON(p.Norm)
	if err != nil {
		t.Fatalf("_normalized_event does not pass fullsend run's validation: %v\npayload: %s", err, data)
	}
	return ev
}

func TestBuildEventPayload_EmbedsValidNormalizedEvent(t *testing.T) {
	event, ne := syncedMR()
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	ev := payloadEvent(t, data)
	if ev.Source.System != normevent.SystemGitLab {
		t.Errorf("source.system = %q, want gitlab", ev.Source.System)
	}
	if ev.Repo != "group/project" || ev.Entity.ID != 12 {
		t.Errorf("repo/entity = %q/%d, want group/project/12", ev.Repo, ev.Entity.ID)
	}
	cp := ev.State.ChangeProposal
	if cp == nil || cp.HeadSHA != testMRHeadSHA || cp.HeadRef != "feature" || cp.BaseRef != "main" {
		t.Errorf("change_proposal = %+v, want head %s on feature -> main", cp, testMRHeadSHA)
	}
	// The flat fields other consumers read are unchanged.
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["type"] != "mr_event" || m["action"] != "synchronized" || m["iid"] != float64(12) {
		t.Errorf("flat fields = type %v action %v iid %v", m["type"], m["action"], m["iid"])
	}
}

func TestBuildEventPayload_FullEventBoundsNonCommandComment(t *testing.T) {
	body := "Changes requested\n" + strings.Repeat("x", 3000)
	event, ne := mrNote(body)
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	c := payloadEvent(t, data).Transition.Comment
	if c == nil {
		t.Fatal("transition.comment missing")
	}
	for name, s := range map[string]string{"body": c.Body, "instruction": c.Instruction} {
		if n := len([]rune(s)); n > noteBodyTransportLimit {
			t.Errorf("comment %s has %d runes, want at most %d", name, n, noteBodyTransportLimit)
		}
	}
	if !strings.HasSuffix(c.Body, noteBodyTruncationNotice) {
		t.Errorf("comment body = %q, want the truncation notice", c.Body)
	}
	// run-agent-job.sh reads the body from the same canonical path.
	if got, ok := payloadCommentBody(t, data); !ok || got != c.Body {
		t.Errorf("canonical comment body = %q (present %v), want %q", got, ok, c.Body)
	}
}

func TestBuildEventPayload_FullEventKeepsCommandIntact(t *testing.T) {
	event, ne := mrNote(multilineFixBody)
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	c := payloadEvent(t, data).Transition.Comment
	if c == nil || c.Body != multilineFixBody || c.Command != "/fs-fix" {
		t.Fatalf("comment = %+v, want the intact /fs-fix command", c)
	}
	_, instruction := extractCommand(multilineFixBody)
	if c.Instruction != instruction {
		t.Errorf("instruction = %q, want %q", c.Instruction, instruction)
	}
}

func TestBuildEventPayload_FullEventCommentMatchesTransportedBody(t *testing.T) {
	// convert.go bounds the instruction at 4096 runes for display; the
	// payload re-derives it from the body it actually carries.
	event, ne := mrNote(multilineFixBody)
	ne.Transition.Comment.Command = ""
	ne.Transition.Comment.Instruction = "stale"
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	c := payloadEvent(t, data).Transition.Comment
	cmd, instruction := extractCommand(multilineFixBody)
	if c == nil || c.Command != cmd || c.Instruction != instruction {
		t.Errorf("comment = %+v, want command %q and instruction %q from the body", c, cmd, instruction)
	}
}

func TestBuildEventPayload_FallsBackToCommentOnlyEvent(t *testing.T) {
	cases := map[string]func(*RoutableEvent, *dispatch.NormalizedEvent){
		// fullsend run would reject it, so it must not be sent.
		"invalid event": func(_ *RoutableEvent, ne *dispatch.NormalizedEvent) { ne.Actor.Role = "" },
		// Too large for the nine transport chunks.
		"oversized event": func(_ *RoutableEvent, ne *dispatch.NormalizedEvent) {
			for i := 0; i < 2000; i++ {
				ne.State.Labels = append(ne.State.Labels, "label-number-"+strings.Repeat("z", 5))
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			event, ne := mrNote(multilineFixBody)
			mutate(&event, &ne)
			got, err := buildEventPayload(event, &ne)
			if err != nil {
				t.Fatalf("buildEventPayload: %v", err)
			}
			want, err := buildEventPayload(event, nil)
			if err != nil {
				t.Fatalf("buildEventPayload without event: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("payload = %s\nwant the comment-only payload %s", got, want)
			}
		})
	}
}

func TestDispatch_SignsPayloadWithNormalizedEvent(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	event, ne := syncedMR()
	if err := p.dispatch(context.Background(), "owner", "repo", "review", event, &ne); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(mc.pipelineCalls) != 1 {
		t.Fatalf("expected 1 pipeline call, got %d", len(mc.pipelineCalls))
	}
	data, err := base64.StdEncoding.DecodeString(mc.pipelineCalls[0].Variables["EVENT_PAYLOAD_B64"])
	if err != nil {
		t.Fatalf("decode EVENT_PAYLOAD_B64: %v", err)
	}
	if got := payloadEvent(t, data).State.ChangeProposal; got == nil || got.HeadSHA != testMRHeadSHA {
		t.Errorf("signed payload change_proposal = %+v, want head %s", got, testMRHeadSHA)
	}
}

// gitlabEventFilter returns the jq program run-agent-job.sh uses to pick the
// GitLab normalized event out of the dispatch payload.
func gitlabEventFilter(t *testing.T) string {
	t.Helper()
	content, err := scaffold.GitLabPerRepoFile(".gitlab/ci/scripts/run-agent-job.sh")
	if err != nil {
		t.Fatalf("read run-agent-job.sh: %v", err)
	}
	m := regexp.MustCompile(`jq -e '([^']*\.source\.system == "gitlab"[^']*)'`).FindSubmatch(content)
	if m == nil {
		t.Fatal("run-agent-job.sh has no jq filter selecting a gitlab normalized event")
	}
	return string(m[1])
}

func TestRunAgentJob_GitLabEventFilterMatchesPayload(t *testing.T) {
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not installed")
	}
	filter := gitlabEventFilter(t)
	extract := func(payload []byte) ([]byte, bool) {
		cmd := exec.Command(jq, "-e", filter)
		cmd.Stdin = bytes.NewReader(payload)
		out, err := cmd.Output()
		return out, err == nil
	}

	event, ne := syncedMR()
	full, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	out, ok := extract(full)
	if !ok {
		t.Fatal("filter did not select the embedded GitLab event")
	}
	if ev, err := normevent.ParseJSON(out); err != nil || ev.Source.System != normevent.SystemGitLab {
		t.Errorf("extracted event = %s, err %v; want a valid gitlab event", out, err)
	}

	// A comment-only payload (an older poller, or a fallback) must not be
	// handed to fullsend run.
	noteEvent, _ := mrNote(multilineFixBody)
	commentOnly, err := buildEventPayload(noteEvent, nil)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	if out, ok := extract(commentOnly); ok {
		t.Errorf("filter selected a comment-only event: %s", out)
	}
}
