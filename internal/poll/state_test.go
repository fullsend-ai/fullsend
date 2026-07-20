package poll

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func newTestPoller(client GitLabClient, opts Options) *Poller {
	return &Poller{
		client: client,
		owner:  "testgroup",
		repo:   "testrepo",
		opts:   opts,
	}
}

// --- readWatermark tests ---

func TestReadWatermark_ReturnsStoredTime(t *testing.T) {
	stored := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = stored.Format(time.RFC3339)

	p := newTestPoller(mc, Options{})
	got, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(stored) {
		t.Errorf("got %v, want %v", got, stored)
	}
}

func TestReadWatermark_FirstRunDefaultsToOneHourAgo(t *testing.T) {
	mc := newMockClient()
	// No variable stored -- GetCIVariable returns ErrNotFound.

	p := newTestPoller(mc, Options{})
	before := time.Now().Add(-1*time.Hour - time.Second)
	got, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := time.Now().Add(-1*time.Hour + time.Second)

	if got.Before(before) || got.After(after) {
		t.Errorf("expected watermark ~1 hour ago, got %v (window %v to %v)", got, before, after)
	}
}

func TestReadWatermark_ParseError(t *testing.T) {
	mc := newMockClient()
	mc.variables["FULLSEND_LAST_POLL_AT_FULL"] = "not-a-timestamp"

	p := newTestPoller(mc, Options{})
	_, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestReadWatermark_ClientError(t *testing.T) {
	mc := newMockClient()
	mc.variableErr["FULLSEND_LAST_POLL_AT_FULL"] = fmt.Errorf("network failure")

	p := newTestPoller(mc, Options{})
	_, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "network failure" {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- watermarkVarName tests ---

func TestWatermarkVarName_FastMode(t *testing.T) {
	p := newTestPoller(nil, Options{SlashCommandsOnly: true})
	got := p.watermarkVarName()
	if got != "FULLSEND_LAST_POLL_AT_FAST" {
		t.Errorf("got %q, want FULLSEND_LAST_POLL_AT_FAST", got)
	}
}

func TestWatermarkVarName_FullMode(t *testing.T) {
	p := newTestPoller(nil, Options{SlashCommandsOnly: false})
	got := p.watermarkVarName()
	if got != "FULLSEND_LAST_POLL_AT_FULL" {
		t.Errorf("got %q, want FULLSEND_LAST_POLL_AT_FULL", got)
	}
}

// --- updateWatermark tests ---

func TestUpdateWatermark_StoresRFC3339(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	ts := time.Date(2025, 7, 1, 14, 30, 0, 0, time.UTC)
	err := p.updateWatermark(context.Background(), "testgroup", "testrepo", ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := ts.Format(time.RFC3339)
	got := mc.updatedVars["FULLSEND_LAST_POLL_AT_FULL"]
	if got != want {
		t.Errorf("stored %q, want %q", got, want)
	}
}

func TestUpdateWatermark_UsesFastVarForSlashOnly(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{SlashCommandsOnly: true})

	ts := time.Date(2025, 7, 1, 14, 30, 0, 0, time.UTC)
	err := p.updateWatermark(context.Background(), "testgroup", "testrepo", ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := mc.updatedVars["FULLSEND_LAST_POLL_AT_FAST"]; !ok {
		t.Error("expected FULLSEND_LAST_POLL_AT_FAST to be set, got nothing")
	}
}

// --- detectNewLabels tests ---

func TestDetectNewLabels_NewLabelsDetected(t *testing.T) {
	mc := newMockClient()
	// Pre-existing state: issue 1 had "ready-to-code"
	mc.variables["FULLSEND_LABEL_STATE"] = `{"1":["ready-to-code"]}`

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 1, Labels: []string{"ready-to-code", "ready-for-review"}},
	}

	newLabels, _, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	labels, ok := newLabels[1]
	if !ok {
		t.Fatal("expected new labels for issue 1")
	}
	if len(labels) != 1 || labels[0] != "ready-for-review" {
		t.Errorf("got %v, want [ready-for-review]", labels)
	}
}

func TestDetectNewLabels_PreExistingNotRedetected(t *testing.T) {
	mc := newMockClient()
	// Issue 1 already had both routable labels.
	mc.variables["FULLSEND_LABEL_STATE"] = `{"1":["ready-to-code","ready-for-review"]}`

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 1, Labels: []string{"ready-to-code", "ready-for-review"}},
	}

	newLabels, _, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(newLabels) != 0 {
		t.Errorf("expected no new labels, got %v", newLabels)
	}
}

func TestDetectNewLabels_FirstRunNoStoredState(t *testing.T) {
	mc := newMockClient()
	// No FULLSEND_LABEL_STATE variable -- GetCIVariable returns ErrNotFound.

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 42, Labels: []string{"ready-to-code", "bug"}},
	}

	newLabels, state, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// On first run, all routable labels are "new".
	labels, ok := newLabels[42]
	if !ok {
		t.Fatal("expected new labels for issue 42")
	}
	if len(labels) != 1 || labels[0] != "ready-to-code" {
		t.Errorf("got %v, want [ready-to-code]", labels)
	}

	// State should now track the routable labels.
	if tracked, ok := state[42]; !ok || len(tracked) != 1 {
		t.Errorf("expected state to track issue 42 with 1 label, got %v", state)
	}
}

func TestDetectNewLabels_CorruptStateGracefulDegradation(t *testing.T) {
	mc := newMockClient()
	mc.variables["FULLSEND_LABEL_STATE"] = `{invalid json`

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 5, Labels: []string{"ready-for-review"}},
	}

	newLabels, _, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}

	// With corrupt state treated as empty, all routable labels are "new".
	labels, ok := newLabels[5]
	if !ok {
		t.Fatal("expected new labels for issue 5")
	}
	if len(labels) != 1 || labels[0] != "ready-for-review" {
		t.Errorf("got %v, want [ready-for-review]", labels)
	}
}

func TestDetectNewLabels_PrunesClosedIssues(t *testing.T) {
	mc := newMockClient()
	// State has issue 99 which is NOT in the current poll set.
	mc.variables["FULLSEND_LABEL_STATE"] = `{"10":["ready-to-code"],"99":["ready-to-code"]}`
	// Issue 99 is closed.
	mc.issue[99] = &Issue{IID: 99, State: "closed"}

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 10, Labels: []string{"ready-to-code"}},
	}

	_, state, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Issue 99 should be pruned from state.
	if _, ok := state[99]; ok {
		t.Error("expected closed issue 99 to be pruned from state")
	}
	// Issue 10 should remain.
	if _, ok := state[10]; !ok {
		t.Error("expected issue 10 to remain in state")
	}
}

func TestDetectNewLabels_DoesNotPruneOpenIssues(t *testing.T) {
	mc := newMockClient()
	mc.variables["FULLSEND_LABEL_STATE"] = `{"10":["ready-to-code"],"88":["ready-for-review"]}`
	// Issue 88 is open -- not in poll set but not closed.
	mc.issue[88] = &Issue{IID: 88, State: "opened"}

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 10, Labels: []string{"ready-to-code"}},
	}

	_, state, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := state[88]; !ok {
		t.Error("expected open issue 88 to remain in state")
	}
}

func TestDetectNewLabels_PreviousLabelsSnapshot(t *testing.T) {
	mc := newMockClient()
	mc.variables["FULLSEND_LABEL_STATE"] = `{"7":["ready-to-code"]}`

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 7, Labels: []string{"ready-to-code", "ready-for-review"}},
	}

	_, _, previousLabels, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prev, ok := previousLabels[7]
	if !ok {
		t.Fatal("expected previous labels for issue 7")
	}
	if len(prev) != 1 || prev[0] != "ready-to-code" {
		t.Errorf("got %v, want [ready-to-code]", prev)
	}
}

func TestDetectNewLabels_ClientError(t *testing.T) {
	mc := newMockClient()
	mc.variableErr["FULLSEND_LABEL_STATE"] = fmt.Errorf("api timeout")

	p := newTestPoller(mc, Options{})
	_, _, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- persistLabelState tests ---

func TestPersistLabelState_MarshalsAndWrites(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	state := LabelState{
		1: {"ready-to-code"},
		2: {"ready-for-review", "ready-to-code"},
	}

	err := p.persistLabelState(context.Background(), "testgroup", "testrepo", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := mc.updatedVars["FULLSEND_LABEL_STATE"]
	if !ok {
		t.Fatal("expected FULLSEND_LABEL_STATE to be set")
	}

	var roundtripped LabelState
	if err := json.Unmarshal([]byte(raw), &roundtripped); err != nil {
		t.Fatalf("stored value is not valid JSON: %v", err)
	}

	for iid, wantLabels := range state {
		gotLabels := roundtripped[iid]
		if len(gotLabels) != len(wantLabels) {
			t.Errorf("issue %d: got %v, want %v", iid, gotLabels, wantLabels)
		}
	}
}

// --- isIssueClosed tests ---

func TestIsIssueClosed_ReturnsTrue(t *testing.T) {
	mc := newMockClient()
	mc.issue[42] = &Issue{IID: 42, State: "closed"}

	p := newTestPoller(mc, Options{})
	if !p.isIssueClosed(context.Background(), "testgroup", "testrepo", 42) {
		t.Error("expected true for closed issue")
	}
}

func TestIsIssueClosed_ReturnsFalseForOpen(t *testing.T) {
	mc := newMockClient()
	mc.issue[42] = &Issue{IID: 42, State: "opened"}

	p := newTestPoller(mc, Options{})
	if p.isIssueClosed(context.Background(), "testgroup", "testrepo", 42) {
		t.Error("expected false for open issue")
	}
}

func TestIsIssueClosed_ReturnsFalseOnError(t *testing.T) {
	mc := newMockClient()
	mc.issueErr[42] = fmt.Errorf("server error")

	p := newTestPoller(mc, Options{})
	if p.isIssueClosed(context.Background(), "testgroup", "testrepo", 42) {
		t.Error("expected false on error")
	}
}

func TestIsIssueClosed_ReturnsFalseOnNotFound(t *testing.T) {
	mc := newMockClient()
	// No issue[42] entry -- GetIssue returns forge.ErrNotFound.

	p := newTestPoller(mc, Options{})
	if p.isIssueClosed(context.Background(), "testgroup", "testrepo", 42) {
		t.Error("expected false when issue not found")
	}
}

// --- toSet tests ---

func TestToSet_BasicConversion(t *testing.T) {
	s := toSet([]string{"a", "b", "c"})
	if len(s) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(s))
	}
	for _, k := range []string{"a", "b", "c"} {
		if !s[k] {
			t.Errorf("expected %q in set", k)
		}
	}
}

func TestToSet_EmptySlice(t *testing.T) {
	s := toSet([]string{})
	if len(s) != 0 {
		t.Errorf("expected empty set, got %d elements", len(s))
	}
}

func TestToSet_NilSlice(t *testing.T) {
	s := toSet(nil)
	if s == nil {
		t.Fatal("expected non-nil map")
	}
	if len(s) != 0 {
		t.Errorf("expected empty set, got %d elements", len(s))
	}
}

func TestToSet_Duplicates(t *testing.T) {
	s := toSet([]string{"x", "x", "y"})
	if len(s) != 2 {
		t.Errorf("expected 2 elements, got %d", len(s))
	}
}
