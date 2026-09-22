package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func newTestPoller(client GitLabClient, opts Options) *Poller {
	if opts.PipelineRef == "" {
		opts.PipelineRef = "main"
	}
	// Default to a configured dispatch secret so poll state is signed:
	// the poller fails closed on unsigned state, so tests that don't
	// specifically exercise the unset-secret path need one. Use
	// newUnsignedTestPoller for the fail-closed cases.
	if opts.DispatchSecret == "" {
		opts.DispatchSecret = testDispatchSecret
	}
	return &Poller{
		client:      client,
		projectPath: "testgroup/testrepo",
		owner:       "testgroup",
		repo:        "testrepo",
		gitlabURL:   "https://gitlab.example.com",
		opts:        opts,
	}
}

// newUnsignedTestPoller returns a test poller with no dispatch secret
// configured, for exercising the fail-closed (refuse unsigned state)
// behavior.
func newUnsignedTestPoller(client GitLabClient) *Poller {
	p := newTestPoller(client, Options{})
	p.opts.DispatchSecret = ""
	return p
}

// withTestSecret fills DispatchSecret so Run()/discover tests fail-open
// on signed state the same way production does once the secret is set.
func withTestSecret(opts Options) Options {
	if opts.DispatchSecret == "" {
		opts.DispatchSecret = testDispatchSecret
	}
	return opts
}

// --- readWatermark tests ---

func TestReadWatermark_ReturnsStoredTime(t *testing.T) {
	stored := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	mc := newMockClient()
	mc.setPollState(persistedPollState{LastPollAtFull: stored.Format(time.RFC3339)})

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
	// No branch stored -- GetFileContentAtRef returns ErrNotFound.

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
	mc.setPollState(persistedPollState{LastPollAtFull: "not-a-timestamp"})

	p := newTestPoller(mc, Options{})
	_, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestReadWatermark_ClientError(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErr = fmt.Errorf("network failure")

	p := newTestPoller(mc, Options{})
	_, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "network failure" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestReadWatermark_FastModeUsesFastField(t *testing.T) {
	stored := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	mc := newMockClient()
	mc.setSlashState(persistedPollState{
		LastPollAtFast: stored.Format(time.RFC3339),
	})
	// Events branch has a different watermark that slash mode must ignore.
	mc.setPollState(persistedPollState{
		LastPollAtFull: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})

	p := newTestPoller(mc, Options{})
	p.slashCommandsOnly = true
	got, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(stored) {
		t.Errorf("got %v, want %v", got, stored)
	}
}

func TestStateBranch_FastMode(t *testing.T) {
	p := newTestPoller(nil, Options{})
	p.slashCommandsOnly = true
	if got := p.stateBranch(); got != PollStateBranchSlash {
		t.Errorf("got %q, want %s", got, PollStateBranchSlash)
	}
	want := hmacDomainSlash + p.projectPath + "\n"
	if got := p.hmacDomain(); got != want {
		t.Errorf("hmac domain = %q, want %q", got, want)
	}
}

func TestStateBranch_FullMode(t *testing.T) {
	p := newTestPoller(nil, Options{})
	p.slashCommandsOnly = false
	if got := p.stateBranch(); got != PollStateBranchEvents {
		t.Errorf("got %q, want %s", got, PollStateBranchEvents)
	}
	want := hmacDomainEvents + p.projectPath + "\n"
	if got := p.hmacDomain(); got != want {
		t.Errorf("hmac domain = %q, want %q", got, want)
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
	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state to be written")
	}
	if got.LastPollAtFull != want {
		t.Errorf("stored %q, want %q", got.LastPollAtFull, want)
	}
	if got.HMAC == "" {
		t.Error("expected HMAC to be set")
	}
	if _, ok := mc.getSlashState(); ok {
		t.Error("events-mode write must not touch the slash branch")
	}
}

func TestUpdateWatermark_UsesFastFieldForSlashOnly(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	p.slashCommandsOnly = true

	ts := time.Date(2025, 7, 1, 14, 30, 0, 0, time.UTC)
	err := p.updateWatermark(context.Background(), "testgroup", "testrepo", ts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := mc.getSlashState()
	if !ok {
		t.Fatal("expected slash poll state to be written")
	}
	if got.LastPollAtFast == "" {
		t.Error("expected last_poll_at_fast to be set, got nothing")
	}
	if got.LastPollAtFull != "" {
		t.Errorf("full watermark should not be on the slash branch, got %q", got.LastPollAtFull)
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("slash-mode write must not touch the events branch")
	}
}

func TestUpdateWatermark_DownloadError(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErr = fmt.Errorf("download boom")
	p := newTestPoller(mc, Options{})
	err := p.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpdateWatermark_UploadError(t *testing.T) {
	mc := newMockClient()
	mc.forceCommitErr = fmt.Errorf("upload boom")
	p := newTestPoller(mc, Options{})
	err := p.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpdateWatermark_LastWriteWins(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	t1 := time.Date(2025, 7, 1, 14, 30, 0, 0, time.UTC)
	t2 := time.Date(2025, 7, 1, 15, 0, 0, 0, time.UTC)
	if err := p.updateWatermark(context.Background(), "testgroup", "testrepo", t1); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := p.updateWatermark(context.Background(), "testgroup", "testrepo", t2); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state")
	}
	if got.LastPollAtFull != t2.Format(time.RFC3339) {
		t.Errorf("last-write-wins: got %q, want %q", got.LastPollAtFull, t2.Format(time.RFC3339))
	}
	if mc.forceCommits != 2 {
		t.Errorf("force commits = %d, want 2", mc.forceCommits)
	}
}

func TestSavePollState_ConcurrentLastWriteWins(t *testing.T) {
	mc := newMockClient()
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			p := newTestPoller(mc, Options{})
			ts := time.Date(2025, 7, 1, 0, i, 0, 0, time.UTC)
			_ = p.updateWatermark(context.Background(), "testgroup", "testrepo", ts)
		}(i)
	}
	wg.Wait()
	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state after concurrent writes")
	}
	if got.LastPollAtFull == "" {
		t.Fatal("expected a watermark from the last writer")
	}
	if got.HMAC == "" {
		t.Error("last writer must still sign the document")
	}
}

// --- HMAC / fail-closed / self-heal ---

func TestLoadPollState_AcceptsValidSignature(t *testing.T) {
	mc := newMockClient()
	writer := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	stored := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	if err := writer.updateWatermark(context.Background(), "testgroup", "testrepo", stored); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reader := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	state, err := reader.loadPollState(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("expected valid signature to verify, got error: %v", err)
	}
	if state.LastPollAtFull != stored.Format(time.RFC3339) {
		t.Errorf("LastPollAtFull = %q", state.LastPollAtFull)
	}
}

func TestLoadPollState_SelfHealOnMissingBranch(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	state, err := p.loadPollState(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("missing branch must self-heal, got error: %v", err)
	}
	if state.LastPollAtFull != "" || state.LabelState != nil {
		t.Errorf("expected empty baseline, got %+v", state)
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("load must not create the branch; the next save does")
	}
}

func TestLoadPollState_RejectsTamperedStateAndDiscardsBranch(t *testing.T) {
	mc := newMockClient()
	writer := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	if err := writer.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tampered, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state")
	}
	tampered.LastPollAtFull = time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	mc.setPollStateUnsigned(tampered)

	reader := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	if _, err := reader.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected tampered poll state to fail signature verification")
	} else if !errors.Is(err, errPollStateTampered) {
		t.Errorf("error = %v, want errPollStateTampered", err)
	}

	if _, ok := mc.getPollState(); ok {
		t.Error("tampered branch must be discarded")
	}
	if len(mc.deletedRefs) == 0 {
		t.Fatal("expected DeleteRef to be called")
	}
	wantRef := "heads/" + PollStateBranchEvents
	if mc.deletedRefs[len(mc.deletedRefs)-1] != wantRef {
		t.Errorf("deleted %v, want %s", mc.deletedRefs, wantRef)
	}
}

func TestLoadPollState_RejectsMissingSignatureAndDiscards(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateUnsigned(persistedPollState{LastPollAtFull: time.Now().Format(time.RFC3339)})

	p := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected missing signature to fail closed")
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("unsigned branch must be discarded")
	}
}

func TestLoadPollState_CorruptJSONFailsClosedAndDiscards(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateRaw(`{invalid json`)

	p := newTestPoller(mc, Options{})
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected error on corrupt poll state, got nil")
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("corrupt branch must be discarded")
	}
}

func TestLoadPollState_CorruptJSONDiscardError(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateRaw(`{invalid json`)
	mc.deleteRefErr = fmt.Errorf("delete denied")

	p := newTestPoller(mc, Options{})
	_, err := p.loadPollState(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "also failed to discard") {
		t.Errorf("error = %v, want discard failure mentioned", err)
	}
}

func TestLoadPollState_FailsClosedWhenSecretEmpty(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateUnsigned(persistedPollState{LastPollAtFull: time.Now().Format(time.RFC3339)})

	p := newUnsignedTestPoller(mc)
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected fail-closed error when no secret is configured, got nil")
	} else if !errors.Is(err, errDispatchSecretUnset) {
		t.Errorf("error = %v, want errDispatchSecretUnset", err)
	}
}

func TestLoadPollState_FailsClosedWhenSecretEmptyAndNoBranch(t *testing.T) {
	mc := newMockClient()
	p := newUnsignedTestPoller(mc)
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected fail-closed error when no secret is configured and no branch exists")
	} else if !errors.Is(err, errDispatchSecretUnset) {
		t.Errorf("error = %v, want errDispatchSecretUnset", err)
	}
}

func TestSavePollState_FailsClosedWhenSecretEmpty(t *testing.T) {
	mc := newMockClient()
	p := newUnsignedTestPoller(mc)
	if err := p.savePollState(context.Background(), "testgroup", "testrepo", persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}); err == nil {
		t.Fatal("expected fail-closed error when no secret is configured, got nil")
	} else if !errors.Is(err, errDispatchSecretUnset) {
		t.Errorf("error = %v, want errDispatchSecretUnset", err)
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("expected no poll state to be written when failing closed")
	}
}

func TestUpdateWatermark_FailsClosedWhenSecretEmpty(t *testing.T) {
	mc := newMockClient()
	p := newUnsignedTestPoller(mc)
	if err := p.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now()); err == nil {
		t.Fatal("expected fail-closed error when no secret is configured, got nil")
	} else if !errors.Is(err, errDispatchSecretUnset) {
		t.Errorf("error = %v, want errDispatchSecretUnset", err)
	}
}

func TestLoadPollState_DiscardErrorStillFailsClosed(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateUnsigned(persistedPollState{LastPollAtFull: time.Now().Format(time.RFC3339)})
	mc.deleteRefErr = fmt.Errorf("delete denied")

	p := newTestPoller(mc, Options{})
	_, err := p.loadPollState(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errPollStateTampered) {
		t.Errorf("error = %v, want to wrap errPollStateTampered", err)
	}
}

func TestLoadPollState_CrossBranchSubstitutionRejected(t *testing.T) {
	mc := newMockClient()
	slash := newTestPoller(mc, Options{})
	slash.slashCommandsOnly = true
	ts := time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := slash.updateWatermark(context.Background(), "testgroup", "testrepo", ts); err != nil {
		t.Fatalf("slash write: %v", err)
	}
	slashState, ok := mc.getSlashState()
	if !ok {
		t.Fatal("expected slash state")
	}
	// Copy the signed slash document onto the events branch.
	raw, err := json.Marshal(slashState)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	mc.putBranchFile(PollStateBranchEvents, PollStateFileName, raw)

	events := newTestPoller(mc, Options{})
	if _, err := events.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected cross-branch substitution to fail HMAC (domain separation)")
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("substituted events branch must be discarded")
	}
	if _, ok := mc.getSlashState(); !ok {
		t.Error("slash branch must be left intact")
	}
}

func TestLoadPollState_CrossProjectReplayRejected(t *testing.T) {
	mc := newMockClient()
	writer := newTestPoller(mc, Options{})
	ts := time.Date(2025, 7, 1, 12, 0, 0, 0, time.UTC)
	if err := writer.updateWatermark(context.Background(), "testgroup", "testrepo", ts); err != nil {
		t.Fatalf("write: %v", err)
	}
	signed, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state")
	}
	raw, err := json.Marshal(signed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Capture the validly-signed document and replay it against a
	// different project sharing the same FULLSEND_DISPATCH_SECRET.
	otherMC := newMockClient()
	otherMC.putBranchFile(PollStateBranchEvents, PollStateFileName, raw)
	other := newTestPoller(otherMC, Options{})
	other.projectPath = "othergroup/otherrepo"

	if _, err := other.loadPollState(context.Background(), "othergroup", "otherrepo"); err == nil {
		t.Fatal("expected cross-project replay to fail HMAC (project binding)")
	}
	if _, ok := otherMC.getPollState(); ok {
		t.Error("replayed document on the other project must be discarded")
	}
}

func TestComputeStateHMAC_Deterministic(t *testing.T) {
	state := persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}
	h1, err := computeStateHMAC("test-secret", hmacDomainEvents, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := computeStateHMAC("test-secret", hmacDomainEvents, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 != h2 {
		t.Errorf("expected deterministic HMAC, got %q vs %q", h1, h2)
	}
}

func TestComputeStateHMAC_IgnoresExistingHMACField(t *testing.T) {
	state := persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}
	h1, err := computeStateHMAC("test-secret", hmacDomainEvents, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state.HMAC = "some-stale-value"
	h2, err := computeStateHMAC("test-secret", hmacDomainEvents, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 != h2 {
		t.Error("expected computeStateHMAC to clear the HMAC field before signing")
	}
}

func TestComputeStateHMAC_DifferentSecretProducesDifferentMAC(t *testing.T) {
	state := persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}
	h1, err := computeStateHMAC("secret-a", hmacDomainEvents, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := computeStateHMAC("secret-b", hmacDomainEvents, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 == h2 {
		t.Error("expected different secrets to produce different MACs")
	}
}

func TestComputeStateHMAC_DomainSeparation(t *testing.T) {
	state := persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}
	hSlash, err := computeStateHMAC("test-secret", hmacDomainSlash, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hEvents, err := computeStateHMAC("test-secret", hmacDomainEvents, state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hSlash == hEvents {
		t.Error("expected per-branch domain separation to produce different MACs")
	}
}

func TestCrossModeIsolation_SlashWriteDoesNotAffectEvents(t *testing.T) {
	mc := newMockClient()
	eventsWatermark := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	mc.setPollState(persistedPollState{
		LastPollAtFull:     eventsWatermark.Format(time.RFC3339),
		DispatchedKeysFull: map[string]int64{"keep-full": 99},
		LabelState:         LabelState{1: {"ready-to-code"}},
	})

	slash := newTestPoller(mc, Options{})
	slash.slashCommandsOnly = true
	ts := time.Date(2025, 7, 1, 14, 30, 0, 0, time.UTC)
	if err := slash.updateWatermark(context.Background(), "testgroup", "testrepo", ts); err != nil {
		t.Fatalf("slash write: %v", err)
	}

	events, ok := mc.getPollState()
	if !ok {
		t.Fatal("events branch must remain")
	}
	if events.LastPollAtFull != eventsWatermark.Format(time.RFC3339) {
		t.Errorf("events watermark clobbered: %q", events.LastPollAtFull)
	}
	if events.DispatchedKeysFull["keep-full"] != 99 {
		t.Errorf("events dispatched keys clobbered: %v", events.DispatchedKeysFull)
	}
	if len(events.LabelState[1]) != 1 {
		t.Errorf("events label state clobbered: %v", events.LabelState)
	}

	slashState, ok := mc.getSlashState()
	if !ok {
		t.Fatal("expected slash branch")
	}
	if slashState.LastPollAtFast != ts.Format(time.RFC3339) {
		t.Errorf("slash watermark = %q", slashState.LastPollAtFast)
	}
	if slashState.LastPollAtFull != "" || slashState.LabelState != nil {
		t.Errorf("slash document must not carry events fields: %+v", slashState)
	}
}

func TestCrossModeIsolation_EventsWriteDoesNotAffectSlash(t *testing.T) {
	mc := newMockClient()
	slashWatermark := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	mc.setSlashState(persistedPollState{
		LastPollAtFast:     slashWatermark.Format(time.RFC3339),
		DispatchedKeysFast: map[string]int64{"keep-fast": 7},
	})

	events := newTestPoller(mc, Options{})
	ts := time.Date(2025, 7, 1, 14, 30, 0, 0, time.UTC)
	if err := events.updateWatermark(context.Background(), "testgroup", "testrepo", ts); err != nil {
		t.Fatalf("events write: %v", err)
	}

	slashState, ok := mc.getSlashState()
	if !ok {
		t.Fatal("slash branch must remain")
	}
	if slashState.LastPollAtFast != slashWatermark.Format(time.RFC3339) {
		t.Errorf("slash watermark clobbered: %q", slashState.LastPollAtFast)
	}
	if slashState.DispatchedKeysFast["keep-fast"] != 7 {
		t.Errorf("slash dispatched keys clobbered: %v", slashState.DispatchedKeysFast)
	}
}

// --- dispatched / failed keys ---

func TestReadDispatchedKeys_ClientError(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	_, err := p.readDispatchedKeys(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadFailedKeys_ClientError(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	_, err := p.readFailedKeys(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadDispatchedKeys_SlashMode(t *testing.T) {
	mc := newMockClient()
	mc.setSlashState(persistedPollState{
		DispatchedKeysFast: map[string]int64{"slash": 1},
	})
	mc.setPollState(persistedPollState{
		DispatchedKeysFull: map[string]int64{"full": 2},
	})
	p := newTestPoller(mc, Options{})
	p.slashCommandsOnly = true
	keys, err := p.readDispatchedKeys(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys["slash"] != 1 {
		t.Errorf("got %v, want slash=1", keys)
	}
	if _, ok := keys["full"]; ok {
		t.Error("slash mode should not return full-mode keys")
	}
}

func TestPersistDispatchedKeys_SlashMode(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{
		DispatchedKeysFull: map[string]int64{"keep-full": 99},
	})
	p := newTestPoller(mc, Options{})
	p.slashCommandsOnly = true
	ts := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := p.persistDispatchedKeys(context.Background(), "testgroup", "testrepo", map[string]int64{"slash": ts.Unix() + 1}, ts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := mc.getSlashState()
	if !ok {
		t.Fatal("expected slash poll state")
	}
	if got.DispatchedKeysFast["slash"] == 0 {
		t.Error("expected slash dispatched key")
	}
	events, ok := mc.getPollState()
	if !ok || events.DispatchedKeysFull["keep-full"] != 99 {
		t.Error("full dispatched keys should be preserved on the events branch")
	}
}

func TestPersistFailedKeys_SlashMode(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	p.slashCommandsOnly = true
	if err := p.persistFailedKeys(context.Background(), "testgroup", "testrepo", map[string]int{"n": 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := mc.getSlashState()
	if !ok {
		t.Fatal("expected poll state")
	}
	if got.FailedKeysFast["n"] != 1 {
		t.Errorf("FailedKeysFast = %v", got.FailedKeysFast)
	}
}

func TestReadFailedKeys_SlashMode(t *testing.T) {
	mc := newMockClient()
	mc.setSlashState(persistedPollState{
		FailedKeysFast: map[string]int{"slash": 1},
	})
	p := newTestPoller(mc, Options{})
	p.slashCommandsOnly = true
	keys, err := p.readFailedKeys(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if keys["slash"] != 1 {
		t.Errorf("got %v, want slash=1", keys)
	}
}

func TestPersistDispatchedKeys_DownloadError(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	ts := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := p.persistDispatchedKeys(context.Background(), "testgroup", "testrepo", map[string]int64{"k": ts.Unix()}, ts); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPersistFailedKeys_DownloadError(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	if err := p.persistFailedKeys(context.Background(), "testgroup", "testrepo", map[string]int{"k": 1}); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadFailedKeys_EmptyOnFirstRun(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	keys, err := p.readFailedKeys(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty map, got %v", keys)
	}
}

func TestPersistLabelState_DownloadError(t *testing.T) {
	mc := newMockClient()
	mc.fileContentErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	if err := p.persistLabelState(context.Background(), "testgroup", "testrepo", LabelState{1: {"ready-to-code"}}); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPersistDispatchedKeys_PrunesAndWrites(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})

	watermark := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	keys := map[string]int64{
		"keep": watermark.Unix() + 10,
		"drop": watermark.Unix() - 10,
	}
	if err := p.persistDispatchedKeys(context.Background(), "testgroup", "testrepo", keys, watermark); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state to be written")
	}
	if _, ok := got.DispatchedKeysFull["keep"]; !ok {
		t.Error("expected keep key to be persisted")
	}
	if _, ok := got.DispatchedKeysFull["drop"]; ok {
		t.Error("expected drop key to be pruned")
	}
}

func TestReadDispatchedKeys_EmptyOnFirstRun(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	keys, err := p.readDispatchedKeys(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty map, got %v", keys)
	}
}

func TestPersistFailedKeys_PrunesOverBudget(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	keys := map[string]int{
		"retry": 2,
		"done":  maxEventRetries + 1,
		"zero":  0,
	}
	if err := p.persistFailedKeys(context.Background(), "testgroup", "testrepo", keys); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state to be written")
	}
	if got.FailedKeysFull["retry"] != 2 {
		t.Errorf("retry count = %d, want 2", got.FailedKeysFull["retry"])
	}
	if _, exists := got.FailedKeysFull["done"]; exists {
		t.Error("over-budget key should be pruned")
	}
	if _, exists := got.FailedKeysFull["zero"]; exists {
		t.Error("zero-count key should be pruned")
	}
}

// --- detectNewLabels tests ---

func TestDetectNewLabels_NewLabelsDetected(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{LabelState: LabelState{1: {"ready-to-code"}}})

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
	mc.setPollState(persistedPollState{LabelState: LabelState{1: {"ready-to-code", "ready-for-review"}}})

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

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 42, Labels: []string{"ready-to-code", "bug"}},
	}

	newLabels, state, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	labels, ok := newLabels[42]
	if !ok {
		t.Fatal("expected new labels for issue 42")
	}
	if len(labels) != 1 || labels[0] != "ready-to-code" {
		t.Errorf("got %v, want [ready-to-code]", labels)
	}

	if tracked, ok := state[42]; !ok || len(tracked) != 1 {
		t.Errorf("expected state to track issue 42 with 1 label, got %v", state)
	}
}

func TestDetectNewLabels_CorruptStateFailsClosed(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateRaw(`{invalid json`)

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 5, Labels: []string{"ready-for-review"}},
	}

	if _, _, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues); err == nil {
		t.Fatal("expected error on corrupt poll state, got nil")
	}
}

func TestDetectNewLabels_PrunesClosedIssues(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{LabelState: LabelState{
		10: {"ready-to-code"},
		99: {"ready-to-code"},
	}})
	mc.issue[99] = &Issue{IID: 99, State: "closed"}

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 10, Labels: []string{"ready-to-code"}},
	}

	_, state, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", issues)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := state[99]; ok {
		t.Error("expected closed issue 99 to be pruned from state")
	}
	if _, ok := state[10]; !ok {
		t.Error("expected issue 10 to remain in state")
	}
}

func TestDetectNewLabels_DoesNotPruneOpenIssues(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{LabelState: LabelState{
		10: {"ready-to-code"},
		88: {"ready-for-review"},
	}})
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
	mc.setPollState(persistedPollState{LabelState: LabelState{7: {"ready-to-code"}}})

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
	mc.fileContentErr = fmt.Errorf("api timeout")

	p := newTestPoller(mc, Options{})
	_, _, _, err := p.detectNewLabels(context.Background(), "testgroup", "testrepo", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

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

	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state to be written")
	}

	for iid, wantLabels := range state {
		gotLabels := got.LabelState[iid]
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

func TestPersistedPollState_RoundTripJSON(t *testing.T) {
	orig := persistedPollState{
		LastPollAtFast:     "2025-01-01T00:00:00Z",
		LastPollAtFull:     "2025-01-02T00:00:00Z",
		LabelState:         LabelState{3: {"ready-to-code"}},
		DispatchedKeysFast: map[string]int64{"a": 1},
		FailedKeysFull:     map[string]int{"b": 2},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got persistedPollState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LastPollAtFast != orig.LastPollAtFast || got.LastPollAtFull != orig.LastPollAtFull {
		t.Errorf("watermarks: got %+v", got)
	}
	if got.FailedKeysFull["b"] != 2 {
		t.Errorf("failed keys: %v", got.FailedKeysFull)
	}
}

func TestDiscardPollState_NotFoundIsOK(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	if err := p.discardPollState(context.Background(), "testgroup", "testrepo", PollStateBranchEvents); err != nil {
		t.Fatalf("missing branch should not error: %v", err)
	}
}

func TestModeDocument_StripsOtherModeFields(t *testing.T) {
	full := persistedPollState{
		LastPollAtFast:     "fast",
		LastPollAtFull:     "full",
		LabelState:         LabelState{1: {"ready-to-code"}},
		DispatchedKeysFast: map[string]int64{"a": 1},
		DispatchedKeysFull: map[string]int64{"b": 2},
		FailedKeysFast:     map[string]int{"c": 1},
		FailedKeysFull:     map[string]int{"d": 2},
	}
	slash := newTestPoller(nil, Options{})
	slash.slashCommandsOnly = true
	got := slash.modeDocument(full)
	if got.LastPollAtFast != "fast" || got.DispatchedKeysFast["a"] != 1 || got.FailedKeysFast["c"] != 1 {
		t.Errorf("slash fields missing: %+v", got)
	}
	if got.LastPollAtFull != "" || got.LabelState != nil || got.DispatchedKeysFull != nil || got.FailedKeysFull != nil {
		t.Errorf("slash document leaked events fields: %+v", got)
	}

	events := newTestPoller(nil, Options{})
	got = events.modeDocument(full)
	if got.LastPollAtFull != "full" || got.DispatchedKeysFull["b"] != 2 || got.FailedKeysFull["d"] != 2 {
		t.Errorf("events fields missing: %+v", got)
	}
	if got.LastPollAtFast != "" || got.DispatchedKeysFast != nil || got.FailedKeysFast != nil {
		t.Errorf("events document leaked slash fields: %+v", got)
	}
}

// --- buildLegacyPollStateFromVars / SeedGitLabPollStateBranches ---

func TestBuildLegacyPollStateFromVars_AllFields(t *testing.T) {
	vars := map[string]string{
		forge.VarLastPollAtFast:     "2025-03-01T09:00:00Z",
		forge.VarLastPollAtFull:     "2025-03-01T08:00:00Z",
		forge.VarLabelState:         `{"42":["ready-to-code"]}`,
		forge.VarDispatchedKeysFast: `{"a":10}`,
		forge.VarDispatchedKeysFull: `{"b":20}`,
		forge.VarFailedKeysFast:     `{"c":1}`,
		forge.VarFailedKeysFull:     `{"d":2}`,
	}

	state, found := buildLegacyPollStateFromVars(vars, "group", "project")
	if !found {
		t.Fatal("expected found=true when legacy variables are present")
	}
	if state.LastPollAtFast != "2025-03-01T09:00:00Z" {
		t.Errorf("LastPollAtFast = %q", state.LastPollAtFast)
	}
	if state.LastPollAtFull != "2025-03-01T08:00:00Z" {
		t.Errorf("LastPollAtFull = %q", state.LastPollAtFull)
	}
	if got := state.LabelState[42]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Errorf("LabelState[42] = %v", got)
	}
	if state.DispatchedKeysFast["a"] != 10 {
		t.Errorf("DispatchedKeysFast = %v", state.DispatchedKeysFast)
	}
	if state.DispatchedKeysFull["b"] != 20 {
		t.Errorf("DispatchedKeysFull = %v", state.DispatchedKeysFull)
	}
	if state.FailedKeysFast["c"] != 1 {
		t.Errorf("FailedKeysFast = %v", state.FailedKeysFast)
	}
	if state.FailedKeysFull["d"] != 2 {
		t.Errorf("FailedKeysFull = %v", state.FailedKeysFull)
	}
}

func TestBuildLegacyPollStateFromVars_None(t *testing.T) {
	_, found := buildLegacyPollStateFromVars(map[string]string{"UNRELATED": "x"}, "group", "project")
	if found {
		t.Error("expected found=false when no legacy variables are present")
	}
}

func TestBuildLegacyPollStateFromVars_MalformedJSONSkipped(t *testing.T) {
	vars := map[string]string{
		forge.VarLastPollAtFull:     "2025-03-01T08:00:00Z",
		forge.VarLabelState:         "{not-json",
		forge.VarDispatchedKeysFast: "{not-json",
		forge.VarDispatchedKeysFull: "{not-json",
		forge.VarFailedKeysFast:     "{not-json",
		forge.VarFailedKeysFull:     "{not-json",
	}

	state, found := buildLegacyPollStateFromVars(vars, "group", "project")
	if !found {
		t.Fatal("expected found=true from the valid watermark variable")
	}
	if state.LabelState != nil {
		t.Errorf("expected malformed label state to be skipped, got %v", state.LabelState)
	}
	if state.DispatchedKeysFast != nil || state.DispatchedKeysFull != nil {
		t.Error("expected malformed dispatched-keys maps to be skipped")
	}
	if state.FailedKeysFast != nil || state.FailedKeysFull != nil {
		t.Error("expected malformed failed-keys maps to be skipped")
	}
}

func TestSeedGitLabPollStateBranches_MigratesLegacyVarsPerBranch(t *testing.T) {
	fc := forge.NewFakeClient()
	fast := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	full := time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC).Format(time.RFC3339)
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFast] = fast
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = full
	fc.VariableValues["testgroup/testrepo/"+forge.VarLabelState] = `{"42":["ready-to-code"]}`
	fc.VariableValues["testgroup/testrepo/"+forge.VarDispatchedKeysFast] = `{"a":10}`
	fc.VariableValues["testgroup/testrepo/"+forge.VarFailedKeysFull] = `{"d":2}`

	seeded, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seeded {
		t.Fatal("expected branches to be seeded")
	}

	slashRaw, err := fc.GetFileContentAtRef(context.Background(), "testgroup", "testrepo", PollStateFileName, PollStateBranchSlash)
	if err != nil {
		t.Fatalf("slash branch: %v", err)
	}
	var slash persistedPollState
	if err := json.Unmarshal(slashRaw, &slash); err != nil {
		t.Fatalf("unmarshal slash: %v", err)
	}
	if slash.LastPollAtFast != fast {
		t.Errorf("slash LastPollAtFast = %q, want %q", slash.LastPollAtFast, fast)
	}
	if slash.DispatchedKeysFast["a"] != 10 {
		t.Errorf("slash DispatchedKeysFast = %v", slash.DispatchedKeysFast)
	}
	if slash.LastPollAtFull != "" || slash.LabelState != nil || slash.FailedKeysFull != nil {
		t.Errorf("slash leaked events fields: %+v", slash)
	}
	wantSlash, err := computeStateHMAC("test-secret", hmacDomainFor(PollStateBranchSlash, "testgroup/testrepo"), slash)
	if err != nil {
		t.Fatalf("compute slash HMAC: %v", err)
	}
	if slash.HMAC != wantSlash {
		t.Error("slash HMAC does not match the signature the runtime poller would verify")
	}

	eventsRaw, err := fc.GetFileContentAtRef(context.Background(), "testgroup", "testrepo", PollStateFileName, PollStateBranchEvents)
	if err != nil {
		t.Fatalf("events branch: %v", err)
	}
	var events persistedPollState
	if err := json.Unmarshal(eventsRaw, &events); err != nil {
		t.Fatalf("unmarshal events: %v", err)
	}
	if events.LastPollAtFull != full {
		t.Errorf("events LastPollAtFull = %q, want %q", events.LastPollAtFull, full)
	}
	if got := events.LabelState[42]; len(got) != 1 || got[0] != "ready-to-code" {
		t.Errorf("events LabelState[42] = %v", got)
	}
	if events.FailedKeysFull["d"] != 2 {
		t.Errorf("events FailedKeysFull = %v", events.FailedKeysFull)
	}
	if events.LastPollAtFast != "" || events.DispatchedKeysFast != nil {
		t.Errorf("events leaked slash fields: %+v", events)
	}
	wantEvents, err := computeStateHMAC("test-secret", hmacDomainFor(PollStateBranchEvents, "testgroup/testrepo"), events)
	if err != nil {
		t.Fatalf("compute events HMAC: %v", err)
	}
	if events.HMAC != wantEvents {
		t.Error("events HMAC does not match the signature the runtime poller would verify")
	}
}

func TestSeedGitLabPollStateBranches_EmptyBaselineWhenNoLegacyVars(t *testing.T) {
	fc := forge.NewFakeClient()

	seeded, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seeded {
		t.Fatal("expected empty baseline to be written when no legacy vars exist")
	}

	for _, branch := range []string{PollStateBranchSlash, PollStateBranchEvents} {
		raw, err := fc.GetFileContentAtRef(context.Background(), "testgroup", "testrepo", PollStateFileName, branch)
		if err != nil {
			t.Fatalf("%s: %v", branch, err)
		}
		var state persistedPollState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("unmarshal %s: %v", branch, err)
		}
		if state.LastPollAtFast != "" || state.LastPollAtFull != "" || state.LabelState != nil {
			t.Errorf("%s: expected empty baseline, got %+v", branch, state)
		}
		if state.HMAC == "" {
			t.Errorf("%s: empty baseline must still be signed", branch)
		}
		want, err := computeStateHMAC("test-secret", hmacDomainFor(branch, "testgroup/testrepo"), state)
		if err != nil {
			t.Fatalf("compute HMAC %s: %v", branch, err)
		}
		if state.HMAC != want {
			t.Errorf("%s: HMAC mismatch", branch)
		}
	}
}

func TestSeedGitLabPollStateBranches_SkipsExistingBranch(t *testing.T) {
	fc := forge.NewFakeClient()
	existing := []byte(`{"last_poll_at_fast":"2025-06-01T00:00:00Z","hmac":"keep-me"}`)
	if err := fc.ForceCommitFileToBranch(context.Background(), "testgroup", "testrepo", PollStateBranchSlash, PollStateFileName, "prior", existing); err != nil {
		t.Fatalf("seed existing slash branch: %v", err)
	}
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFast] = "2025-03-01T09:00:00Z"
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = "2025-03-01T08:00:00Z"

	seeded, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seeded {
		t.Fatal("expected events branch to be seeded even when slash already exists")
	}

	slashRaw, err := fc.GetFileContentAtRef(context.Background(), "testgroup", "testrepo", PollStateFileName, PollStateBranchSlash)
	if err != nil {
		t.Fatalf("slash: %v", err)
	}
	if string(slashRaw) != string(existing) {
		t.Errorf("existing slash document was clobbered: %s", slashRaw)
	}
	if _, err := fc.GetFileContentAtRef(context.Background(), "testgroup", "testrepo", PollStateFileName, PollStateBranchEvents); err != nil {
		t.Errorf("events branch should have been created: %v", err)
	}
}

func TestSeedGitLabPollStateBranches_NoopWhenBothBranchesExist(t *testing.T) {
	fc := forge.NewFakeClient()
	for _, branch := range []string{PollStateBranchSlash, PollStateBranchEvents} {
		if err := fc.ForceCommitFileToBranch(context.Background(), "testgroup", "testrepo", branch, PollStateFileName, "prior", []byte(`{"hmac":"keep"}`)); err != nil {
			t.Fatalf("seed %s: %v", branch, err)
		}
	}
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = "2025-03-01T08:00:00Z"

	seeded, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seeded {
		t.Error("expected no-op when both branches already exist")
	}
}

func TestSeedGitLabPollStateBranches_RuntimePollerAcceptsMigratedState(t *testing.T) {
	fc := forge.NewFakeClient()
	fast := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	full := time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC).Format(time.RFC3339)
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFast] = fast
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = full

	if _, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", testDispatchSecret); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mc := newMockClient()
	for _, branch := range []string{PollStateBranchSlash, PollStateBranchEvents} {
		raw, err := fc.GetFileContentAtRef(context.Background(), "testgroup", "testrepo", PollStateFileName, branch)
		if err != nil {
			t.Fatalf("%s: %v", branch, err)
		}
		mc.putBranchFile(branch, PollStateFileName, raw)
	}

	slash := newTestPoller(mc, Options{DispatchSecret: testDispatchSecret})
	slash.slashCommandsOnly = true
	got, err := slash.readWatermark(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("slash load: %v", err)
	}
	wantFast, _ := time.Parse(time.RFC3339, fast)
	if !got.Equal(wantFast) {
		t.Errorf("slash watermark = %v, want %v", got, wantFast)
	}

	events := newTestPoller(mc, Options{DispatchSecret: testDispatchSecret})
	got, err = events.readWatermark(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("events load: %v", err)
	}
	wantFull, _ := time.Parse(time.RFC3339, full)
	if !got.Equal(wantFull) {
		t.Errorf("events watermark = %v, want %v", got, wantFull)
	}
}

func TestSeedGitLabPollStateBranches_EmptySecretRefused(t *testing.T) {
	fc := forge.NewFakeClient()
	_, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "")
	if !errors.Is(err, errDispatchSecretUnset) {
		t.Errorf("error = %v, want errDispatchSecretUnset", err)
	}
}

func TestSeedGitLabPollStateBranches_ListVarsErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepoVariables"] = fmt.Errorf("forbidden")
	_, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected a variable-listing error to propagate")
	}
}

func TestSeedGitLabPollStateBranches_GetFileErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["GetFileContentAtRef"] = fmt.Errorf("boom")
	_, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected a non-NotFound read error to propagate")
	}
}

func TestSeedGitLabPollStateBranches_ForceCommitErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ForceCommitFileToBranch"] = fmt.Errorf("denied")
	_, err := SeedGitLabPollStateBranches(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected a force-commit error to propagate")
	}
}

func TestEnsureDispatchSecret_GeneratesWhenMissing(t *testing.T) {
	fc := forge.NewFakeClient()
	secret, created, err := EnsureDispatchSecret(context.Background(), fc, "group", "project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Error("expected created=true when no secret exists")
	}
	if secret == "" {
		t.Fatal("expected a generated secret")
	}
	if len(fc.CreatedSecrets) != 1 || fc.CreatedSecrets[0].Name != forge.SecretDispatch {
		t.Errorf("CreatedSecrets = %+v, want one FULLSEND_DISPATCH_SECRET", fc.CreatedSecrets)
	}
	if fc.CreatedSecrets[0].Value != secret {
		t.Error("stored secret does not match returned value")
	}
}

func TestEnsureDispatchSecret_ReusesExisting(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["group/project/"+forge.SecretDispatch] = "existing-secret"
	secret, created, err := EnsureDispatchSecret(context.Background(), fc, "group", "project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Error("expected created=false when a secret already exists")
	}
	if secret != "existing-secret" {
		t.Errorf("secret = %q, want existing-secret", secret)
	}
	if len(fc.CreatedSecrets) != 0 {
		t.Errorf("should not generate a new secret, got %+v", fc.CreatedSecrets)
	}
}

func TestEnsureDispatchSecret_ListError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepoVariables"] = fmt.Errorf("forbidden")
	_, _, err := EnsureDispatchSecret(context.Background(), fc, "group", "project")
	if err == nil {
		t.Fatal("expected list error to propagate")
	}
}

func TestEnsureDispatchSecret_CreateError(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["CreateRepoSecret"] = fmt.Errorf("denied")
	_, _, err := EnsureDispatchSecret(context.Background(), fc, "group", "project")
	if err == nil {
		t.Fatal("expected create error to propagate")
	}
}
