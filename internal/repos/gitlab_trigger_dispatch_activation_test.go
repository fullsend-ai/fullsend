package repos

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func (e *trigEnv) activationRaw() (string, bool) {
	raw, ok := e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabDispatchActivation]
	return raw, ok
}

func (e *trigEnv) activation(t *testing.T) (gitLabDispatchActivation, bool) {
	t.Helper()
	raw, ok := e.activationRaw()
	if !ok {
		return gitLabDispatchActivation{}, false
	}
	var a gitLabDispatchActivation
	require.NoError(t, json.Unmarshal([]byte(raw), &a))
	return a, true
}

func (e *trigEnv) rawLedger() string {
	return e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabTriggerAccount]
}

// currentLedgerFields lists the non-secret ledger fields accepted by this
// version. The trigger digest and publication-pending marker are new and
// older strict decoders reject either one before they read the version;
// that compatibility break is called out in the GitLab upgrade guidance.
var currentLedgerFields = map[string]bool{
	"version": true, "phase": true, "branch": true, "nonce": true, "account_id": true,
	"elevated": true, "pat_id": true, "pat_pending": true, "deploy_key_fingerprint": true,
	"deploy_key_id": true, "grant_entry_id": true, "trigger_token_id": true,
	"trigger_published": true, "publish_started": true, "retired": true,
	"trigger_token_digest": true, "publication_pending": true,
}

// TestCurrentLedgerFields_MatchStateSchema keeps currentLedgerFields in sync
// with the ledger struct, so a new ledger key cannot ship without being
// listed (and named in the compatibility guidance).
func TestCurrentLedgerFields_MatchStateSchema(t *testing.T) {
	tags := map[string]bool{}
	for field := range reflect.TypeFor[gitLabTriggerAccountState]().Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		tags[name] = true
	}
	assert.Equal(t, currentLedgerFields, tags)

	// A ledger carrying both new keys serializes only listed keys and
	// passes a strict decode.
	raw, err := json.Marshal(gitLabTriggerAccountState{
		Version: gitLabTriggerAccountStateVersion, Phase: "ready", Branch: "main",
		Nonce: "abc", TriggerTokenID: 9, TriggerTokenDigest: "digest",
		TriggerPublished: true, PublicationPending: true,
	})
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"trigger_token_digest"`)
	assert.Contains(t, string(raw), `"publication_pending"`)
	assertCurrentLedgerSchema(t, string(raw))
}

func assertCurrentLedgerSchema(t *testing.T, raw string) {
	t.Helper()
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(raw), &doc))
	for key := range doc {
		assert.Truef(t, currentLedgerFields[key], "ledger key %q is unexpected", key)
	}
	// And a strict decode (the older decoder's behavior) accepts it.
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var st gitLabTriggerAccountState
	require.NoError(t, dec.Decode(&st))
}

// The dispatch activation is recorded only by SetGitLabTriggerDispatchActive
// (after the activation gate passes) and is withdrawn by every lifecycle run,
// so a ready ledger with a published trigger token never activates the
// child-pipeline route on its own (#8438). The activation is a separate
// variable bound to the ledger generation, never a ledger field.
func TestGitLabTriggerDispatchActive_RecordedOnlyExplicitlyAndWithdrawnByLifecycle(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.seedAdminResources()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	_, active := e.activation(t)
	assert.False(t, active, "a ready ledger is not an activation")
	assert.NotEmpty(t, e.f.published, "the trigger token is published while dispatch stays inactive")
	ledgerBefore := e.rawLedger()

	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))
	st, ok := e.ledger(t)
	require.True(t, ok)
	a, active := e.activation(t)
	require.True(t, active)
	assert.Equal(t, gitLabDispatchActivation{
		Version: 1, Nonce: st.Nonce, AccountID: st.AccountID, DeployKeyID: st.DeployKeyID,
		GrantEntryID: st.GrantEntryID, TriggerTokenID: st.TriggerTokenID,
	}, a, "the activation binds to the exact ledger generation")
	assert.Equal(t, gitLabTriggerPhaseReady, st.Phase)
	assert.Equal(t, ledgerBefore, e.rawLedger(), "activating must not rewrite the ledger an older CLI reads")
	assertCurrentLedgerSchema(t, e.rawLedger())
	protected := false
	for _, rec := range e.state.UpdatedVariables {
		if rec.Name == forge.VarGitLabDispatchActivation {
			protected = rec.Protected
		}
	}
	assert.True(t, protected, "the activation is a protected variable")

	// Recording it again, and withdrawing, are idempotent.
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))

	// Any converge run withdraws it before anything else runs.
	_, err = ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	_, active = e.activation(t)
	assert.False(t, active, "converge must withdraw the activation")

	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), false))
	_, active = e.activation(t)
	assert.False(t, active)

	// So does a converge that defers (weakened override role).
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))
	e.state.PipelineVarOverrideRoles[trigOwner+"/"+trigRepo] = forge.PipelineVarOverrideDeveloper
	_, err = ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	_, active = e.activation(t)
	assert.False(t, active, "a deferred run must not leave the activation recorded")
}

// Rotation replaces the generation, so it withdraws the activation first and
// a stale record (written for the old generation) can never match the new
// ledger, even if the withdrawal were missed: the record binds the nonce.
func TestGitLabTriggerDispatchActive_RotationWithdrawsAndNewGenerationDoesNotMatch(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.seedAdminResources()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))
	old, ok := e.activation(t)
	require.True(t, ok)

	res, err = RotateGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	_, active := e.activation(t)
	assert.False(t, active, "rotation must withdraw the activation")
	st, _ := e.ledger(t)
	assert.NotEqual(t, old.Nonce, st.Nonce, "the rotated generation has a new nonce, so an older record can never bind to it")
	assertCurrentLedgerSchema(t, e.rawLedger())
}

// A CLI that predates the activation does not know the variable: it
// rotates, repairs or removes the account and leaves the record behind. The
// next newer-CLI lifecycle run, and uninstall, delete it; until then it names
// a generation the ledger no longer has.
func TestGitLabTriggerDispatchActive_StaleRecordFromOlderCLIIsWithdrawn(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.seedAdminResources()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))

	// Uninstall removes the record along with the ledger.
	require.NoError(t, RemoveGitLabTriggerAccount(ctx, e.opts()))
	_, ok := e.ledger(t)
	assert.False(t, ok)
	_, active := e.activation(t)
	assert.False(t, active, "uninstall must remove the activation record")

	// An orphaned record (ledger removed by an older CLI) is withdrawn by the
	// next converge and by containment before any other stage.
	e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabDispatchActivation] = `{"version":1,"nonce":"0123456789abcdef","account_id":1,"deploy_key_id":2,"grant_entry_id":3,"trigger_token_id":4}`
	require.NoError(t, ContainGitLabTriggerAccount(ctx, e.containOpts()))
	_, active = e.activation(t)
	assert.False(t, active, "containment withdraws an orphaned record")
}

// Containment-only recovery runs before every install stage, so it withdraws
// the activation too: an install that fails in a stage before the lifecycle
// (preflight, scaffold, inventory, lease) must not leave the activation set.
func TestContainGitLabTriggerAccount_WithdrawsDispatchActivation(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.seedAdminResources()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))
	_, active := e.activation(t)
	require.True(t, active)

	require.NoError(t, ContainGitLabTriggerAccount(ctx, e.containOpts()))
	st, ok := e.ledger(t)
	require.True(t, ok)
	_, active = e.activation(t)
	assert.False(t, active, "containment must withdraw the activation")
	assert.Equal(t, gitLabTriggerPhaseReady, st.Phase)
}

// A failed withdrawal is an error, never a silent leftover activation.
func TestGitLabTriggerDispatchActive_WithdrawalFailureIsReported(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	e.seedAdminResources()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))

	e.state.Errors["DeleteRepoVariable"] = errTrigInjected
	_, err = ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.ErrorIs(t, err, errTrigInjected, "converge must stop when the activation cannot be withdrawn")
	require.ErrorIs(t, RotateErrOnly(ctx, e), errTrigInjected)
	require.ErrorIs(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), false), errTrigInjected)
	require.ErrorIs(t, ContainGitLabTriggerAccount(ctx, e.containOpts()), errTrigInjected)
}

// RotateErrOnly runs rotation and returns only its error.
func RotateErrOnly(ctx context.Context, e *trigEnv) error {
	_, err := RotateGitLabTriggerAccount(ctx, e.opts())
	return err
}

func TestSetGitLabTriggerDispatchActive_Refusals(t *testing.T) {
	ctx := context.Background()

	t.Run("no ledger", func(t *testing.T) {
		e := newTrigEnv()
		require.Error(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true))
		require.NoError(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), false), "withdrawing nothing is a no-op")
		_, ok := e.ledger(t)
		assert.False(t, ok)
		_, active := e.activation(t)
		assert.False(t, active)
	})

	t.Run("ledger not ready", func(t *testing.T) {
		e := newTrigEnv()
		e.seedAdminResources()
		_, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
		require.NoError(t, err)
		st, _ := e.ledger(t)
		st.Phase = gitLabTriggerPhaseRotating
		raw, err := json.Marshal(st)
		require.NoError(t, err)
		require.NoError(t, e.state.UpdateCIVariable(ctx, trigOwner, trigRepo, forge.VarGitLabTriggerAccount, string(raw), true))
		err = SetGitLabTriggerDispatchActive(ctx, e.opts(), true)
		require.ErrorContains(t, err, "cannot be activated")
		_, active := e.activation(t)
		assert.False(t, active)
	})

	t.Run("trigger token not published", func(t *testing.T) {
		e := newTrigEnv()
		e.seedAdminResources()
		_, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
		require.NoError(t, err)
		st, _ := e.ledger(t)
		st.TriggerPublished = false
		st.PublishStarted = false
		e.setLedger(t, st)
		require.ErrorContains(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true), "published trigger token")
		_, active := e.activation(t)
		assert.False(t, active)
	})

	t.Run("recording failure is an error and leaves no activation", func(t *testing.T) {
		e := newTrigEnv()
		e.seedAdminResources()
		_, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
		require.NoError(t, err)
		e.state.Errors["UpdateCIVariable"] = errTrigInjected
		require.ErrorIs(t, SetGitLabTriggerDispatchActive(ctx, e.opts(), true), errTrigInjected)
		_, active := e.activation(t)
		assert.False(t, active)
	})

	t.Run("requires the project lease", func(t *testing.T) {
		e := newTrigEnv()
		opts := e.opts()
		opts.Lease = nil
		require.ErrorIs(t, SetGitLabTriggerDispatchActive(ctx, opts, true), errTriggerAccountUnleased)
	})
}
