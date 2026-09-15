package poll

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// No package stored -- DownloadPackageFile returns ErrNotFound.

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
	mc.packageDownloadErr = fmt.Errorf("network failure")

	p := newTestPoller(mc, Options{})
	_, err := p.readWatermark(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() != "network failure" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestUpdateWatermark_DownloadError(t *testing.T) {
	mc := newMockClient()
	mc.packageDownloadErr = fmt.Errorf("download boom")
	p := newTestPoller(mc, Options{})
	err := p.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestUpdateWatermark_UploadError(t *testing.T) {
	mc := newMockClient()
	mc.packageUploadErr = fmt.Errorf("upload boom")
	p := newTestPoller(mc, Options{})
	err := p.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadDispatchedKeys_ClientError(t *testing.T) {
	mc := newMockClient()
	mc.packageDownloadErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	_, err := p.readDispatchedKeys(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadFailedKeys_ClientError(t *testing.T) {
	mc := newMockClient()
	mc.packageDownloadErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	_, err := p.readFailedKeys(context.Background(), "testgroup", "testrepo")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadDispatchedKeys_SlashMode(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{
		DispatchedKeysFast: map[string]int64{"slash": 1},
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
	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state")
	}
	if got.DispatchedKeysFast["slash"] == 0 {
		t.Error("expected slash dispatched key")
	}
	if got.DispatchedKeysFull["keep-full"] != 99 {
		t.Error("full dispatched keys should be preserved")
	}
}

func TestPersistFailedKeys_SlashMode(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{})
	p.slashCommandsOnly = true
	if err := p.persistFailedKeys(context.Background(), "testgroup", "testrepo", map[string]int{"n": 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state")
	}
	if got.FailedKeysFast["n"] != 1 {
		t.Errorf("FailedKeysFast = %v", got.FailedKeysFast)
	}
}

func TestReadFailedKeys_SlashMode(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{
		FailedKeysFast: map[string]int{"slash": 1},
		FailedKeysFull: map[string]int{"full": 2},
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
	mc.packageDownloadErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	ts := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := p.persistDispatchedKeys(context.Background(), "testgroup", "testrepo", map[string]int64{"k": ts.Unix()}, ts); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPersistFailedKeys_DownloadError(t *testing.T) {
	mc := newMockClient()
	mc.packageDownloadErr = fmt.Errorf("timeout")
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
	mc.packageDownloadErr = fmt.Errorf("timeout")
	p := newTestPoller(mc, Options{})
	if err := p.persistLabelState(context.Background(), "testgroup", "testrepo", LabelState{1: {"ready-to-code"}}); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestReadWatermark_FastModeUsesFastField(t *testing.T) {
	stored := time.Date(2025, 6, 15, 10, 0, 0, 0, time.UTC)
	mc := newMockClient()
	mc.setPollState(persistedPollState{
		LastPollAtFast: stored.Format(time.RFC3339),
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

func TestLoadPollState_CorruptJSONFailsClosed(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateRaw(`{invalid json`)

	p := newTestPoller(mc, Options{})
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected error on corrupt poll state, got nil")
	}
}

// --- legacy CI/CD variable migration tests ---

func TestLoadPollState_MigratesLegacyCIVariables(t *testing.T) {
	mc := newMockClient()
	watermark := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	mc.ciVariables[forge.VarLastPollAtFull] = watermark
	mc.ciVariables[forge.VarLabelState] = `{"42":["ready-to-code"]}`
	mc.ciVariables[forge.VarDispatchedKeysFull] = `{"issue-42":1700000000}`
	mc.ciVariables[forge.VarFailedKeysFull] = `{"issue-7":2}`

	p := newTestPoller(mc, Options{})
	state, err := p.loadPollState(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.LastPollAtFull != watermark {
		t.Errorf("LastPollAtFull = %q, want %q", state.LastPollAtFull, watermark)
	}
	if len(state.LabelState[42]) != 1 || state.LabelState[42][0] != "ready-to-code" {
		t.Errorf("LabelState = %+v, want issue 42 -> [ready-to-code]", state.LabelState)
	}
	if state.DispatchedKeysFull["issue-42"] != 1700000000 {
		t.Errorf("DispatchedKeysFull = %+v", state.DispatchedKeysFull)
	}
	if state.FailedKeysFull["issue-7"] != 2 {
		t.Errorf("FailedKeysFull = %+v", state.FailedKeysFull)
	}

	// The migrated document must be persisted so subsequent polls read
	// the package file directly instead of re-migrating.
	saved, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected migrated state to be persisted to the package file")
	}
	if saved.LastPollAtFull != watermark {
		t.Errorf("persisted LastPollAtFull = %q, want %q", saved.LastPollAtFull, watermark)
	}
}

func TestLoadPollState_NoLegacyVariablesStartsFresh(t *testing.T) {
	mc := newMockClient()

	p := newTestPoller(mc, Options{})
	state, err := p.loadPollState(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.LastPollAtFull != "" || state.LabelState != nil {
		t.Errorf("expected empty state for a genuinely new install, got %+v", state)
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("expected no package file to be written when there is nothing to migrate")
	}
}

func TestLoadPollState_LegacyVariableTransientErrorPropagates(t *testing.T) {
	mc := newMockClient()
	mc.ciVarErr[forge.VarLastPollAtFull] = fmt.Errorf("timeout")

	p := newTestPoller(mc, Options{})
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected error to propagate on transient legacy-variable read failure, got nil")
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("expected no package file to be written on migration failure")
	}
}

// TestLoadPollState_LegacyVariableForbiddenTreatedAsAbsent covers the
// review finding that a Developer-level bot PAT (the access level this
// PR creates) gets ErrForbidden, not ErrNotFound, from GitLab's
// project-level CI/CD Variables API — even for variables that don't
// exist. Without this, every GitLab repo's first poll cycle would hit
// this branch and abort Run() permanently instead of starting fresh.
func TestLoadPollState_LegacyVariableForbiddenTreatedAsAbsent(t *testing.T) {
	mc := newMockClient()
	mc.ciVarErr[forge.VarLastPollAtFull] = forge.ErrForbidden
	mc.ciVarErr[forge.VarLastPollAtFast] = forge.ErrForbidden
	mc.ciVarErr[forge.VarLabelState] = forge.ErrForbidden
	mc.ciVarErr[forge.VarDispatchedKeysFast] = forge.ErrForbidden
	mc.ciVarErr[forge.VarDispatchedKeysFull] = forge.ErrForbidden
	mc.ciVarErr[forge.VarFailedKeysFast] = forge.ErrForbidden
	mc.ciVarErr[forge.VarFailedKeysFull] = forge.ErrForbidden

	p := newTestPoller(mc, Options{})
	state, err := p.loadPollState(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("expected ErrForbidden to be treated as absent, got error: %v", err)
	}
	if state.LastPollAtFull != "" || state.LabelState != nil {
		t.Errorf("expected empty state, got %+v", state)
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("expected no package file to be written when every legacy variable is forbidden")
	}
}

// TestLoadPollState_LegacyVariableForbiddenAmongOthersMigratesRest
// confirms a mix of a readable legacy variable and a forbidden one
// still migrates the readable one instead of failing the whole cycle.
func TestLoadPollState_LegacyVariableForbiddenAmongOthersMigratesRest(t *testing.T) {
	mc := newMockClient()
	watermark := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	mc.ciVariables[forge.VarLastPollAtFull] = watermark
	mc.ciVarErr[forge.VarLabelState] = forge.ErrForbidden

	p := newTestPoller(mc, Options{})
	state, err := p.loadPollState(context.Background(), "testgroup", "testrepo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.LastPollAtFull != watermark {
		t.Errorf("LastPollAtFull = %q, want %q", state.LastPollAtFull, watermark)
	}
	if state.LabelState != nil {
		t.Errorf("expected no label state migrated, got %+v", state.LabelState)
	}
}

// --- poll state HMAC signing tests ---

func TestSavePollState_SignsWhenSecretSet(t *testing.T) {
	mc := newMockClient()
	p := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	if err := p.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	saved, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state")
	}
	if saved.HMAC == "" {
		t.Error("expected HMAC to be set when DispatchSecret is configured")
	}
}

func TestSavePollState_FailsClosedWhenSecretEmpty(t *testing.T) {
	mc := newMockClient()
	p := newUnsignedTestPoller(mc)
	// With no secret configured, writing state must fail closed rather
	// than persist an unsigned (forgeable) document.
	if err := p.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now()); err == nil {
		t.Fatal("expected fail-closed error when no secret is configured, got nil")
	}
	if _, ok := mc.getPollState(); ok {
		t.Error("expected no poll state to be written when failing closed")
	}
}

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

func TestLoadPollState_RejectsTamperedState(t *testing.T) {
	mc := newMockClient()
	writer := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	if err := writer.updateWatermark(context.Background(), "testgroup", "testrepo", time.Now()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Simulate a forged document: a Developer-level actor advances the
	// watermark directly without knowing the signing secret.
	tampered, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state")
	}
	tampered.LastPollAtFull = time.Now().Add(48 * time.Hour).Format(time.RFC3339)
	// Write the mutated document back WITHOUT re-signing (the actor does
	// not know the secret), preserving the now-stale signature.
	mc.setPollStateUnsigned(tampered)

	reader := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	if _, err := reader.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected tampered poll state to fail signature verification, got nil error")
	}
}

func TestLoadPollState_RejectsMissingSignatureWhenSecretConfigured(t *testing.T) {
	mc := newMockClient()
	// Written without a secret (e.g. an actor bypassing the signing
	// poller entirely), but read back with one configured.
	mc.setPollStateUnsigned(persistedPollState{LastPollAtFull: time.Now().Format(time.RFC3339)})

	p := newTestPoller(mc, Options{DispatchSecret: "test-secret"})
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected missing signature to fail closed when a secret is configured, got nil error")
	}
}

func TestLoadPollState_FailsClosedWhenSecretEmpty(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateUnsigned(persistedPollState{LastPollAtFull: time.Now().Format(time.RFC3339)})

	// With no secret configured, an existing (Developer-writable, hence
	// untrusted) state document must not be loaded — fail closed rather
	// than trust unsigned state.
	p := newUnsignedTestPoller(mc)
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected fail-closed error when no secret is configured, got nil")
	}
}

func TestLoadPollState_FailsClosedWhenSecretEmptyAndNoPackage(t *testing.T) {
	// No package exists at all, so loadPollState would otherwise take the
	// ErrNotFound → migrateLegacyPollState path, which returns empty state
	// with a nil error. With an unset secret that must still fail closed
	// *before* any event discovery/dispatch — otherwise the cycle would
	// discover, dispatch (irreversibly), fail to persist, and re-dispatch
	// the same events every cycle.
	mc := newMockClient()
	p := newUnsignedTestPoller(mc)
	if _, err := p.loadPollState(context.Background(), "testgroup", "testrepo"); err == nil {
		t.Fatal("expected fail-closed error when no secret is configured and no package exists, got nil")
	}
}

func TestComputeStateHMAC_Deterministic(t *testing.T) {
	state := persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}
	h1, err := computeStateHMAC("test-secret", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := computeStateHMAC("test-secret", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 != h2 {
		t.Errorf("expected deterministic HMAC, got %q vs %q", h1, h2)
	}
}

func TestComputeStateHMAC_IgnoresExistingHMACField(t *testing.T) {
	state := persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}
	h1, err := computeStateHMAC("test-secret", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	state.HMAC = "some-stale-value"
	h2, err := computeStateHMAC("test-secret", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 != h2 {
		t.Error("expected computeStateHMAC to clear the HMAC field before signing")
	}
}

func TestComputeStateHMAC_DifferentSecretProducesDifferentMAC(t *testing.T) {
	state := persistedPollState{LastPollAtFull: "2025-01-01T00:00:00Z"}
	h1, err := computeStateHMAC("secret-a", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := computeStateHMAC("secret-b", state)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 == h2 {
		t.Error("expected different secrets to produce different MACs")
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

	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state to be written")
	}
	if got.LastPollAtFast == "" {
		t.Error("expected last_poll_at_fast to be set, got nothing")
	}
	if got.LastPollAtFull != "" {
		t.Errorf("full watermark should be untouched, got %q", got.LastPollAtFull)
	}
}

func TestUpdateWatermark_PreservesOtherFields(t *testing.T) {
	mc := newMockClient()
	mc.setPollState(persistedPollState{
		LastPollAtFast: "2025-01-01T00:00:00Z",
		LabelState:     LabelState{1: {"ready-to-code"}},
	})
	p := newTestPoller(mc, Options{})

	ts := time.Date(2025, 7, 1, 14, 30, 0, 0, time.UTC)
	if err := p.updateWatermark(context.Background(), "testgroup", "testrepo", ts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, ok := mc.getPollState()
	if !ok {
		t.Fatal("expected poll state to be written")
	}
	if got.LastPollAtFast != "2025-01-01T00:00:00Z" {
		t.Errorf("fast watermark overwritten: %q", got.LastPollAtFast)
	}
	if len(got.LabelState[1]) != 1 || got.LabelState[1][0] != "ready-to-code" {
		t.Errorf("label state overwritten: %v", got.LabelState)
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

func TestDetectNewLabels_CorruptStatePropagatesError(t *testing.T) {
	mc := newMockClient()
	mc.setPollStateRaw(`{invalid json`)

	p := newTestPoller(mc, Options{})
	issues := []Issue{
		{IID: 5, Labels: []string{"ready-for-review"}},
	}

	// Fail closed: corrupt state must abort the cycle rather than be
	// treated as empty, which would re-dispatch every routable label.
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
	mc.packageDownloadErr = fmt.Errorf("api timeout")

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

// --- buildLegacyPollStateFromVars tests ---

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
	// Malformed JSON map fields are logged and skipped; a scalar
	// watermark still marks the state as found so migration proceeds.
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

// --- SeedGitLabPollStateFromLegacyVars tests ---
//
// This is the install/converge-time counterpart to migrateLegacyPollState:
// it uses a Maintainer-or-higher forge.Client (forge.FakeClient here
// stands in for the operator's own client, as opposed to mockClient
// which models the Developer-level bot PAT's narrower GitLabClient
// surface) so it can read legacy CI/CD variables that a Developer-level
// bot PAT cannot. See the logic-error finding on PR #7317.

func TestSeedGitLabPollStateFromLegacyVars_SeedsAndSigns(t *testing.T) {
	fc := forge.NewFakeClient()
	watermark := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = watermark
	fc.VariableValues["testgroup/testrepo/"+forge.VarLabelState] = `{"42":["ready-to-code"]}`

	seeded, err := SeedGitLabPollStateFromLegacyVars(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seeded {
		t.Fatal("expected state to be seeded")
	}

	data, err := fc.DownloadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName)
	if err != nil {
		t.Fatalf("expected package file to be written: %v", err)
	}
	var state persistedPollState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal seeded state: %v", err)
	}
	if state.LastPollAtFull != watermark {
		t.Errorf("LastPollAtFull = %q, want %q", state.LastPollAtFull, watermark)
	}
	if state.HMAC == "" {
		t.Error("expected seeded state to be signed when a dispatch secret is given")
	}
	want, err := computeStateHMAC("test-secret", state)
	if err != nil {
		t.Fatalf("computeStateHMAC: %v", err)
	}
	if state.HMAC != want {
		t.Error("seeded HMAC does not match the signature the runtime poller would verify")
	}
}

func TestSeedGitLabPollStateFromLegacyVars_NoLegacyVars(t *testing.T) {
	fc := forge.NewFakeClient()

	seeded, err := SeedGitLabPollStateFromLegacyVars(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seeded {
		t.Error("expected no-op when a genuinely new install has nothing to migrate")
	}
	if _, err := fc.DownloadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("expected no package file to be written, got err=%v", err)
	}
}

func TestSeedGitLabPollStateFromLegacyVars_SkipsWhenPackageAlreadyExists(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = "2025-03-01T09:00:00Z"
	if err := fc.UploadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName, []byte(`{"last_poll_at_full":"2025-06-01T00:00:00Z"}`)); err != nil {
		t.Fatalf("seed existing package: %v", err)
	}

	seeded, err := SeedGitLabPollStateFromLegacyVars(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seeded {
		t.Error("expected no-op when a poll state package already exists, to avoid clobbering live state")
	}
}

func TestSeedGitLabPollStateFromLegacyVars_NoSecretLeavesUnsigned(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = "2025-03-01T09:00:00Z"

	seeded, err := SeedGitLabPollStateFromLegacyVars(context.Background(), fc, "testgroup", "testrepo", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seeded {
		t.Fatal("expected state to be seeded")
	}
	data, err := fc.DownloadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName)
	if err != nil {
		t.Fatalf("expected package file to be written: %v", err)
	}
	var state persistedPollState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("unmarshal seeded state: %v", err)
	}
	if state.HMAC != "" {
		t.Error("expected unsigned state when no dispatch secret is configured")
	}
}

func TestSeedGitLabPollStateFromLegacyVars_DownloadErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["DownloadPackageFile"] = fmt.Errorf("boom")

	_, err := SeedGitLabPollStateFromLegacyVars(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected a non-NotFound download error to propagate")
	}
}

func TestSeedGitLabPollStateFromLegacyVars_ListVarsErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["ListRepoVariables"] = fmt.Errorf("forbidden")

	_, err := SeedGitLabPollStateFromLegacyVars(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected a variable-listing error to propagate")
	}
}

func TestSeedGitLabPollStateFromLegacyVars_UploadErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["testgroup/testrepo/"+forge.VarLastPollAtFull] = "2025-03-01T09:00:00Z"
	fc.Errors["UploadPackageFile"] = fmt.Errorf("upload denied")

	_, err := SeedGitLabPollStateFromLegacyVars(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected an upload error to propagate")
	}
}

// --- DiscardUnsignedGitLabPollState tests ---
//
// The upgrade bridge for repos whose state.json was written by a
// pre-#7317 (unsigned) poller: once repos install/converge provisions
// FULLSEND_DISPATCH_SECRET, DiscardUnsignedGitLabPollState *deletes* the
// untrusted unsigned document (any Developer-level token can write it)
// rather than laundering it into a signed one. The trustworthy migration
// source is the Maintainer-only legacy CI/CD variables handled by
// SeedGitLabPollStateFromLegacyVars, which the caller runs next.

func TestDiscardUnsignedGitLabPollState_DiscardsUnsigned(t *testing.T) {
	fc := forge.NewFakeClient()
	watermark := time.Date(2025, 3, 1, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	unsigned := persistedPollState{LastPollAtFull: watermark, LabelState: LabelState{42: {"ready-to-code"}}}
	raw, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatalf("marshal unsigned state: %v", err)
	}
	if err := fc.UploadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName, raw); err != nil {
		t.Fatalf("seed unsigned state: %v", err)
	}

	changed, err := DiscardUnsignedGitLabPollState(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatal("expected an unsigned document to be discarded")
	}

	// The untrusted document must be gone, not re-signed in place.
	if _, err := fc.DownloadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName); !errors.Is(err, forge.ErrNotFound) {
		t.Errorf("expected unsigned state to be deleted (ErrNotFound), got err=%v", err)
	}
}

func TestDiscardUnsignedGitLabPollState_NoopWhenAlreadySigned(t *testing.T) {
	fc := forge.NewFakeClient()
	state := persistedPollState{LastPollAtFull: "2025-03-01T09:00:00Z"}
	sig, err := computeStateHMAC("test-secret", state)
	if err != nil {
		t.Fatalf("computeStateHMAC: %v", err)
	}
	state.HMAC = sig
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal signed state: %v", err)
	}
	if err := fc.UploadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName, raw); err != nil {
		t.Fatalf("seed signed state: %v", err)
	}

	changed, err := DiscardUnsignedGitLabPollState(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no-op when the document already carries a signature")
	}
	// A signed (trusted) document must be preserved, not deleted.
	if _, err := fc.DownloadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName); err != nil {
		t.Errorf("signed document must be preserved, got err=%v", err)
	}
}

func TestDiscardUnsignedGitLabPollState_NoopWhenNoPackage(t *testing.T) {
	fc := forge.NewFakeClient()

	changed, err := DiscardUnsignedGitLabPollState(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no-op when no poll state package exists yet")
	}
}

func TestDiscardUnsignedGitLabPollState_NoopWhenSecretEmpty(t *testing.T) {
	fc := forge.NewFakeClient()
	if err := fc.UploadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName, []byte(`{"last_poll_at_full":"2025-03-01T09:00:00Z"}`)); err != nil {
		t.Fatalf("seed unsigned state: %v", err)
	}

	changed, err := DiscardUnsignedGitLabPollState(context.Background(), fc, "testgroup", "testrepo", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changed {
		t.Error("expected no-op when no dispatch secret is configured")
	}
	// With no secret there is nothing to enforce yet; the document must
	// be left untouched for the runtime fail-closed path.
	if _, err := fc.DownloadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName); err != nil {
		t.Errorf("expected the document to remain when no secret is configured, got err=%v", err)
	}
}

func TestDiscardUnsignedGitLabPollState_DownloadErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.Errors["DownloadPackageFile"] = fmt.Errorf("boom")

	_, err := DiscardUnsignedGitLabPollState(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected a non-NotFound download error to propagate")
	}
}

func TestDiscardUnsignedGitLabPollState_DeleteErrorPropagates(t *testing.T) {
	fc := forge.NewFakeClient()
	if err := fc.UploadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName, []byte(`{"last_poll_at_full":"2025-03-01T09:00:00Z"}`)); err != nil {
		t.Fatalf("seed unsigned state: %v", err)
	}
	fc.Errors["DeletePackage"] = fmt.Errorf("delete denied")

	_, err := DiscardUnsignedGitLabPollState(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err == nil {
		t.Fatal("expected a delete error to propagate")
	}
}

func TestDiscardUnsignedGitLabPollState_LeavesCorruptDocument(t *testing.T) {
	fc := forge.NewFakeClient()
	if err := fc.UploadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName, []byte("not-json{")); err != nil {
		t.Fatalf("seed corrupt state: %v", err)
	}

	changed, err := DiscardUnsignedGitLabPollState(context.Background(), fc, "testgroup", "testrepo", "test-secret")
	if err != nil {
		t.Fatalf("expected corrupt document to be left for the runtime fail-closed path, got err: %v", err)
	}
	if changed {
		t.Error("expected no-op for a corrupt (unparseable) document")
	}
	data, err := fc.DownloadPackageFile(context.Background(), "testgroup", "testrepo", pollStatePackageName, pollStatePackageVersion, pollStateFileName)
	if err != nil {
		t.Fatalf("download state: %v", err)
	}
	if string(data) != "not-json{" {
		t.Errorf("corrupt document was modified: %q", string(data))
	}
}
