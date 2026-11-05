package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// publishedValue reports whether value was written as a repository secret.
func publishedValue(fc *forge.FakeClient, value string) bool {
	for _, rec := range fc.CreatedSecrets {
		if rec.Value == value {
			return true
		}
	}
	return false
}

// A supplied Poller's protected-branch pipeline access is established after the
// supplied owner is recorded and before the credential is published, in both
// the rotation and the provisioning enrollment paths. A failed grant leaves the
// previous credential installed and the replacement unpublished.
func TestSuppliedPollerEnrollmentEstablishesPipelineAccessBeforePublication(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	const provided = "administrator-poller-token"
	identity := describedIdentity(gitlabroles.PollerTokenName, 888, 14)

	type enroll func(t *testing.T, fc *forge.FakeClient, tokens ServiceAccountTokenClient) (failedReasons []string)
	paths := map[string]struct {
		seed   func(t *testing.T) *forge.FakeClient
		enroll enroll
	}{
		"rotation": {
			seed: func(t *testing.T) *forge.FakeClient { return seededRoleClient(t, gitlabroles.RolePoller) },
			enroll: func(t *testing.T, fc *forge.FakeClient, tokens ServiceAccountTokenClient) []string {
				result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
					Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
					Force: true, Now: now,
					ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: provided},
				})
				require.NoError(t, err)
				return failureReasons(result.Failed)
			},
		},
		"provisioning": {
			seed: func(t *testing.T) *forge.FakeClient { return provisionClient(t) },
			enroll: func(t *testing.T, fc *forge.FakeClient, tokens ServiceAccountTokenClient) []string {
				result, err := ProvisionGitLabRoleCredentials(context.Background(), RoleProvisionConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
					Registry: gitlabroles.BuiltinRegistry(), Now: now,
					ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: provided},
				})
				require.NoError(t, err)
				return failureReasons(result.Failed)
			},
		},
	}

	for name, path := range paths {
		t.Run(name+"/granted before publication", func(t *testing.T) {
			t.Parallel()
			fc := path.seed(t)
			tokens := verifyingTokens(&fakeTokens{}, identity)
			var calls []int
			recordedBeforeGrant := false
			tokens.EstablishSuppliedPollerAccess = func(_ context.Context, _, _ string, userID int) error {
				calls = append(calls, userID)
				recordedBeforeGrant = readRoleState(t, fc, gitlabroles.RolePoller).SuppliedUserID == 888
				assert.False(t, publishedValue(fc, provided), "the grant precedes publication")
				return nil
			}
			assert.Empty(t, path.enroll(t, fc, tokens))
			assert.Equal(t, []int{888}, calls)
			assert.True(t, recordedBeforeGrant, "the supplied owner is recorded before the grant")
			assert.True(t, publishedValue(fc, provided))
		})

		t.Run(name+"/grant failure keeps the previous credential", func(t *testing.T) {
			t.Parallel()
			fc := path.seed(t)
			tokens := verifyingTokens(&fakeTokens{}, identity)
			tokens.EstablishSuppliedPollerAccess = func(context.Context, string, string, int) error {
				return errors.New("raw server detail")
			}
			failed := path.enroll(t, fc, tokens)
			require.Len(t, failed, 1)
			assert.Contains(t, failed[0], "protected-branch pipeline access")
			assert.Contains(t, failed[0], "credential not stored")
			assert.NotContains(t, failed[0], "raw server detail")
			assert.False(t, publishedValue(fc, provided), "a Poller that cannot dispatch is never published")
		})
	}

	t.Run("other roles skip the grant", func(t *testing.T) {
		t.Parallel()
		called := false
		tokens := ServiceAccountTokenClient{EstablishSuppliedPollerAccess: func(context.Context, string, string, int) error {
			called = true
			return errors.New("must not be called")
		}}
		require.NoError(t, establishSuppliedPollerAccess(context.Background(), tokens, "group", "project", gitlabroles.CoderTokenName, describedCoderIdentity(777, 13)))
		assert.False(t, called)
	})
}

// A merge grant added during a failed supplied-Poller enrollment is rolled back
// by its exact protected-branch entry ID after a failed reread and after a
// publication proven not to have been committed, on both enrollment paths. A
// grant that cannot be safely removed (publication that may have been
// committed, an unreadable rule, an entry without an ID, or a failed removal)
// is retained and reported in result.Diagnostics. A grant that existed before
// enrollment is never removed or reported.
func TestSuppliedPollerEnrollmentRollsBackMergeGrant(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	const provided = "administrator-poller-token"
	identity := describedIdentity(gitlabroles.PollerTokenName, 888, 14)

	type run func(ctx context.Context, t *testing.T, fc *forge.FakeClient, tokens ServiceAccountTokenClient) (failed int, diagnostics []string)
	paths := map[string]struct {
		seed func(t *testing.T) *forge.FakeClient
		run  run
	}{
		"rotation": {
			seed: func(t *testing.T) *forge.FakeClient { return seededRoleClient(t, gitlabroles.RolePoller) },
			run: func(ctx context.Context, t *testing.T, fc *forge.FakeClient, tokens ServiceAccountTokenClient) (int, []string) {
				result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
					Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
					Force: true, Now: now,
					ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: provided},
				})
				require.NoError(t, err)
				return len(result.Failed), result.Diagnostics
			},
		},
		"provisioning": {
			seed: func(t *testing.T) *forge.FakeClient { return provisionClient(t) },
			run: func(ctx context.Context, t *testing.T, fc *forge.FakeClient, tokens ServiceAccountTokenClient) (int, []string) {
				result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
					Registry: gitlabroles.BuiltinRegistry(), Now: now,
					ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: provided},
				})
				require.NoError(t, err)
				return len(result.Failed), result.Diagnostics
			},
		},
	}
	grantedUsers := func(fc *forge.FakeClient) []int {
		var ids []int
		for _, g := range fc.GrantedProtectedBranchMergeUsers {
			ids = append(ids, g.UserID)
		}
		return ids
	}
	retained := func(diagnostics []string) []string {
		var out []string
		for _, d := range diagnostics {
			if strings.Contains(d, "Protected branches") && strings.Contains(d, "888") {
				out = append(out, d)
			}
		}
		return out
	}

	for name, path := range paths {
		setup := func(t *testing.T, preGranted bool) *forge.FakeClient {
			fc := path.seed(t)
			fc.Repos = []forge.Repository{{Name: "project", FullName: "group/project", DefaultBranch: "main"}}
			rule := maintainerOnlyRule("main")
			if preGranted {
				rule.MergeAccessLevels = append(rule.MergeAccessLevels, forge.ProtectedBranchAccess{UserID: 888})
			}
			fc.ProtectedBranchRules["group/project/main"] = rule
			return fc
		}
		// grants adds the merge grant the way the live callback does, then
		// reports the given result.
		grants := func(fc *forge.FakeClient, result error) ServiceAccountTokenClient {
			tokens := verifyingTokens(&fakeTokens{}, identity)
			tokens.EstablishSuppliedPollerAccess = func(ctx context.Context, owner, repo string, userID int) error {
				if err := fc.GrantProtectedBranchMergeUser(ctx, owner, repo, "main", userID); err != nil {
					return err
				}
				return result
			}
			return tokens
		}

		// previousOwner makes the installed credential authenticate as a
		// different owner than the supplied replacement, which proves a failed
		// replacement write was not committed (rotation only).
		previousOwner := func(tokens ServiceAccountTokenClient) ServiceAccountTokenClient {
			tokens.InstalledRoleCredentialSelf = func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
				return SuppliedIdentity{UserID: 555, TokenID: 9, Active: true, ExpiresAt: "2027-06-01"}, true, nil
			}
			return tokens
		}
		// afterGrant runs fn once the grant has been added, so a fault can be
		// injected without disturbing the reads that precede enrollment.
		afterGrant := func(tokens ServiceAccountTokenClient, fn func()) ServiceAccountTokenClient {
			establish := tokens.EstablishSuppliedPollerAccess
			tokens.EstablishSuppliedPollerAccess = func(ctx context.Context, owner, repo string, userID int) error {
				err := establish(ctx, owner, repo, userID)
				fn()
				return err
			}
			return tokens
		}
		mergeEntriesFor := func(fc *forge.FakeClient, userID int) []forge.ProtectedBranchAccess {
			var out []forge.ProtectedBranchAccess
			for _, l := range fc.ProtectedBranchRules["group/project/main"].MergeAccessLevels {
				if l.UserID == userID {
					out = append(out, l)
				}
			}
			return out
		}

		t.Run(name+"/reread failure after the grant is rolled back", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			failed, diagnostics := path.run(context.Background(), t, fc, grants(fc, errors.New("reread failed")))
			assert.Equal(t, 1, failed)
			assert.Equal(t, []int{888}, grantedUsers(fc))
			require.Len(t, fc.RevokedProtectedBranchMergeEntries, 1, "exactly the added entry is removed")
			rev := fc.RevokedProtectedBranchMergeEntries[0]
			assert.Equal(t, 888, rev.UserID)
			assert.Positive(t, rev.EntryID)
			assert.Empty(t, mergeEntriesFor(fc, 888), "no grant is left behind")
			assert.Equal(t, []forge.ProtectedBranchAccess{{AccessLevel: 40}}, fc.ProtectedBranchRules["group/project/main"].MergeAccessLevels, "other entries are untouched")
			assert.Empty(t, retained(diagnostics), "a removed grant needs no warning")
			assert.False(t, publishedValue(fc, provided))
		})

		t.Run(name+"/reread failure rolls back under a cancelled context", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tokens := verifyingTokens(&fakeTokens{}, identity)
			tokens.EstablishSuppliedPollerAccess = func(c context.Context, owner, repo string, userID int) error {
				require.NoError(t, fc.GrantProtectedBranchMergeUser(c, owner, repo, "main", userID))
				cancel()
				return context.Canceled
			}
			// The enrollment may fail before or after the cancel is observed;
			// whichever way, the grant must not be left behind.
			_, _ = path.run(ctx, t, fc, tokens)
			require.Len(t, fc.RevokedProtectedBranchMergeEntries, 1)
			rev := fc.RevokedProtectedBranchMergeEntries[0]
			assert.NoError(t, rev.CtxErr, "cleanup runs detached from the cancelled enrollment context")
			assert.True(t, rev.HasDeadline, "cleanup is bounded")
			assert.Empty(t, mergeEntriesFor(fc, 888))
		})

		t.Run(name+"/publication failure proven uncommitted is rolled back", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			tokens := previousOwner(grants(fc, nil))
			fc.Errors["CreateRepoSecret"] = errors.New("write failed")
			failed, diagnostics := path.run(context.Background(), t, fc, tokens)
			// Provisioning also fails its other roles on the shared write error.
			assert.GreaterOrEqual(t, failed, 1)
			assert.Equal(t, []int{888}, grantedUsers(fc))
			require.Len(t, fc.RevokedProtectedBranchMergeEntries, 1)
			assert.Empty(t, mergeEntriesFor(fc, 888), "no grant is left behind")
			assert.Empty(t, retained(diagnostics))
		})

		t.Run(name+"/publication failure that may have been committed keeps the grant", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			tokens := afterGrant(grants(fc, nil), func() {
				// The failed write was in fact committed: provisioning reads the
				// secret back as present, and rotation's installed credential
				// authenticates as the replacement's own owner.
				fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
			})
			fc.Errors["CreateRepoSecret"] = errors.New("write failed")
			tokens.InstalledRoleCredentialSelf = func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
				return SuppliedIdentity{UserID: 888, TokenID: 14, Active: true, ExpiresAt: "2027-06-01"}, true, nil
			}
			failed, diagnostics := path.run(context.Background(), t, fc, tokens)
			assert.GreaterOrEqual(t, failed, 1)
			assert.Empty(t, fc.RevokedProtectedBranchMergeEntries, "a credential that may be installed keeps its access")
			assert.Len(t, mergeEntriesFor(fc, 888), 1)
			require.Len(t, retained(diagnostics), 1, "the retained grant is reported")
			assert.Contains(t, retained(diagnostics)[0], "retained")
		})

		t.Run(name+"/publication failure with an unverifiable installed credential keeps the grant", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			tokens := afterGrant(grants(fc, nil), func() {
				fc.Secrets["group/project/"+forge.SecretGitLabPollerToken] = true
			})
			fc.Errors["CreateRepoSecret"] = errors.New("write failed")
			tokens.InstalledRoleCredentialSelf = func(context.Context, string, string, string) (SuppliedIdentity, bool, error) {
				return SuppliedIdentity{}, true, errors.New("authentication unavailable")
			}
			_, diagnostics := path.run(context.Background(), t, fc, tokens)
			assert.Empty(t, fc.RevokedProtectedBranchMergeEntries)
			assert.Len(t, mergeEntriesFor(fc, 888), 1)
			require.Len(t, retained(diagnostics), 1)
		})

		t.Run(name+"/failed cleanup keeps the warning", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			fc.Errors["RevokeProtectedBranchMergeUserEntry"] = errors.New("revoke refused: raw server detail")
			failed, diagnostics := path.run(context.Background(), t, fc, grants(fc, errors.New("reread failed")))
			assert.Equal(t, 1, failed)
			require.Len(t, fc.RevokedProtectedBranchMergeEntries, 1)
			assert.Len(t, mergeEntriesFor(fc, 888), 1, "the grant remains when removal fails")
			require.Len(t, retained(diagnostics), 1, "the remaining grant is reported")
			assert.Contains(t, retained(diagnostics)[0], "retained")
			assert.Contains(t, retained(diagnostics)[0], "entry")
			assert.NotContains(t, retained(diagnostics)[0], "raw server detail")
		})

		t.Run(name+"/an entry without an ID is reported, not removed", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			tokens := verifyingTokens(&fakeTokens{}, identity)
			tokens.EstablishSuppliedPollerAccess = func(context.Context, string, string, int) error {
				rule := fc.ProtectedBranchRules["group/project/main"]
				rule.MergeAccessLevels = append(rule.MergeAccessLevels, forge.ProtectedBranchAccess{UserID: 888})
				return errors.New("reread failed")
			}
			failed, diagnostics := path.run(context.Background(), t, fc, tokens)
			assert.Equal(t, 1, failed)
			assert.Empty(t, fc.RevokedProtectedBranchMergeEntries, "an entry that cannot be identified is never removed by user ID")
			assert.Len(t, mergeEntriesFor(fc, 888), 1)
			require.Len(t, retained(diagnostics), 1)
		})

		t.Run(name+"/an administrator's other entries are untouched", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			rule := fc.ProtectedBranchRules["group/project/main"]
			rule.MergeAccessLevels = append(rule.MergeAccessLevels, forge.ProtectedBranchAccess{ID: 31, UserID: 999}, forge.ProtectedBranchAccess{ID: 32, GroupID: 7})
			rule.PushAccessLevels = append(rule.PushAccessLevels, forge.ProtectedBranchAccess{ID: 33, UserID: 999})
			failed, _ := path.run(context.Background(), t, fc, grants(fc, errors.New("reread failed")))
			assert.Equal(t, 1, failed)
			after := fc.ProtectedBranchRules["group/project/main"]
			assert.Equal(t, []forge.ProtectedBranchAccess{{AccessLevel: 40}, {ID: 31, UserID: 999}, {ID: 32, GroupID: 7}}, after.MergeAccessLevels)
			assert.Equal(t, []forge.ProtectedBranchAccess{{AccessLevel: 40}, {ID: 33, UserID: 999}}, after.PushAccessLevels)
		})

		t.Run(name+"/a grant that predates enrollment is not removed or reported", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, true)
			rule := fc.ProtectedBranchRules["group/project/main"]
			rule.MergeAccessLevels[len(rule.MergeAccessLevels)-1].ID = 77
			failed, diagnostics := path.run(context.Background(), t, fc, grants(fc, errors.New("reread failed")))
			assert.Equal(t, 1, failed)
			assert.Empty(t, retained(diagnostics))
			assert.Empty(t, fc.RevokedProtectedBranchMergeEntries, "an existing administrator grant is never removed")
			assert.Equal(t, []forge.ProtectedBranchAccess{{AccessLevel: 40}, {ID: 77, UserID: 888}}, fc.ProtectedBranchRules["group/project/main"].MergeAccessLevels)
		})

		t.Run(name+"/no grant is reported when none was added", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			tokens := verifyingTokens(&fakeTokens{}, identity)
			tokens.EstablishSuppliedPollerAccess = func(context.Context, string, string, int) error {
				return errors.New("grant refused")
			}
			failed, diagnostics := path.run(context.Background(), t, fc, tokens)
			assert.Equal(t, 1, failed)
			assert.Empty(t, retained(diagnostics))
		})

		t.Run(name+"/an unreadable rule is reported as possibly retained", func(t *testing.T) {
			t.Parallel()
			fc := setup(t, false)
			tokens := verifyingTokens(&fakeTokens{}, identity)
			tokens.EstablishSuppliedPollerAccess = func(ctx context.Context, owner, repo string, userID int) error {
				require.NoError(t, fc.GrantProtectedBranchMergeUser(ctx, owner, repo, "main", userID))
				fc.Errors["GetProtectedBranch"] = errors.New("rule lookup failed: raw server detail")
				return errors.New("reread failed")
			}
			failed, diagnostics := path.run(context.Background(), t, fc, tokens)
			assert.Equal(t, 1, failed)
			require.Len(t, retained(diagnostics), 1)
			assert.Contains(t, retained(diagnostics)[0], "may remain")
			assert.NotContains(t, retained(diagnostics)[0], "raw server detail")
		})
	}
}

// A supplied-Poller enrollment that fails before anything is published (the
// protected-branch access cannot be established) restores the previous phase
// and error, so the unresolved-publication hold does not block a later ordinary
// rotation or install convergence of the still-installed credential.
func TestSuppliedPollerRotationAccessFailureDoesNotHoldPublication(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	const provided = "administrator-poller-token"
	identity := describedIdentity(gitlabroles.PollerTokenName, 888, 14)

	fc := seededRoleClient(t, gitlabroles.RolePoller)
	tokens := verifyingTokens(&fakeTokens{}, identity)
	tokens.EstablishSuppliedPollerAccess = func(context.Context, string, string, int) error {
		return errors.New("transient grant failure")
	}
	result, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
		Force: true, Now: now,
		ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: provided},
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.False(t, publishedValue(fc, provided))

	rs := readRoleState(t, fc, gitlabroles.RolePoller)
	assert.False(t, providedPublicationUnresolved(rs), "a pre-publication failure is not an unresolved publication")
	assert.NotEqual(t, rotationPhaseFailed, rs.Phase)
	assert.Empty(t, rs.Error)
	assert.Contains(t, rs.ExcludedUserIDs, 888, "the replacement owner stays excluded")

	retry, err := RotateGitLabRoleCredentials(context.Background(), RoleRotateConfig{
		Owner: "group", Repo: "project", Client: fc, Tokens: verifyingTokens(&fakeTokens{}, identity),
		Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
		Now: now,
	})
	require.NoError(t, err)
	for _, reason := range failureReasons(retry.Failed) {
		assert.NotContains(t, reason, "publication was interrupted", "the publication hold is not triggered")
	}
}

func failureReasons(failed []RoleProvisionFailure) []string {
	var out []string
	for _, f := range failed {
		out = append(out, f.Reason)
	}
	return out
}

// Rotation never retains an enrolled supplied credential through a token client
// that cannot authenticate it or verify its owner's access: the role is
// reported as unverifiable and the credential is left untouched.
func TestRotateGitLabRoleCredentials_UnverifiableSuppliedCredentialFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	const state = `{"version":2,"roles":{"coder":{"phase":"idle","distributed_at":"2026-10-01T00:00:00Z","supplied":true,"supplied_user_id":777,"supplied_token_id":13,"excluded_user_ids":[777]}}}`
	listed := ProjectAccessToken{ID: 13, Active: true, ExpiresAt: "2027-06-01", UserID: 777}
	listed.Name = gitlabroles.BuiltinRegistry().Registrations()[2].Credential.TokenName

	cases := map[string]func() ProjectAccessTokenClient{
		"service-account client without the installed-credential check": func() ProjectAccessTokenClient {
			return ServiceAccountTokenClient{Legacy: &fakeTokens{}, VerifySuppliedAccess: allowSuppliedAccess}
		},
		"generic client with an unlisted credential": func() ProjectAccessTokenClient { return &fakeTokens{} },
		"generic client with a listed credential": func() ProjectAccessTokenClient {
			generic := &fakeTokens{}
			generic.seed(listed)
			return generic
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fc := seededRoleClient(t, gitlabroles.RoleCoder)
			require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, state, true))
			tokens := build()

			result, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
				Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
				Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RoleCoder}, Now: now,
			})
			require.NoError(t, err)
			require.Len(t, result.Failed, 1)
			assert.Equal(t, gitlabroles.RoleCoder, result.Failed[0].Role)
			assert.Contains(t, result.Failed[0].Reason, "credential left untouched")
			assert.NotContains(t, result.Skipped, gitlabroles.RoleCoder, "an unverified credential is not reported retained")
			assert.Empty(t, result.Rotated)
			rs := readRoleState(t, fc, gitlabroles.RoleCoder)
			assert.True(t, rs.Supplied)
			assert.Equal(t, 777, rs.SuppliedUserID)
		})
	}
}

// A merge grant whose removal fails must reach the operator even when the run
// then fails outright: callers discard the partial result when an error is
// returned, so the warning is carried by the returned error. A grant that is
// removed is not reported. The final credential-presence read, a cancelled
// context, and a failed lease release each fail the run after the enrollment
// already added the grant; removal runs on a context detached from the
// cancelled run.
func TestSuppliedPollerEnrollmentRetainedGrantSurvivesRunFailure(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	const provided = "administrator-poller-token"
	identity := describedIdentity(gitlabroles.PollerTokenName, 888, 14)

	type run func(ctx context.Context, fc *forge.FakeClient, tokens ServiceAccountTokenClient) error
	paths := map[string]struct {
		seed func(t *testing.T) *forge.FakeClient
		run  run
	}{
		"rotation": {
			seed: func(t *testing.T) *forge.FakeClient { return seededRoleClient(t, gitlabroles.RolePoller) },
			run: func(ctx context.Context, fc *forge.FakeClient, tokens ServiceAccountTokenClient) error {
				_, err := RotateGitLabRoleCredentials(ctx, RoleRotateConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
					Registry: gitlabroles.BuiltinRegistry(), Roles: []gitlabroles.Role{gitlabroles.RolePoller},
					Force: true, Now: now,
					ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: provided},
				})
				return err
			},
		},
		"provisioning": {
			seed: func(t *testing.T) *forge.FakeClient { return provisionClient(t) },
			run: func(ctx context.Context, fc *forge.FakeClient, tokens ServiceAccountTokenClient) error {
				_, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
					Owner: "group", Repo: "project", Client: fc, Tokens: tokens,
					Registry: gitlabroles.BuiltinRegistry(), Now: now,
					ProvidedTokens: map[gitlabroles.Role]string{gitlabroles.RolePoller: provided},
				})
				return err
			},
		},
	}

	// failures break the run after the grant is added and the enrollment fails.
	failures := map[string]func(fc *forge.FakeClient, cancel context.CancelFunc){
		"final credential-presence read fails": func(fc *forge.FakeClient, _ context.CancelFunc) {
			fc.Errors["RepoSecretExists"] = errors.New("presence read failed")
		},
		"context is cancelled": func(fc *forge.FakeClient, cancel context.CancelFunc) {
			cancel()
			fc.Errors["RepoSecretExists"] = context.Canceled
		},
		"lease release fails": func(fc *forge.FakeClient, _ context.CancelFunc) {
			fc.Errors["ReleaseProjectLease"] = errors.New("release failed")
		},
	}

	for pathName, path := range paths {
		for failName, fail := range failures {
			// A grant whose removal fails is retained and must be reported; a
			// grant that is removed leaves nothing to report even though the
			// run fails.
			for _, removalFails := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/removal fails=%t", pathName, failName, removalFails), func(t *testing.T) {
					t.Parallel()
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					fc := path.seed(t)
					fc.Repos = []forge.Repository{{Name: "project", FullName: "group/project", DefaultBranch: "main"}}
					fc.ProtectedBranchRules["group/project/main"] = maintainerOnlyRule("main")
					tokens := verifyingTokens(&fakeTokens{}, identity)
					tokens.EstablishSuppliedPollerAccess = func(ctx context.Context, owner, repo string, userID int) error {
						if err := fc.GrantProtectedBranchMergeUser(ctx, owner, repo, "main", userID); err != nil {
							return err
						}
						if removalFails {
							fc.Errors["RevokeProtectedBranchMergeUserEntry"] = errors.New("revoke refused")
						}
						fail(fc, cancel)
						return errors.New("reread failed")
					}

					err := path.run(ctx, fc, tokens)

					require.Error(t, err)
					assert.Equal(t, []int{888}, func() []int {
						var ids []int
						for _, g := range fc.GrantedProtectedBranchMergeUsers {
							ids = append(ids, g.UserID)
						}
						return ids
					}())
					require.Len(t, fc.RevokedProtectedBranchMergeEntries, 1, "the added entry is removed once")
					assert.NoError(t, fc.RevokedProtectedBranchMergeEntries[0].CtxErr, "cleanup is detached from a cancelled run")
					if removalFails {
						assert.Contains(t, err.Error(), "Protected branches", "the retained grant warning is carried by the error")
						assert.Contains(t, err.Error(), "888")
						assert.Contains(t, err.Error(), "retained")
						return
					}
					assert.NotContains(t, err.Error(), "Protected branches", "a removed grant is not reported")
					for _, l := range fc.ProtectedBranchRules["group/project/main"].MergeAccessLevels {
						assert.NotEqual(t, 888, l.UserID, "the grant is gone")
					}
				})
			}
		}
	}

	t.Run("diagnostics that look like credential material are not attached", func(t *testing.T) {
		t.Parallel()
		base := errors.New("run failed")
		assert.Same(t, base, attachDiagnosticsToError(base, []string{"leaked glpat-secret"}))
		assert.Same(t, base, attachDiagnosticsToError(base, nil))
		assert.NoError(t, attachDiagnosticsToError(nil, []string{"note"}))
		assert.ErrorIs(t, attachDiagnosticsToError(base, []string{"note"}), base)
	})
}
