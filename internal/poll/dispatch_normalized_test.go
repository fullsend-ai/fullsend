package poll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"reflect"
	"regexp"
	"slices"
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
		// A complete form that does not fit falls back only for a
		// non-command body; an oversized /fs-* command body fails closed
		// instead (TestBuildEventPayload_CommandBodyOverBudgetFailsClosed).
		// With the body, labels and every attacker-shaped single string
		// bounded (noteBodyTransportLimit, transportLabelsLimit,
		// transportStringRefLimit) only the whole-travelling config-derived
		// fields can push a non-command complete form past the budget —
		// TestBuildEventPayload_SchemaMaxConfigFailsClosed pins that
		// outcome, and TestBuildEventPayload_WorstCaseFitsTransportBudget
		// pins that a plausible config still fits.
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

// An event author controls state.labels (anyone who can label the work item
// can), so the complete normalized event's labels are bounded in transport:
// a many-label event still carries its complete normalized event — the
// author cannot force the comment-only fallback that would leave the run
// with no event, which ADR 0112 Decision 2 trusts — and the labels it
// carries are the bounded prefix.
func TestBuildEventPayload_BoundsStateLabels(t *testing.T) {
	event, ne := mrNote(multilineFixBody)
	labels := make([]string, 2000)
	for i := range labels {
		labels[i] = "label-number-" + strings.Repeat("z", 5)
	}
	ne.State.Labels = labels
	// The event's own label list is the same attacker-controlled channel
	// state.labels is: an unbounded outer copy would push the complete
	// payload past the budget and force the comment-only fallback.
	event.Labels = labels
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	got := payloadEvent(t, data).State.Labels
	if len(got) == 0 || len(got) == len(labels) {
		t.Fatalf("transported %d labels as %d, want a strict non-empty prefix", len(labels), len(got))
	}
	if enc, err := json.Marshal(got); err != nil || len(enc) > transportLabelsLimit {
		t.Errorf("transported labels are %d bytes (err %v), want at most %d", len(enc), err, transportLabelsLimit)
	}
	if !slices.Equal(got, labels[:len(got)]) {
		t.Errorf("transported labels are not a prefix of the event's labels")
	}
}

// The transport budget is the sum of independently bounded
// attacker-controlled fields. This builds the worst shape an event author
// can give the payload — an escape-heavy note body at the note body bound
// (each rune JSON-escapes to six bytes), state.labels at the labels bound,
// maximal escape-heavy branch refs and logins, and the schema-maximal repo
// paths, entity URL and linked-change-proposal URL — and pins that the
// complete normalized event still fits the chunk budget, so the
// comment-only fallback (and with it the attacker-chosen "no event" every
// `event == null` overlay applies on) cannot be reached through any one
// field's bound. Every maximal string is escape-heavy because Go's JSON
// encoder expands <, > and & to six bytes per rune; transportStringRefLimit
// bounds each one's JSON form. If a limit drifts so this stops holding,
// lower noteBodyTransportLimit, transportLabelsLimit or
// transportStringRefLimit until it does.
func TestBuildEventPayload_WorstCaseFitsTransportBudget(t *testing.T) {
	event, ne := mrNote(strings.Repeat("<", noteBodyTransportLimit))
	// One label sized to (nearly) the whole labels bound.
	ne.State.Labels = []string{"l" + strings.Repeat("x", transportLabelsLimit-6)}
	// The event's own label list is bounded like state.labels.
	event.Labels = ne.State.Labels
	// Maximal escape-heavy branch refs and logins (Git refnames and the
	// normalized event spec allow 255) and schema-maximal fork repo
	// paths (512).
	event.SourceBranch = strings.Repeat("<", 255)
	event.TargetBranch = strings.Repeat("<", 255)
	if cp := ne.State.ChangeProposal; cp != nil {
		cp.HeadRef = strings.Repeat("<", 255)
		cp.BaseRef = strings.Repeat("<", 255)
		cp.AuthorID = strings.Repeat("<", 255)
		cp.HeadRepo = strings.Repeat("<", 512)
		cp.BaseRepo = strings.Repeat("<", 512)
	}
	ne.Actor.ID = strings.Repeat("<", 255)
	// The config-derived repo, entity.url and
	// entity.linked_change_proposal.url travel whole, so they sit at a
	// documented plausible config rather than the schema maxima an
	// operator would have to go out of their way to configure: a
	// 120-rune nested-group project path and 160-rune URLs (host, path
	// and /-/merge_requests/N), each well under the schema's 512/2048
	// ceilings. A schema-max config instead forces the documented
	// fail-closed outcome
	// (TestBuildEventPayload_SchemaMaxConfigFailsClosed).
	ne.Repo = strings.Repeat("p", 120)
	// A work item linked to its change proposal (validation forbids
	// linked_change_proposal on a change_proposal entity) carries the
	// proposal's URL beside the entity URL, and a review carried beside
	// the comment adds the maximal reviewer login: an attacker-shaped
	// single string the budget has to bound too.
	ne.Entity.Kind = "work_item"
	ne.Entity.URL = strings.Repeat("u", 160)
	ne.Entity.LinkedChangeProposal = &dispatch.LinkedChangeProposal{
		ID:  12,
		URL: strings.Repeat("l", 160),
	}
	ne.Transition.Review = &dispatch.TransitionReview{
		State:      "approved",
		ReviewerID: strings.Repeat("<", 255),
	}
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	// The payload must be the complete form, not the comment-only
	// fallback: it validates as a normalized event and carries the
	// bounded labels, the escape-heavy body at its bound and the bounded
	// refs.
	ev := payloadEvent(t, data)
	if len(ev.State.Labels) != 1 || ev.State.Labels[0] != ne.State.Labels[0] {
		t.Errorf("transported labels = %v, want the bounded label carried", ev.State.Labels)
	}
	if got := ev.Transition.Comment; got == nil || len(got.Body) != noteBodyTransportLimit {
		t.Errorf("transported comment body has %d runes, want %d", len(got.Body), noteBodyTransportLimit)
	}
	// An escape-heavy maximal ref is carried as the longest prefix whose
	// JSON form fits transportStringRefLimit: six JSON bytes per rune.
	boundedRunes := (transportStringRefLimit - 2) / 6
	if cp := ev.State.ChangeProposal; cp == nil || len(cp.HeadRef) != boundedRunes || len(cp.BaseRef) != boundedRunes {
		t.Errorf("transported change_proposal = %+v, want %d-rune bounded refs carried", cp, boundedRunes)
	}
	// The config-derived fields travel whole: the plausible config's
	// repo and URLs are carried uncut, past nothing — they are not
	// bounded at all.
	if len(ev.Repo) != 120 || len(ev.Entity.URL) != 160 {
		t.Errorf("transported repo/url = %d/%d runes, want the whole 120/160-rune config values", len(ev.Repo), len(ev.Entity.URL))
	}
	if lcp := ev.Entity.LinkedChangeProposal; lcp == nil || lcp.ID != 12 || len(lcp.URL) != 160 {
		t.Errorf("transported linked_change_proposal = %+v, want the whole 160-rune URL carried", ev.Entity.LinkedChangeProposal)
	}
	if review := ev.Transition.Review; review == nil || len(review.ReviewerID) != boundedRunes {
		t.Errorf("transported review = %+v, want the %d-rune bounded reviewer id carried", ev.Transition.Review, boundedRunes)
	}
	// The bounds are applied to a copy (transportNormalizedEvent), so the
	// caller's event keeps every original string: the dispatch decision
	// and any later evaluation see what the forge sent, not the
	// transported prefix.
	if ne.Entity.LinkedChangeProposal == nil {
		t.Fatalf("caller's linked_change_proposal was cleared")
	}
	if ne.Transition.Review == nil {
		t.Fatalf("caller's transition.review was cleared")
	}
	if len(ne.Entity.LinkedChangeProposal.URL) != 160 || len(ne.Transition.Review.ReviewerID) != 255 ||
		len(ne.Entity.URL) != 160 || len(ne.Actor.ID) != 255 || len(ne.State.ChangeProposal.HeadRef) != 255 {
		t.Errorf("bounding mutated the caller's event: lcp url %d, reviewer id %d, entity url %d, actor %d, head ref %d runes",
			len(ne.Entity.LinkedChangeProposal.URL), len(ne.Transition.Review.ReviewerID),
			len(ne.Entity.URL), len(ne.Actor.ID), len(ne.State.ChangeProposal.HeadRef))
	}
	if n := base64.StdEncoding.EncodedLen(len(data)); n > maxEventPayloadChunks*dispatchPayloadChunkSize {
		t.Errorf("worst-case payload is %d base64 characters, over the %d budget", n, maxEventPayloadChunks*dispatchPayloadChunkSize)
	}
}

// The config-derived repo, entity.url and entity.linked_change_proposal.url
// leave the 258-byte single-string bound and travel whole, so a GitLab
// project whose path plus host plus /-/merge_requests/N runs past the bound
// still gets its repo and URLs intact — including the bot-role lookup's
// event.Repo. The dispatch still fails closed on the chunk budget: a config
// whose lengths push the complete payload past it falls back to the
// comment-only event for a note and refuses the dispatch for a command
// (TestBuildEventPayload_SchemaMaxConfigFailsClosed).
func TestBuildEventPayload_ConfigFieldsTravelWhole(t *testing.T) {
	event, ne := mrNote(strings.Repeat("<", 100))
	// Each config-derived field past the 258-byte single-string bound
	// (300 plain runes cost 302 JSON bytes), the payload still fitting
	// the chunk budget.
	ne.Repo = strings.Repeat("p", 300)
	ne.Entity.Kind = "work_item"
	ne.Entity.URL = strings.Repeat("u", 300)
	ne.Entity.LinkedChangeProposal = &dispatch.LinkedChangeProposal{
		ID:  12,
		URL: strings.Repeat("l", 300),
	}
	// An attacker-shaped field beside them is still bounded.
	ne.Actor.ID = strings.Repeat("<", 255)
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	ev := payloadEvent(t, data)
	if len(ev.Repo) != 300 || len(ev.Entity.URL) != 300 {
		t.Errorf("transported repo/url = %d/%d runes, want the whole 300-rune values", len(ev.Repo), len(ev.Entity.URL))
	}
	if lcp := ev.Entity.LinkedChangeProposal; lcp == nil || len(lcp.URL) != 300 {
		t.Errorf("transported linked_change_proposal = %+v, want the whole 300-rune URL carried", ev.Entity.LinkedChangeProposal)
	}
	if boundedRunes := (transportStringRefLimit - 2) / 6; len(ev.Actor.ID) != boundedRunes {
		t.Errorf("transported actor.id = %d runes, want the %d-rune bound to still apply", len(ev.Actor.ID), boundedRunes)
	}
}

// A schema-maximal config URL pushes the complete payload past the chunk
// budget, so the dispatch fails closed the documented way: a note falls
// back to the comment-only event (never a partial _normalized_event) and a
// command body is refused outright.
func TestBuildEventPayload_SchemaMaxConfigFailsClosed(t *testing.T) {
	oversize := func() (RoutableEvent, dispatch.NormalizedEvent) {
		event, ne := worstCaseAttackerEvent()
		ne.Entity.URL = strings.Repeat("u", 2048)
		return event, ne
	}

	event, ne := oversize()
	data, err := buildEventPayload(event, &ne)
	if err != nil {
		t.Fatalf("buildEventPayload: %v", err)
	}
	want, err := buildEventPayload(event, nil)
	if err != nil {
		t.Fatalf("buildEventPayload without event: %v", err)
	}
	if !bytes.Equal(data, want) {
		t.Errorf("payload = %s\nwant the comment-only payload %s", data, want)
	}

	event, ne = oversize()
	// The command body itself fits; the schema-max config URL is what
	// pushes the complete form past the budget, and a command note is
	// still refused rather than sent without its complete event.
	event.NoteBody = "/fs-fix " + strings.Repeat("<", 100)
	if _, err := buildEventPayload(event, &ne); err == nil {
		t.Fatal("buildEventPayload: want an error for an over-budget command body, got a payload")
	} else if !strings.Contains(err.Error(), "refusing to dispatch a command note") {
		t.Errorf("error does not name the command-note refusal: %v", err)
	}
}

// A /fs-* command body is carried untruncated, so a complete event that
// does not fit cannot shrink to fit: buildEventPayload fails closed, the
// way dispatch() refuses an untransportable command, instead of falling
// back to the comment-only normalized event. The fallback's partial
// _normalized_event would fail the run's validation and bind event as CEL
// null — the attacker-chosen "no event" every `event == null` overlay
// applies on (ADR 0112 Decision 2).
func TestBuildEventPayload_CommandBodyOverBudgetFailsClosed(t *testing.T) {
	event, ne := mrNote("/fs-fix " + strings.Repeat("<", 6000))
	event.SourceBranch = strings.Repeat("<", 255)
	event.TargetBranch = strings.Repeat("<", 255)
	ne.State.Labels = []string{"l" + strings.Repeat("x", transportLabelsLimit-6)}
	_, err := buildEventPayload(event, &ne)
	if err == nil {
		t.Fatal("buildEventPayload: want an error for an over-budget command body, got a payload")
	}
	if !strings.Contains(err.Error(), "refusing to dispatch a command note") {
		t.Errorf("error does not name the command-note refusal: %v", err)
	}
}

func TestBoundTransportString(t *testing.T) {
	if got := boundTransportString(""); got != "" {
		t.Errorf("boundTransportString(\"\") = %q, want empty", got)
	}
	plain := strings.Repeat("b", 255)
	if got := boundTransportString(plain); got != plain {
		t.Errorf("boundTransportString(plain 255-byte ref) cut it to %d runes, want it unchanged", len(got))
	}
	escapeHeavy := strings.Repeat("<", 255)
	got := boundTransportString(escapeHeavy)
	want := (transportStringRefLimit - 2) / 6
	if len(got) != want || strings.Trim(got, "<") != "" {
		t.Errorf("boundTransportString(escape-heavy) = %d runes, want the %d-rune prefix of '<'", len(got), want)
	}
	if enc, err := json.Marshal(got); err != nil || len(enc) > transportStringRefLimit {
		t.Errorf("bounded string is %d JSON bytes (err %v), want at most %d", len(enc), err, transportStringRefLimit)
	}
	// Bytes that are not valid UTF-8 (git permits them in refnames) decode
	// as U+FFFD over the range loop and the encoder writes each as the
	// six-byte \ufffd escape, so they are bounded like an escape-heavy
	// rune, not like the three UTF-8 bytes a genuine U+FFFD costs.
	invalid := strings.Repeat("\xff", 255)
	got = boundTransportString(invalid)
	if len(got) != want || strings.Trim(got, "\xff") != "" {
		t.Errorf("boundTransportString(invalid UTF-8) = %d bytes, want the %d-byte prefix", len(got), want)
	}
	if enc, err := json.Marshal(got); err != nil || len(enc) > transportStringRefLimit {
		t.Errorf("bounded invalid-UTF-8 string is %d JSON bytes (err %v), want at most %d", len(enc), err, transportStringRefLimit)
	}
}

func TestBoundTransportLabels(t *testing.T) {
	if got := boundTransportLabels(nil); got != nil {
		t.Errorf("boundTransportLabels(nil) = %v, want nil", got)
	}
	small := []string{"ready-for-review"}
	if got := boundTransportLabels(small); !slices.Equal(got, small) {
		t.Errorf("boundTransportLabels(small) = %v, want it unchanged", got)
	}
	big := make([]string, 2000)
	for i := range big {
		big[i] = "label-" + strings.Repeat("y", 20)
	}
	got := boundTransportLabels(big)
	if len(got) == 0 || len(got) == len(big) {
		t.Fatalf("bound %d labels to %d, want a strict non-empty prefix", len(big), len(got))
	}
	if enc, err := json.Marshal(got); err != nil || len(enc) > transportLabelsLimit {
		t.Errorf("bounded labels are %d bytes (err %v), want at most %d", len(enc), err, transportLabelsLimit)
	}
	if !slices.Equal(got, big[:len(got)]) {
		t.Errorf("bounded labels are not a prefix of the input")
	}
}

// boundedTransportFields mirrors the field list in boundTransportEventStrings
// as (JSON path, getter) pairs. TestBoundTransportEventStrings_CoversEveryStringField
// fails when the struct and this list drift apart in either direction.
var boundedTransportFields = []struct {
	path string
	get  func(*dispatch.NormalizedEvent) string
}{
	{"transition.label.name", func(e *dispatch.NormalizedEvent) string {
		return e.Transition.Label.Name
	}},
	{"transition.review.reviewer_id", func(e *dispatch.NormalizedEvent) string {
		return e.Transition.Review.ReviewerID
	}},
	{"actor.id", func(e *dispatch.NormalizedEvent) string { return e.Actor.ID }},
	{"state.change_proposal.head_repo", func(e *dispatch.NormalizedEvent) string {
		return e.State.ChangeProposal.HeadRepo
	}},
	{"state.change_proposal.base_repo", func(e *dispatch.NormalizedEvent) string {
		return e.State.ChangeProposal.BaseRepo
	}},
	{"state.change_proposal.head_ref", func(e *dispatch.NormalizedEvent) string {
		return e.State.ChangeProposal.HeadRef
	}},
	{"state.change_proposal.base_ref", func(e *dispatch.NormalizedEvent) string {
		return e.State.ChangeProposal.BaseRef
	}},
	{"state.change_proposal.author_id", func(e *dispatch.NormalizedEvent) string {
		return e.State.ChangeProposal.AuthorID
	}},
}

// collectStringFieldPaths records the JSON path of every string and []string
// field of t (and its nested structs, pointers followed) in seen.
func collectStringFieldPaths(t reflect.Type, prefix string, seen map[string]bool) {
	switch t.Kind() {
	case reflect.String:
		if prefix != "" {
			seen[prefix] = true
		}
	case reflect.Pointer, reflect.Slice, reflect.Array:
		collectStringFieldPaths(t.Elem(), prefix, seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			collectStringFieldPaths(f.Type, path, seen)
		}
	}
}

// TestBoundTransportEventStrings_CoversEveryStringField is the drift guard
// for the hand-list in boundTransportEventStrings. A string field added to
// dispatch.NormalizedEvent or one of its nested structs must be bounded there
// (and mirrored in boundedTransportFields above) or classified here as
// bounded elsewhere, config-derived or forge-assigned/enum, else this test
// fails — so a new
// attacker-shaped field cannot ship unbounded and push the complete payload
// past maxEventPayloadChunks*dispatchPayloadChunkSize into
// buildEventPayload's comment-only fallback, the "no event" channel ADR 0112
// Decision 2's guard closes. The same shape as
// TestOverlayRequiredEventMembers_MatchSchema.
func TestBoundTransportEventStrings_CoversEveryStringField(t *testing.T) {
	bounded := make(map[string]bool, len(boundedTransportFields))
	for _, f := range boundedTransportFields {
		bounded[f.path] = true
	}
	// boundedElsewhere: strings another transport bound owns.
	boundedElsewhere := map[string]bool{
		"transition.comment.body": true, // truncateNoteBody / noteBodyTransportLimit
		"state.labels":            true, // boundTransportLabels
		// command and instruction are derived from the note body the event
		// author writes (extractCommand), but transportNormalizedEvent
		// re-derives them from the already-bounded body, and an untruncated
		// /fs-* command body fails closed on the budget.
		"transition.comment.command":     true,
		"transition.comment.instruction": true,
	}
	// forgeOrEnum: the forge or a closed schema enum picks the value; the
	// event author cannot pad it.
	forgeOrEnum := map[string]bool{
		"entity.kind":                    true,
		"entity.key":                     true,
		"transition.kind":                true,
		"transition.label.action":        true,
		"transition.review.state":        true,
		"actor.kind":                     true,
		"actor.role":                     true,
		"source.system":                  true,
		"source.raw_type":                true,
		"source.raw_action":              true,
		"state.change_proposal.head_sha": true,
	}
	// configDerived: the operator's config, not the event author, picks the
	// value, so these travel whole past transportStringRefLimit; a config
	// whose lengths push the complete payload past the chunk budget fails
	// closed there (comment-only fallback for a note, command refusal)
	// instead of being silently cut — the recorded direction for thread
	// 4538900260. head_repo/base_repo stay bounded: a fork's path is
	// event-author-chosen.
	configDerived := map[string]bool{
		"repo":                              true,
		"entity.url":                        true,
		"entity.linked_change_proposal.url": true,
	}
	seen := map[string]bool{}
	collectStringFieldPaths(reflect.TypeOf(dispatch.NormalizedEvent{}), "", seen)

	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		if bounded[path] || boundedElsewhere[path] || forgeOrEnum[path] || configDerived[path] {
			continue
		}
		t.Errorf("unclassified string field %q: bound it in boundTransportEventStrings (and mirror it in boundedTransportFields) or classify it in this test as bounded elsewhere, config-derived or forge-assigned/enum", path)
	}
	for _, classified := range []map[string]bool{bounded, boundedElsewhere, forgeOrEnum, configDerived} {
		for path := range classified {
			if !seen[path] {
				t.Errorf("classified string field %q no longer exists on dispatch.NormalizedEvent", path)
			}
		}
	}

	// The mirror must be real: with every bounded field oversized,
	// boundTransportEventStrings cuts each of them — and leaves every
	// config-derived field whole.
	oversized := strings.Repeat("<", 255)
	ev := dispatch.NormalizedEvent{
		Repo: oversized,
		Entity: dispatch.Entity{
			URL:                  oversized,
			LinkedChangeProposal: &dispatch.LinkedChangeProposal{URL: oversized},
		},
		Transition: dispatch.Transition{
			Label:  &dispatch.TransitionLabel{Name: oversized},
			Review: &dispatch.TransitionReview{ReviewerID: oversized},
		},
		Actor: dispatch.Actor{ID: oversized},
		State: dispatch.State{
			ChangeProposal: &dispatch.ChangeProposalState{
				HeadRepo: oversized,
				BaseRepo: oversized,
				HeadRef:  oversized,
				BaseRef:  oversized,
				AuthorID: oversized,
			},
		},
	}
	boundTransportEventStrings(&ev)
	for _, f := range boundedTransportFields {
		if got := f.get(&ev); len(got) == 0 || len(got) >= len(oversized) {
			t.Errorf("bounded field %q was not cut by boundTransportEventStrings (got %d of %d runes)", f.path, len(got), len(oversized))
		}
	}
	for path, got := range map[string]string{
		"repo":                              ev.Repo,
		"entity.url":                        ev.Entity.URL,
		"entity.linked_change_proposal.url": ev.Entity.LinkedChangeProposal.URL,
	} {
		if len(got) != len(oversized) {
			t.Errorf("config-derived field %q was cut by boundTransportEventStrings (got %d of %d runes), want it whole", path, len(got), len(oversized))
		}
	}
}
