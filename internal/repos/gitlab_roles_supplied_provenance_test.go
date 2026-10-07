package repos

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// A supplied credential is published only after its provenance is durable. When
// the provenance write fails the credential is not stored, so an existing
// managed entry can never classify the new secret.
func TestProvisionOwnRoles_SuppliedProvenanceWriteFailureDoesNotPublishCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rotationKey := "group/project/" + forge.VarGitLabRoleRotation
	managed := `{"roles":{"poller":{"phase":"idle","incoming_id":7,"distributed_at":"2026-01-01T00:00:00Z"}}}`
	fc.VariableValues[rotationKey] = managed
	fc.VariablesExist[rotationKey] = true
	fc.Errors = map[string]error{"UpdateCIVariable": errors.New("write refused")}

	cfg := RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc,
		ProvidedCredentials: map[gitlabroles.Role]ProvidedRoleCredential{gitlabroles.RolePoller: {Token: "suppliedXXXX", OwnerID: 90}},
		Now:                 time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	}
	var result RoleProvisionResult
	provisionOwnRoles(ctx, cfg, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)

	for _, rec := range fc.CreatedSecrets {
		assert.NotEqual(t, forge.SecretGitLabPollerToken, rec.Name, "the supplied credential must not be stored")
	}
	assert.Empty(t, result.Enrolled)
	var failed bool
	for _, f := range result.Failed {
		if f.Role == gitlabroles.RolePoller {
			failed = true
			assert.Contains(t, f.Reason, "provenance")
		}
	}
	assert.True(t, failed, "the Poller enrollment is reported as failed")
	assert.Equal(t, managed, fc.VariableValues[rotationKey], "existing provenance is unchanged")
}

func TestProvisionOwnRoles_SuppliedProvenanceIsRecordedOverManagedEntry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rotationKey := "group/project/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"idle","incoming_id":7,"distributed_at":"2026-01-01T00:00:00Z"}}}`
	fc.VariablesExist[rotationKey] = true

	cfg := RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc,
		ProvidedCredentials: map[gitlabroles.Role]ProvidedRoleCredential{gitlabroles.RolePoller: {Token: "suppliedXXXX", OwnerID: 90}},
		Now:                 time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	}
	var result RoleProvisionResult
	provisionOwnRoles(ctx, cfg, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)

	prov, err := PollerCredentialProvenance(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.True(t, prov.Supplied)
	assert.Equal(t, 90, prov.UserID)
	assert.Equal(t, []int{90}, prov.ExcludedUserIDs)
}

// Exclusions persist while the supplied accounts survive: a successful cleanup
// keeps an exclusions-only document instead of deleting it, and a document with
// no exclusions is still deleted.
func TestCleanupGitLabRoleIdentity_SuccessKeepsSuppliedExclusionsOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	rotationKey := "group/project/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true,"supplied_user_id":90,"excluded_user_ids":[80]},"coder":{"phase":"idle","incoming_id":4}}}`

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{}})
	require.NoError(t, err)
	require.True(t, fc.VariablesExist[rotationKey], "exclusions survive a successful uninstall")

	provs, err := RoleCredentialProvenances(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int{80, 90}, provs[gitlabroles.RolePoller].ExcludedUserIDs)
	assert.False(t, provs[gitlabroles.RolePoller].Known, "installed-credential provenance is not retained")
	assert.NotContains(t, provs, gitlabroles.RoleCoder, "roles without exclusions are dropped")

	// A repeated uninstall keeps the same exclusions.
	_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{}})
	require.NoError(t, err)
	provs, err = RoleCredentialProvenances(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int{80, 90}, provs[gitlabroles.RolePoller].ExcludedUserIDs)

	// Reinstalling provisions a managed credential over the exclusions-only
	// entry without losing them.
	require.NoError(t, recordInitialDistribution(ctx, fc, "group", "project", gitlabroles.RolePoller, 12, "2027-09-21", time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), false))
	prov, err := PollerCredentialProvenance(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, ExcludedUserIDs: []int{80, 90}}, prov)
}

// The rotation document is retained whenever any cleanup step fails, not only
// token revocation.
func TestCleanupGitLabRoleIdentity_RetainsRotationStateOnNonRevocationFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	rotationKey := "group/project/" + forge.VarGitLabRoleRotation
	state := `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true,"supplied_user_id":90}}}`
	fc.VariableValues[rotationKey] = state
	// Deleting a role secret fails while revocation itself succeeds.
	fc.Errors = map[string]error{"DeleteRepoSecret": errors.New("secret delete refused")}

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{}})
	require.Error(t, err)
	assert.True(t, fc.VariablesExist[rotationKey])
	assert.Equal(t, state, fc.VariableValues[rotationKey], "the full document survives for the retry")
}

func TestRecoverSuppliedRoleProvenance_NonPollerRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()

	ok, err := RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RoleCoder, "recovered-credential-value", 77, false, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	provs, err := RoleCredentialProvenances(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, Supplied: true, UserID: 77, ExcludedUserIDs: []int{77}}, provs[gitlabroles.RoleCoder])
	assert.True(t, fc.Secrets["g/p/"+forge.SecretGitLabCoderToken])

	// Known provenance is never overwritten, and an unregistered role recovers nothing.
	ok, err = RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RoleCoder, "recovered-credential-value", 78, false, nil)
	require.NoError(t, err)
	assert.False(t, ok)
	ok, err = RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.Role("unregistered"), "recovered-credential-value", 78, false, nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

// An uninstall that resolved an exclusion for a role with no rotation-state
// entry still persists it, so a repeated uninstall or reinstall keeps the
// account out of fullsend's management.
func TestRecordSuppliedExclusions_CreatesExclusionsOnlyEntryForAbsentRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rotationKey := "g/p/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"coder":{"phase":"idle","incoming_id":4}}}`
	fc.VariablesExist[rotationKey] = true

	require.NoError(t, RecordSuppliedExclusions(ctx, fc, "g", "p", gitlabroles.RolePoller, []int{91}))
	provs, err := RoleCredentialProvenances(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, []int{91}, provs[gitlabroles.RolePoller].ExcludedUserIDs)
	assert.False(t, provs[gitlabroles.RolePoller].Known)
	assert.True(t, provs[gitlabroles.RoleCoder].Known, "other roles are untouched")

	// The exclusions-only entry survives a successful cleanup.
	seedEnforcedIdentity(t, fc)
	fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation] = fc.VariableValues[rotationKey]
	fc.VariablesExist["group/project/"+forge.VarGitLabRoleRotation] = true
	_, err = CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{}})
	require.NoError(t, err)
	provs, err = RoleCredentialProvenances(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.Equal(t, []int{91}, provs[gitlabroles.RolePoller].ExcludedUserIDs)
}

// A rotation document that cannot be decoded may hold exclusions, so cleanup
// keeps it and reports an error instead of deleting it.
func TestCleanupGitLabRoleIdentity_PreservesUndecodableRotationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	seedEnforcedIdentity(t, fc)
	rotationKey := "group/project/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"excluded_user_ids":[90]}`
	fc.VariablesExist[rotationKey] = true

	_, err := CleanupGitLabRoleIdentity(ctx, GitLabRoleCleanupConfig{Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{}})
	require.Error(t, err)
	assert.True(t, fc.VariablesExist[rotationKey])
	assert.Equal(t, `{"roles":{"poller":{"excluded_user_ids":[90]}`, fc.VariableValues[rotationKey])
}

// A known supplied credential whose owner was never recorded can be repaired by
// re-enrolling a working credential, keeping earlier exclusions.
func TestRecoverSuppliedRoleProvenance_RepairsKnownSuppliedWithoutOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rotationKey := "g/p/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true,"excluded_user_ids":[80]}}}`
	fc.VariablesExist[rotationKey] = true
	fc.Secrets["g/p/"+forge.SecretGitLabPollerToken] = true

	// The outgoing credential's owner (85) is attributed and excluded as well.
	outgoing := func(context.Context, gitlabroles.Role) (int, error) { return 85, nil }
	ok, err := RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 90, false, outgoing)
	require.NoError(t, err)
	assert.True(t, ok)
	prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, PollerProvenance{Known: true, Supplied: true, UserID: 90, ExcludedUserIDs: []int{80, 85, 90}}, prov)
	assert.True(t, fc.Secrets["g/p/"+forge.SecretGitLabPollerToken])

	// With the owner recorded, nothing is overwritten.
	ok, err = RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 91, false, nil)
	require.NoError(t, err)
	assert.False(t, ok)

	// A managed credential is never recovered over.
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"phase":"idle","incoming_id":5}}}`
	ok, err = RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 92, false, nil)
	require.NoError(t, err)
	assert.False(t, ok)
}

// An installed supplied credential whose owner is unrecorded and cannot be
// attributed is never overwritten: its provenance stays unresolved and the
// replacement is not published.
func TestRecoverSuppliedRoleProvenance_UnattributableOutgoingOwnerIsNotReplaced(t *testing.T) {
	t.Parallel()
	for name, resolver := range map[string]func(context.Context, gitlabroles.Role) (int, error){
		"no resolver":        nil,
		"attribution failed": func(context.Context, gitlabroles.Role) (int, error) { return 0, fmt.Errorf("rejected") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := forge.NewFakeClient()
			rotationKey := "g/p/" + forge.VarGitLabRoleRotation
			const state = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true}}}`
			fc.VariableValues[rotationKey] = state
			fc.VariablesExist[rotationKey] = true
			fc.Secrets["g/p/"+forge.SecretGitLabPollerToken] = true

			ok, err := RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 90, false, resolver)
			require.Error(t, err)
			assert.False(t, ok)
			assert.Empty(t, fc.CreatedSecrets, "the installed credential is not overwritten")
			assert.Equal(t, state, fc.VariableValues[rotationKey], "provenance stays unresolved")
		})
	}
}

// An installed credential of unknown provenance (no rotation entry, as after an
// interrupted enrollment) has its outgoing owner attributed and excluded before
// a provided credential overwrites it; replacement is refused when attribution
// fails. With nothing installed there is nothing to attribute.
func TestRecoverSuppliedRoleProvenance_UnknownProvenanceExcludesOutgoingOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	secretKey := "g/p/" + forge.SecretGitLabPollerToken

	t.Run("outgoing owner attributed", func(t *testing.T) {
		t.Parallel()
		fc := forge.NewFakeClient()
		fc.Secrets[secretKey] = true
		outgoing := func(context.Context, gitlabroles.Role) (int, error) { return 85, nil }
		ok, err := RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 90, false, outgoing)
		require.NoError(t, err)
		assert.True(t, ok)
		prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
		require.NoError(t, err)
		assert.Equal(t, PollerProvenance{Known: true, Supplied: true, UserID: 90, ExcludedUserIDs: []int{85, 90}}, prov)
	})

	for name, resolver := range map[string]func(context.Context, gitlabroles.Role) (int, error){
		"no resolver":        nil,
		"attribution failed": func(context.Context, gitlabroles.Role) (int, error) { return 0, fmt.Errorf("rejected") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fc := forge.NewFakeClient()
			fc.Secrets[secretKey] = true
			ok, err := RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 90, false, resolver)
			require.Error(t, err)
			assert.False(t, ok)
			assert.Empty(t, fc.CreatedSecrets, "the installed credential is not overwritten")
			prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
			require.NoError(t, err)
			assert.False(t, prov.Known, "no provenance is recorded for the replacement")
		})
	}
}

// An unforced install keeps a healthy enrolled supplied legacy credential: the
// shared inventory still reports it for lifecycle, so rotation does not treat it
// as unverified and replace it, and nothing is revoked.
func TestRotateGitLabRoleCredentials_UnforcedKeepsHealthySuppliedLegacyCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":70,"excluded_user_ids":[70]}}}`, true))
	legacy := &fakeTokens{}
	legacy.seed(ProjectAccessToken{ID: 50, Name: gitlabroles.PollerTokenName, Active: true, UserID: 70, ExpiresAt: "2027-09-21"})
	tokens := ServiceAccountTokenClient{
		SA: newFakeSAAPI(), Legacy: legacy,
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{70}, nil },
	}

	result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
	})
	require.NoError(t, err)
	assert.Empty(t, result.Rotated, "a healthy supplied credential is not replaced")
	assert.Empty(t, result.Failed)
	assert.Empty(t, legacy.created, "no managed credential is minted")
	assert.Empty(t, legacy.revoked, "the supplied credential is never revoked")
	prov, err := PollerCredentialProvenance(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.True(t, prov.Supplied)
}

// Replacing an unresolved supplied credential from account A with one from
// account B through an administrator-provided rotation keeps A excluded.
func TestRotateGitLabRoleCredentials_ProvidedReplacementExcludesUnresolvedOutgoingOwner(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	const suppliedState = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true}}}`
	run := func(t *testing.T, resolver func(context.Context, gitlabroles.Role) (int, error)) (*forge.FakeClient, RoleRotateResult) {
		fc := seededRoleClient(t, gitlabroles.RolePoller)
		require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation, suppliedState, true))
		result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: fc, Tokens: &fakeTokens{},
			Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
			Force: true, Now: now,
			ProvidedCredentials:  map[gitlabroles.Role]ProvidedRoleCredential{gitlabroles.RolePoller: {Token: "replacement-credential-value", OwnerID: 91}},
			ResolveSuppliedOwner: resolver,
		})
		require.NoError(t, err)
		return fc, result
	}

	t.Run("outgoing owner attributed", func(t *testing.T) {
		t.Parallel()
		fc, result := run(t, func(context.Context, gitlabroles.Role) (int, error) { return 90, nil })
		assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
		prov, err := PollerCredentialProvenance(context.Background(), fc, "group", "project")
		require.NoError(t, err)
		assert.True(t, prov.Supplied)
		assert.Equal(t, 91, prov.UserID)
		assert.ElementsMatch(t, []int{90, 91}, prov.ExcludedUserIDs, "the outgoing owner stays excluded")
	})

	t.Run("outgoing owner unattributable", func(t *testing.T) {
		t.Parallel()
		fc, result := run(t, nil)
		assert.Empty(t, result.Rotated)
		assert.Len(t, result.Failed, 1)
		for _, s := range fc.CreatedSecrets {
			assert.NotContains(t, s.Value, "replacement-credential-value", "the installed credential is not overwritten")
		}
		prov, err := PollerCredentialProvenance(context.Background(), fc, "group", "project")
		require.NoError(t, err)
		assert.True(t, prov.Supplied)
		assert.Zero(t, prov.UserID, "provenance stays unresolved")
		assert.Empty(t, prov.ExcludedUserIDs)
	})
}

// Managed rotation of a supplied credential with no recorded owner first
// records the resolved owner; without one it refuses to replace the credential.
func TestRotateGitLabRoleCredentials_SuppliedWithoutOwnerRecordsOwnerBeforeReplacing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	const suppliedState = `{"roles":{"poller":{"phase":"idle","distributed_at":"2026-01-01T00:00:00Z","supplied":true}}}`
	for _, tc := range []struct {
		name      string
		resolver  func(context.Context, gitlabroles.Role) (int, error)
		wantMint  bool
		wantOwner []int
	}{
		{name: "resolved owner is recorded", resolver: func(context.Context, gitlabroles.Role) (int, error) { return 90, nil }, wantMint: true, wantOwner: []int{90}},
		{name: "no resolver refuses", wantMint: false},
		{name: "failed attribution refuses", resolver: func(context.Context, gitlabroles.Role) (int, error) { return 0, fmt.Errorf("rejected") }, wantMint: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc := seededRoleClient(t, gitlabroles.RolePoller)
			require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation, suppliedState, true))
			tokens := &fakeTokens{}
			tokens.seed(ProjectAccessToken{Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"})

			result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
				Force: true, Now: now, ResolveSuppliedOwner: tc.resolver,
			})
			require.NoError(t, err)
			prov, perr := PollerCredentialProvenance(context.Background(), fc, "group", "project")
			require.NoError(t, perr)
			if tc.wantMint {
				assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
				assert.Len(t, tokens.created, 1)
				assert.False(t, prov.Supplied, "the replacement is managed")
				assert.Equal(t, tc.wantOwner, prov.ExcludedUserIDs, "the supplied owner stays excluded after replacement")
				return
			}
			assert.Empty(t, result.Rotated)
			assert.Len(t, result.Failed, 1)
			assert.Empty(t, tokens.created, "nothing is minted when the owner cannot be recorded")
			assert.True(t, prov.Supplied)
		})
	}
}

// Managed provenance is durable before the credential is published: when it
// cannot be written, nothing is stored and the unused token is revoked.
func TestProvisionOwnRoles_ManagedProvenanceWriteFailureDoesNotPublishCredential(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	fc.Errors = map[string]error{"UpdateCIVariable": errors.New("write refused")}
	tokens := &fakeTokens{}

	cfg := RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	}
	var result RoleProvisionResult
	provisionOwnRoles(ctx, cfg, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)

	assert.Empty(t, fc.CreatedSecrets, "no credential is published without provenance")
	assert.Empty(t, result.Created)
	assert.NotEmpty(t, result.Failed)
	assert.Len(t, tokens.revoked, len(tokens.created), "every unused token is revoked")
	for _, f := range result.Failed {
		assert.Contains(t, f.Reason, "provenance")
	}
}

// A failed publish removes the provenance recorded for the revoked token.
func TestProvisionOwnRoles_FailedPublishDiscardsManagedProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	tokens := &fakeTokens{}
	client := &selectiveSecretClient{Client: fc, fail: map[string]error{forge.SecretGitLabPollerToken: errors.New("store refused")}}

	cfg := RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: client, Tokens: tokens,
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	}
	var result RoleProvisionResult
	provisionOwnRoles(ctx, cfg, gitlabroles.BuiltinRegistry(), map[string]bool{}, &result)

	provs, err := RoleCredentialProvenances(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.NotContains(t, provs, gitlabroles.RolePoller, "the revoked token's record is discarded")
	assert.Contains(t, provs, gitlabroles.RoleCoder, "published credentials keep their record")
}

// A failed publish restores the entry that existed before the new token was
// recorded, so earlier incoming and outgoing token IDs stay tracked for grace
// cleanup, and a later successful retry queues all of them as outgoing.
func TestProvisionOwnRoles_FailedPublishRestoresPriorTokenTracking(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"overlapping","incoming_id":101,"outgoing_ids":[100],"distributed_at":"2026-09-01T00:00:00Z","expires_at":"2027-09-01"}}}`, true))
	cfg := RoleProvisionConfig{
		Owner: "group", Repo: "project", Tokens: &fakeTokens{},
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
	}

	cfg.Client = &selectiveSecretClient{Client: fc, fail: map[string]error{forge.SecretGitLabPollerToken: errors.New("store refused")}}
	var failed RoleProvisionResult
	provisionOwnRoles(ctx, cfg, gitlabroles.BuiltinRegistry(), map[string]bool{}, &failed)

	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	rs := state.Roles["poller"]
	assert.Equal(t, 101, rs.IncomingID, "the earlier incoming token is tracked again")
	assert.Equal(t, []int{100}, rs.OutgoingIDs)
	assert.Equal(t, rotationPhaseOverlapping, rs.Phase)

	cfg.Client = fc
	cfg.Tokens = &fakeTokens{nextID: 200}
	var retried RoleProvisionResult
	provisionOwnRoles(ctx, cfg, gitlabroles.BuiltinRegistry(), map[string]bool{}, &retried)

	state, _, err = loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	rs = state.Roles["poller"]
	assert.Greater(t, rs.IncomingID, 200)
	assert.ElementsMatch(t, []int{100, 101}, rs.OutgoingIDs, "older tokens are scheduled for grace revocation")
	assert.Equal(t, rotationPhaseOverlapping, rs.Phase)
}

// A supplied role whose rotation entry has no recorded owner gets the resolved
// owner recorded; roles that are managed, absent, or already have an owner are
// left alone, and a repeat write is a no-op.
func TestRecordSuppliedOwners_RecordsOnlyUnresolvedSuppliedRoles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rotationKey := "g/p/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{` +
		`"poller":{"supplied":true},` +
		`"coder":{"phase":"idle","incoming_id":4},` +
		`"analyst":{"supplied":true,"supplied_user_id":12}}}`
	fc.VariablesExist[rotationKey] = true

	owners := map[gitlabroles.Role]int{
		gitlabroles.RolePoller: 90, gitlabroles.RoleCoder: 91, gitlabroles.RoleAnalyst: 92, gitlabroles.Role("triage"): 93,
	}
	require.NoError(t, RecordSuppliedOwners(ctx, fc, "g", "p", owners))
	provs, err := RoleCredentialProvenances(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 90, provs[gitlabroles.RolePoller].UserID)
	assert.Equal(t, []int{90}, provs[gitlabroles.RolePoller].ExcludedUserIDs)
	assert.Zero(t, provs[gitlabroles.RoleCoder].UserID, "a managed role is not marked supplied")
	assert.Equal(t, 12, provs[gitlabroles.RoleAnalyst].UserID, "a recorded owner is not replaced")
	_, hasTriage := provs[gitlabroles.Role("triage")]
	assert.False(t, hasTriage, "an absent role is not created")

	before := len(fc.UpdatedVariables)
	require.NoError(t, RecordSuppliedOwners(ctx, fc, "g", "p", owners))
	assert.Len(t, fc.UpdatedVariables, before, "a repeat records nothing")
}

// Replacing a supplied service-account credential with a managed one never
// schedules the supplied token for revocation, so rotation state settles to idle
// once the grace period has passed.
func TestRotateGitLabRoleCredentials_SuppliedServiceAccountTokenIsNotScheduledForRevocation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":70,"excluded_user_ids":[70]}}}`, true))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 70, Name: gitlabroles.PollerTokenName}}
	sa.tokens[70] = []ProjectAccessToken{{ID: 50, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"}}
	tokens := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{70}, nil },
	}
	cfg := RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Force: true, Now: now,
	}

	result, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.NotContains(t, state.Roles[string(gitlabroles.RolePoller)].OutgoingIDs, 50, "the supplied token is not an outgoing candidate")

	cfg.Force = false
	cfg.Now = now.Add(25 * time.Hour)
	_, err = RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.NotContains(t, sa.revoked, 50, "the supplied token is never revoked")
	state, _, err = loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	rs := state.Roles[string(gitlabroles.RolePoller)]
	assert.Empty(t, rs.OutgoingIDs)
	assert.Equal(t, rotationPhaseIdle, rs.Phase, "rotation state settles to idle")
}

// A healthy supplied credential whose token name differs from its account name
// is retained by an unforced rotation rather than treated as missing.
func TestRotateGitLabRoleCredentials_UnforcedKeepsHealthySuppliedServiceAccountTokenWithOtherName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":70,"excluded_user_ids":[70]}}}`, true))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 70, Name: gitlabroles.PollerTokenName}}
	sa.tokens[70] = []ProjectAccessToken{{ID: 50, Name: "automation", Active: true, ExpiresAt: "2027-09-21"}}
	tokens := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{70}, nil },
	}
	cfg := RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
	}

	result, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Empty(t, result.Rotated, "the healthy supplied credential is not replaced")
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Skipped)
}

// Rotation never schedules a token for revocation that the token client's
// project-wide supplied-owner boundary would refuse, even when the exclusion
// is recorded on another role.
func TestRotateGitLabRoleCredentials_OutgoingExcludesOwnersRecordedOnOtherRoles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	fc := seededRoleClient(t, gitlabroles.RolePoller)
	require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
		`{"roles":{"poller":{"phase":"idle","incoming_id":50,"distributed_at":"2026-09-01T00:00:00Z"},"analyst":{"supplied":true,"supplied_user_id":70,"excluded_user_ids":[70]}}}`, true))
	sa := newFakeSAAPI()
	sa.accounts = []GitLabServiceAccount{{ID: 70, Name: gitlabroles.PollerTokenName}}
	sa.tokens[70] = []ProjectAccessToken{{ID: 50, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"}}
	tokens := ServiceAccountTokenClient{
		SA: sa, Legacy: &fakeTokens{},
		SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{70}, nil },
	}
	cfg := RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Force: true, Now: now,
	}

	result, err := RotateGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated)
	assert.Empty(t, result.Overlapping, "nothing is left overlapping")
	state, _, err := loadRotationState(ctx, fc, "group", "project")
	require.NoError(t, err)
	rs := state.Roles[string(gitlabroles.RolePoller)]
	assert.NotContains(t, rs.OutgoingIDs, 50)
	assert.Equal(t, rotationPhaseIdle, rs.Phase)
}

func TestRotationRoleState_SuppliedTokenIDFollowsEnrollment(t *testing.T) {
	var rs rotationRoleState
	rs.markSupplied(10)
	rs.setSuppliedToken(10, 5)
	assert.Equal(t, 5, rs.SuppliedTokenID)
	assert.Equal(t, 5, provenanceOf(rs).TokenID)

	// A token ID applies only to the owner just recorded.
	rs.setSuppliedToken(11, 6)
	assert.Equal(t, 5, rs.SuppliedTokenID)

	// Re-enrolling resets the ID until the new one is recorded; a managed
	// replacement clears it.
	rs.markSupplied(10)
	assert.Zero(t, rs.SuppliedTokenID)
	rs.setSuppliedToken(10, 7)
	rs.markManaged()
	assert.Zero(t, rs.SuppliedTokenID)
}

// An unforced rotation judges an enrolled supplied credential by its recorded
// token ID: a revoked, expired, or missing enrolled token is replaced even when
// another managed role-named token is healthy, and a healthy one is retained.
func TestRotateGitLabRoleCredentials_UnforcedUsesRecordedSuppliedTokenAlongsideHealthyManagedToken(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		enrolled   []ProjectAccessToken
		wantRotate bool
	}{
		{"revoked", []ProjectAccessToken{{ID: 50, Name: "automation", Revoked: true, ExpiresAt: "2027-09-21"}}, true},
		{"expired", []ProjectAccessToken{{ID: 50, Name: "automation", Active: true, ExpiresAt: "2026-01-01"}}, true},
		{"missing", nil, true},
		{"healthy", []ProjectAccessToken{{ID: 50, Name: "automation", Active: true, ExpiresAt: "2027-09-21"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RolePoller)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation,
				`{"roles":{"poller":{"phase":"idle","distributed_at":"2026-09-01T00:00:00Z","supplied":true,"supplied_user_id":70,"supplied_token_id":50,"excluded_user_ids":[70]}}}`, true))
			sa := newFakeSAAPI()
			// Account 70 is administrator-supplied; account 71 is a managed
			// Poller account whose healthy token must not decide the outcome.
			sa.accounts = []GitLabServiceAccount{
				{ID: 70, Name: gitlabroles.PollerTokenName},
				{ID: 71, Name: gitlabroles.PollerTokenName},
			}
			sa.tokens[70] = append(tc.enrolled, ProjectAccessToken{ID: 51, Name: "unrelated", Active: true, ExpiresAt: "2030-01-01"})
			sa.tokens[71] = []ProjectAccessToken{{ID: 60, Name: gitlabroles.PollerTokenName, Active: true, ExpiresAt: "2027-09-21"}}
			tokens := ServiceAccountTokenClient{
				SA: sa, Legacy: &fakeTokens{},
				SuppliedAccountIDs: func(context.Context, string, string) ([]int, error) { return []int{70}, nil },
				SuppliedTokenIDs: func(context.Context, string, string) (map[int][]SuppliedTokenRef, error) {
					return map[int][]SuppliedTokenRef{70: {{ID: 50, Name: gitlabroles.PollerTokenName}}}, nil
				},
			}
			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller}, Now: now,
			})
			require.NoError(t, err)
			assert.Empty(t, result.Failed)
			if tc.wantRotate {
				assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Rotated, "the broken installed credential is replaced")
			} else {
				assert.Empty(t, result.Rotated, "the healthy installed credential is retained")
				assert.Equal(t, []gitlabroles.Role{gitlabroles.RolePoller}, result.Skipped)
			}
		})
	}
}

// Known supplied provenance is recorded before the secret is published, so it
// does not prove an installed credential: re-enrolling after a publication that
// never happened must not demand attribution of a nonexistent outgoing owner.
func TestRecoverSuppliedRoleProvenance_KnownSuppliedWithoutInstalledSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rotationKey := "g/p/" + forge.VarGitLabRoleRotation
	fc.VariableValues[rotationKey] = `{"roles":{"poller":{"supplied":true}}}`
	fc.VariablesExist[rotationKey] = true

	ok, err := RecoverSuppliedRoleProvenance(ctx, fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 90, false, nil)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.True(t, fc.Secrets["g/p/"+forge.SecretGitLabPollerToken])
	prov, err := PollerCredentialProvenance(ctx, fc, "g", "p")
	require.NoError(t, err)
	assert.Equal(t, 90, prov.UserID)
}

// Recovery errors reach installer output, so a server echoing an administrative
// credential must not leak through them.
func TestRecoverSuppliedRoleProvenance_APIErrorsAreRedacted(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"GetRepoVariable", "RepoSecretExists"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			fc := forge.NewFakeClient()
			fc.Secrets["g/p/"+forge.SecretGitLabPollerToken] = true
			fc.Errors[method] = fmt.Errorf("server echoed %s", leakToken)
			_, err := RecoverSuppliedRoleProvenance(context.Background(), fc, "g", "p", gitlabroles.RolePoller, "replacement-credential-value", 90, false, nil)
			require.Error(t, err)
			assertNoLeak(t, err.Error())
		})
	}
}

// GitLab may commit a secret and still fail the request. The token and its
// provenance are then kept, and a later install recovers by treating the secret
// as installed.
func TestProvisionGitLabRoleCredentials_CommittedStoreFailureKeepsProvenance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := provisionClient(t)
	client := &commitThenFailClient{FakeClient: fc}
	tokens := &fakeTokens{}
	cfg := RoleProvisionConfig{
		Owner: "group", Repo: "project", Client: client, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(),
	}

	result, err := ProvisionGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Len(t, result.Failed, 3)
	assert.Empty(t, tokens.revoked, "tokens of possibly published secrets are not revoked")
	for _, f := range result.Failed {
		assert.Contains(t, f.Reason, "could not be ruled out")
	}

	// The next install sees the committed secrets and reconciles cleanly.
	client.failStore = false
	again, err := ProvisionGitLabRoleCredentials(ctx, cfg)
	require.NoError(t, err)
	assert.Empty(t, again.Failed)
	assert.Len(t, again.Skipped, 3)
	prov, err := RoleCredentialProvenances(ctx, fc, "group", "project")
	require.NoError(t, err)
	assert.True(t, prov[gitlabroles.RolePoller].Known)
}

// commitThenFailClient stores a secret and then reports the request as failed,
// as when a response is lost after GitLab committed the variable.
type commitThenFailClient struct {
	*forge.FakeClient
	failStore bool
	started   bool
}

func (c *commitThenFailClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if !c.started {
		c.started, c.failStore = true, true
	}
	if err := c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
		return err
	}
	if c.failStore {
		return errors.New("response lost")
	}
	return nil
}

// A supplied replacement whose owner could not be attributed must leave the
// entry unresolved (never the previous managed provenance) even when the secret
// store commits but reports an error, or the completed-state write fails.
func TestRotateGitLabRoleCredentials_ProvidedReplacementWithoutOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	const managedState = `{"roles":{"poller":{"phase":"idle","incoming_id":5,"distributed_at":"2026-01-01T00:00:00Z"}}}`
	run := func(t *testing.T, client func(*forge.FakeClient) forge.Client) (*forge.FakeClient, RoleRotateResult) {
		fc := seededRoleClient(t, gitlabroles.RolePoller)
		require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation, managedState, true))
		result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
			Owner: "group", Repo: "project", Client: client(fc), Tokens: &fakeTokens{},
			Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
			Force: true, Now: now,
			ProvidedCredentials: map[gitlabroles.Role]ProvidedRoleCredential{gitlabroles.RolePoller: {Token: "replacement-credential-value"}},
		})
		require.NoError(t, err)
		return fc, result
	}
	assertUnresolved := func(t *testing.T, fc *forge.FakeClient) {
		prov, err := PollerCredentialProvenance(context.Background(), fc, "group", "project")
		require.NoError(t, err)
		assert.True(t, prov.Known)
		assert.True(t, prov.Supplied, "the replacement is never judged by the previous managed provenance")
		assert.Zero(t, prov.UserID, "its owner is explicitly unresolved")
		state, _, err := loadRotationState(context.Background(), fc, "group", "project")
		require.NoError(t, err)
		assert.False(t, state.Roles["poller"].SuppliedDistributed, "no distribution proof is vouched for")
		assert.NotEqual(t, rotationPhaseIdle, state.Roles["poller"].Phase)
	}

	t.Run("store commits then errors", func(t *testing.T) {
		t.Parallel()
		fc, result := run(t, func(fc *forge.FakeClient) forge.Client {
			return &commitThenFailClient{FakeClient: fc}
		})
		assert.Empty(t, result.Rotated)
		assert.Len(t, result.Failed, 1)
		assertUnresolved(t, fc)
	})

	t.Run("completed state write fails", func(t *testing.T) {
		t.Parallel()
		fc, result := run(t, func(fc *forge.FakeClient) forge.Client {
			return &failStateAfterStoreClient{FakeClient: fc}
		})
		assert.NotEmpty(t, result.Failed)
		assertUnresolved(t, fc)
	})
}

// failStateAfterStoreClient fails rotation-state writes once a secret has been
// stored, as when the process loses its write after publication.
type failStateAfterStoreClient struct {
	*forge.FakeClient
	stored bool
}

func (c *failStateAfterStoreClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if err := c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value); err != nil {
		return err
	}
	c.stored = true
	return nil
}

func (c *failStateAfterStoreClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, masked bool) error {
	if c.stored && name == forge.VarGitLabRoleRotation {
		return errors.New("state write lost")
	}
	return c.FakeClient.UpdateCIVariable(ctx, owner, repo, name, value, masked)
}
