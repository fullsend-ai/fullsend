package repos

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// otherMergeUserID holds an administrator's own per-user merge grant on the
// default branch, which retirement must never touch.
const otherMergeUserID = 4242

// retiringTriggerOwner is a handoff trigger owner that also retires
// superseded Poller generations, as the live GitLab adapter does.
type retiringTriggerOwner struct {
	*handoffTriggerOwner

	blocked    string
	blockedErr error
	retireErr  error
	retired    []int64
}

var _ GitLabPollerRetirement = (*retiringTriggerOwner)(nil)

func newRetiringTriggerOwner(c webhookFake) *retiringTriggerOwner {
	h := newHandoffTriggerOwner(c)
	h.resolveFromRotation = true
	return &retiringTriggerOwner{handoffTriggerOwner: h}
}

func (r *retiringTriggerOwner) PollerRetirementBlocked(context.Context, string, string, int64) (string, error) {
	return r.blocked, r.blockedErr
}

func (r *retiringTriggerOwner) RetirePollerAccount(_ context.Context, _, _ string, uid int64) error {
	r.retired = append(r.retired, uid)
	if r.retireErr != nil {
		return r.retireErr
	}
	delete(r.c.ProjectMemberAccess, uid)
	return nil
}

// seedDefaultBranchGrants protects the default branch with a role-based
// Developer merge grant (branch-wide access), a Maintainer push grant, and an
// administrator's own per-user merge and push grants, then adds the per-user
// merge grant EnsureGitLabPollerPipelineAccess gives the running Poller.
func seedDefaultBranchGrants(t *testing.T, c webhookFake) {
	t.Helper()
	c.ProtectedBranchRules[webhookTestOwner+"/"+webhookTestRepo+"/main"] = &forge.ProtectedBranchRule{
		Name: "main",
		MergeAccessLevels: []forge.ProtectedBranchAccess{
			{ID: 1, AccessLevel: forge.GitLabAccessLevelDeveloper},
			{ID: 2, UserID: otherMergeUserID},
		},
		PushAccessLevels: []forge.ProtectedBranchAccess{
			{ID: 3, AccessLevel: forge.GitLabAccessLevelMaintainer},
			{ID: 4, UserID: otherMergeUserID},
		},
	}
	require.NoError(t, c.GrantProtectedBranchMergeUser(context.Background(), webhookTestOwner, webhookTestRepo, "main", webhookPollerUserID))
}

func defaultBranchRule(t *testing.T, c webhookFake) forge.ProtectedBranchRule {
	t.Helper()
	rule := c.ProtectedBranchRules[webhookTestOwner+"/"+webhookTestRepo+"/main"]
	require.NotNil(t, rule)
	return *rule
}

// mergeGrantUsers lists the user IDs with an individual merge grant.
func mergeGrantUsers(rule forge.ProtectedBranchRule) []int {
	var ids []int
	for _, l := range rule.MergeAccessLevels {
		if l.UserID > 0 {
			ids = append(ids, l.UserID)
		}
	}
	return ids
}

// agePollerDistribution moves the recorded distribution of the current Poller
// credential back by age, as if that much time had passed since cutover.
func agePollerDistribution(t *testing.T, c forge.Client, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	state, _, err := loadRotationState(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	rs := state.Roles[string(gitlabroles.RolePoller)]
	rs.DistributedAt = time.Now().Add(-age).UTC().Format(time.RFC3339)
	state.Roles[string(gitlabroles.RolePoller)] = rs
	require.NoError(t, writeRotationState(ctx, c, webhookTestOwner, webhookTestRepo, state))
}

func livePollerAccounts(st PollerGenerationState) int {
	n := 0
	for _, id := range []int64{st.CurrentUserID, st.RetiringUserID} {
		if id > 0 {
			n++
		}
	}
	if st.Pending != nil {
		n++
	}
	return n
}

func needsWebhook(t *testing.T, c forge.Client) bool {
	t.Helper()
	needs, err := GitLabWebhookNeedsProvisioning(context.Background(), c, webhookTestBase, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	return needs
}

// Repeated install and rotation converge to one old/new Poller pair and then
// retire the superseded generation, which leaves every merge grant on the
// protected default branch in place.
func TestEnsureGitLabWebhookFastPath_RepeatedInstallAndRotateRetireSupersededGeneration(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	seedDefaultBranchGrants(t, c)
	ro := newRetiringTriggerOwner(c)

	// First install hands off to a fresh generation: one old/new pair.
	_, err := ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}, generationState(t, c))
	assert.Empty(t, ro.retired, "the superseded Poller is not retired in the cutover run")
	assert.True(t, needsWebhook(t, c), "a retiring generation is install work")

	// A reinstall inside the rotation grace leaves the pair in place and says
	// why, without changing anything.
	res, err := ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Equal(t, "none", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "rotation grace")
	assert.Empty(t, ro.retired)
	assert.Contains(t, mergeGrantUsers(defaultBranchRule(t, c)), webhookPollerUserID)

	// Once the grace has elapsed an ordinary reinstall retires it.
	agePollerDistribution(t, c, gitlabroles.DefaultRotationGrace+time.Hour)
	before := defaultBranchRule(t, c)
	res, err = ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Equal(t, "update", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "Retired the superseded GitLab Poller generation (user ID 4004)")
	assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID}, generationState(t, c))
	after := defaultBranchRule(t, c)
	assert.Equal(t, before, after, "the protected-branch rule, including per-user merge grants of unknown provenance, is unchanged")
	assert.Contains(t, mergeGrantUsers(after), webhookPollerUserID)
	assert.Empty(t, c.RevokedProtectedBranchMergeEntries)
	assert.False(t, needsWebhook(t, c))
	assert.Len(t, c.hooks(), 1, "the fast path stays in service")
	assertNoCredentialLeak(t, c, res)

	// A forced rotation now starts the next generation, and the cycle repeats
	// without ever exceeding one old/new pair.
	const thirdUID = int64(6006)
	ro.accountUID = thirdUID
	_, err = ensureWebhookAsPoller(c, ro, true)
	require.NoError(t, err)
	st := generationState(t, c)
	assert.Equal(t, PollerGenerationState{CurrentUserID: thirdUID, RetiringUserID: cutoverNewUID}, st)
	assert.LessOrEqual(t, livePollerAccounts(st), 2)

	// Rotating again before retirement starts no further generation.
	ro.accountUID = 7007
	res, err = ensureWebhookAsPoller(c, ro, true)
	require.NoError(t, err)
	assert.Equal(t, "deferred", res.Action)
	assert.Contains(t, strings.Join(res.Details, "\n"), "at most one old/new Poller pair")
	assert.Equal(t, 2, ro.accounts)

	agePollerDistribution(t, c, gitlabroles.DefaultRotationGrace+time.Hour)
	_, err = ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Equal(t, []int64{webhookPollerUserID, cutoverNewUID}, ro.retired)
	assert.Equal(t, PollerGenerationState{CurrentUserID: thirdUID}, generationState(t, c))
}

// An inconclusive account retirement keeps the generation recorded as
// retiring, so the next install retries and no further generation starts.
func TestEnsureGitLabWebhookFastPath_InconclusiveRetirementKeepsRetiringRecord(t *testing.T) {
	c := newWebhookFake()
	ownedBy(c, webhookMaintainerUserID, forge.GitLabAccessLevelMaintainer)
	ro := newRetiringTriggerOwner(c)
	_, err := ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	agePollerDistribution(t, c, gitlabroles.DefaultRotationGrace+time.Hour)

	ro.retireErr = errors.New("service account 4004 has unfinished jobs; not deleted")
	_, err = ensureWebhookAsPollerOn(c, ro)
	require.ErrorContains(t, err, "stays recorded as retiring")
	assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)

	// The retry is idempotent and completes.
	ro.retireErr = nil
	_, err = ensureWebhookAsPollerOn(c, ro)
	require.NoError(t, err)
	assert.Equal(t, []int64{webhookPollerUserID, webhookPollerUserID}, ro.retired)
	assert.Zero(t, generationState(t, c).RetiringUserID)
}

// retirementFixture seeds a project whose cutover to cutoverNewUID is
// recorded, with the grace elapsed, so retirement of webhookPollerUserID may
// proceed.
func retirementFixture(t *testing.T) (webhookFake, *retiringTriggerOwner) {
	t.Helper()
	return seedRetirement(t, newWebhookFake())
}

// seedRetirement records the cutover on an existing project fake.
func seedRetirement(t *testing.T, c webhookFake) (webhookFake, *retiringTriggerOwner) {
	t.Helper()
	ctx := context.Background()
	ro := newRetiringTriggerOwner(c)
	require.NoError(t, writePollerGenerations(ctx, c, webhookTestOwner, webhookTestRepo, PollerGenerationState{CurrentUserID: cutoverNewUID, RetiringUserID: webhookPollerUserID}))
	state, _, err := loadRotationState(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	state.Roles[string(gitlabroles.RolePoller)] = rotationRoleState{ManagedUserID: int(cutoverNewUID), Phase: rotationPhaseIdle, IncomingID: 9002, DistributedAt: time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)}
	require.NoError(t, writeRotationState(ctx, c, webhookTestOwner, webhookTestRepo, state))
	return c, ro
}

// staticTriggerEvidence is a webhookTriggerEvidence that protects fixed
// trigger IDs, or reports unresolved when it is non-empty.
func staticTriggerEvidence(unresolved string, protected ...int64) webhookTriggerEvidence {
	return func(context.Context) ([]int64, string, error) { return protected, unresolved, nil }
}

func runRetirement(t *testing.T, c forge.Client, to GitLabTriggerOwner, activeTriggerID int64) (pollerRetirementResult, error) {
	t.Helper()
	var protected []int64
	if activeTriggerID != 0 {
		protected = []int64{activeTriggerID}
	}
	return runRetirementWith(t, c, to, staticTriggerEvidence("", protected...))
}

func runRetirementWith(t *testing.T, c forge.Client, to GitLabTriggerOwner, evidence webhookTriggerEvidence) (pollerRetirementResult, error) {
	t.Helper()
	ctx := context.Background()
	lease, release, err := LockGitLabProjectLease(ctx, c, webhookTestOwner, webhookTestRepo)
	require.NoError(t, err)
	defer release(nil)
	return retireSupersededPollerGeneration(ctx, c, to, lease, webhookTestOwner, webhookTestRepo, evidence, time.Now())
}

func TestRetireSupersededPollerGeneration_NothingToDo(t *testing.T) {
	ctx := context.Background()
	c, ro := retirementFixture(t)

	res, err := retireSupersededPollerGeneration(ctx, c, ro, nil, webhookTestOwner, webhookTestRepo, staticTriggerEvidence(""), time.Now())
	require.NoError(t, err)
	assert.Equal(t, pollerRetirementResult{}, res, "no lease, no retirement")

	res, err = runRetirement(t, c, ro.handoffTriggerOwner, 0)
	require.NoError(t, err)
	assert.Equal(t, pollerRetirementResult{}, res, "a trigger owner without the capability retires nothing")

	require.NoError(t, writePollerGenerations(ctx, c, webhookTestOwner, webhookTestRepo, PollerGenerationState{CurrentUserID: cutoverNewUID}))
	res, err = runRetirement(t, c, ro, 0)
	require.NoError(t, err)
	assert.Equal(t, pollerRetirementResult{}, res, "no retiring generation")
	assert.Empty(t, ro.retired)

	c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.VarGitLabPollerGenerations] = " "
	_, err = runRetirement(t, c, ro, 0)
	require.ErrorIs(t, err, errPollerGenerationsBlank)
}

func TestRetireSupersededPollerGeneration_WaitsWithoutChanges(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, c webhookFake, ro *retiringTriggerOwner)
		want  string
	}{
		"poller unidentified": {
			setup: func(_ *testing.T, _ webhookFake, ro *retiringTriggerOwner) {
				ro.resolveFromRotation = false
				ro.uidErr = forge.ErrNotFound
			},
			want: "managed Poller could not be identified",
		},
		"cutover unconfirmed": {
			setup: func(_ *testing.T, _ webhookFake, ro *retiringTriggerOwner) { ro.resolveFromRotation = false },
			want:  "is not the recorded current generation",
		},
		"rotation names another poller": {
			setup: func(t *testing.T, c webhookFake, ro *retiringTriggerOwner) {
				ro.resolveFromRotation = false
				ro.uid = cutoverNewUID
				state, _, err := loadRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo)
				require.NoError(t, err)
				state.Roles[string(gitlabroles.RolePoller)] = rotationRoleState{ManagedUserID: 31337, DistributedAt: "2026-01-01T00:00:00Z"}
				require.NoError(t, writeRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo, state))
			},
			want: "rotation state records managed Poller user ID 31337",
		},
		"distribution unrecorded": {
			setup: func(t *testing.T, c webhookFake, _ *retiringTriggerOwner) {
				state, _, err := loadRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo)
				require.NoError(t, err)
				rs := state.Roles[string(gitlabroles.RolePoller)]
				rs.Phase = rotationPhaseDistributing
				state.Roles[string(gitlabroles.RolePoller)] = rs
				require.NoError(t, writeRotationState(context.Background(), c, webhookTestOwner, webhookTestRepo, state))
			},
			want: "distribution of the current Poller credential is not yet recorded",
		},
		"grace in force": {
			setup: func(t *testing.T, c webhookFake, _ *retiringTriggerOwner) {
				agePollerDistribution(t, c, time.Hour)
			},
			want: "stays usable for in-flight jobs until",
		},
		"unfinished jobs": {
			setup: func(_ *testing.T, _ webhookFake, ro *retiringTriggerOwner) {
				ro.blocked = "2 unfinished job(s) still run as it"
			},
			want: "2 unfinished job(s) still run as it",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, ro := retirementFixture(t)
			c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{{ID: 70, OwnerID: webhookPollerUserID}}
			seedDefaultBranchGrants(t, c)
			tc.setup(t, c, ro)

			res, err := runRetirement(t, c, ro, 0)
			require.NoError(t, err)
			assert.Empty(t, res.details)
			assert.Contains(t, res.waiting, "superseded GitLab Poller generation (user ID 4004) is waiting")
			assert.Contains(t, res.waiting, tc.want)
			assert.Empty(t, ro.retired)
			assert.Empty(t, c.RevokedTriggerTokenIDs)
			assert.Empty(t, c.RevokedProtectedBranchMergeEntries)
			assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
		})
	}
}

func TestRetireSupersededPollerGeneration_FailsClosedBeforeChanges(t *testing.T) {
	for name, tc := range map[string]struct {
		setup  func(c webhookFake, ro *retiringTriggerOwner)
		active int64
		want   string
	}{
		"identity lookup": {
			setup: func(_ webhookFake, ro *retiringTriggerOwner) {
				ro.resolveFromRotation = false
				ro.uidErr = errors.New("500 glpat-server-echo")
			},
			want: "resolving the GitLab Poller identity",
		},
		"rotation state unreadable": {
			setup: func(c webhookFake, ro *retiringTriggerOwner) {
				ro.resolveFromRotation = false
				ro.uid = cutoverNewUID
				c.VariableValues[webhookTestOwner+"/"+webhookTestRepo+"/"+forge.VarGitLabRoleRotation] = "{not json"
			},
			want: "reading",
		},
		"blocked check fails": {
			setup: func(_ webhookFake, ro *retiringTriggerOwner) { ro.blockedErr = errors.New("listing jobs failed") },
			want:  "checking whether the superseded GitLab Poller (user ID 4004) can be retired",
		},
		"trigger listing": {
			setup: func(c webhookFake, _ *retiringTriggerOwner) {
				c.Errors["ListPipelineTriggerTokens"] = errors.New("500 glpat-server-echo")
			},
			want: "listing pipeline trigger tokens before retiring",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, ro := retirementFixture(t)
			c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{{ID: 70, OwnerID: webhookPollerUserID}}
			seedDefaultBranchGrants(t, c)
			tc.setup(c, ro)

			_, err := runRetirement(t, c, ro, tc.active)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "glpat-server-echo")
			assert.Empty(t, ro.retired)
			assert.Empty(t, c.RevokedTriggerTokenIDs)
			assert.Empty(t, c.RevokedProtectedBranchMergeEntries)
			assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
		})
	}
}

// Retirement revokes the retiring account's triggers (never the current
// Poller's), deletes the account, and clears the record, leaving a per-user
// merge grant for the retiring account in place because its creator is unknown
// (it may be an administrator's). A rerun after completion has nothing to do.
func TestRetireSupersededPollerGeneration_RetiresAndIsIdempotent(t *testing.T) {
	c, ro := retirementFixture(t)
	c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{
		{ID: 70, OwnerID: webhookPollerUserID},
		{ID: 71, OwnerID: cutoverNewUID, Description: GitLabWebhookTriggerDescription},
	}
	seedDefaultBranchGrants(t, c)

	res, err := runRetirement(t, c, ro, 71)
	require.NoError(t, err)
	assert.Empty(t, res.waiting)
	joined := strings.Join(res.details, "\n")
	assert.Contains(t, joined, "Revoked 1 pipeline trigger token(s) owned by the superseded GitLab Poller (user ID 4004)")
	assert.Contains(t, joined, "Left the protected default-branch merge grant")
	assert.Equal(t, []int64{70}, c.RevokedTriggerTokenIDs)
	assert.Equal(t, []int64{70}, res.revokedTriggerIDs)
	require.Len(t, c.triggers(), 1)
	assert.Equal(t, int64(71), c.triggers()[0].ID)
	assert.ElementsMatch(t, []int{otherMergeUserID, webhookPollerUserID}, mergeGrantUsers(defaultBranchRule(t, c)), "an administrator-created grant for the retiring user's own ID survives retirement")
	assert.Empty(t, c.RevokedProtectedBranchMergeEntries)
	assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
	assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID}, generationState(t, c))

	res, err = runRetirement(t, c, ro, 71)
	require.NoError(t, err)
	assert.Equal(t, pollerRetirementResult{}, res)
	assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
}

// A managed webhook whose active trigger cannot be identified may still embed
// a trigger the retiring account owns, so retirement postpones everything
// until the webhook trigger is reconciled.
func TestRetireSupersededPollerGeneration_UnresolvedActiveTriggerWaits(t *testing.T) {
	c, ro := retirementFixture(t)
	c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{{ID: 70, OwnerID: webhookPollerUserID}}
	seedDefaultBranchGrants(t, c)
	res, err := runRetirementWith(t, c, ro, staticTriggerEvidence("a managed webhook's active pipeline trigger cannot be identified from its credential"))
	require.NoError(t, err)
	assert.Contains(t, res.waiting, "active pipeline trigger cannot be identified")
	assert.Empty(t, res.details)
	assert.Empty(t, c.RevokedTriggerTokenIDs)
	assert.Empty(t, c.RevokedProtectedBranchMergeEntries)
	assert.Empty(t, ro.retired)
	assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
}

// triggerRevokeClient models a trigger revocation that does not stick, or one
// that a concurrent run already completed (GitLab answers not found).
type triggerRevokeClient struct {
	webhookFake
	ignoreRevoke bool
	goneRevoke   bool
}

func (c *triggerRevokeClient) RevokePipelineTriggerToken(ctx context.Context, owner, repo string, id int64) error {
	if c.ignoreRevoke {
		return nil
	}
	if err := c.webhookFake.RevokePipelineTriggerToken(ctx, owner, repo, id); err != nil {
		return err
	}
	if c.goneRevoke {
		return forge.ErrNotFound
	}
	return nil
}

// relistFailClient fails every trigger listing after the first.
type relistFailClient struct {
	webhookFake
	lists int
}

func (c *relistFailClient) ListPipelineTriggerTokens(ctx context.Context, owner, repo string) ([]forge.PipelineTriggerToken, error) {
	c.lists++
	if c.lists > 1 {
		return nil, errors.New("500 glpat-server-echo")
	}
	return c.webhookFake.ListPipelineTriggerTokens(ctx, owner, repo)
}

func TestRetireSupersededPollerGeneration_TriggerRevocationFailures(t *testing.T) {
	t.Run("revoke error", func(t *testing.T) {
		c, ro := retirementFixture(t)
		c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{{ID: 70, OwnerID: webhookPollerUserID}}
		c.Errors["RevokePipelineTriggerToken"] = errors.New("500 glpat-server-echo")
		_, err := runRetirement(t, c, ro, 0)
		require.ErrorContains(t, err, "revoking pipeline trigger token ID 70 of the superseded GitLab Poller")
		assert.NotContains(t, err.Error(), "glpat-server-echo")
		assert.Empty(t, ro.retired)
	})
	t.Run("already gone", func(t *testing.T) {
		c, ro := retirementFixture(t)
		c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{{ID: 70, OwnerID: webhookPollerUserID}}
		_, err := runRetirement(t, &triggerRevokeClient{webhookFake: c, goneRevoke: true}, ro, 0)
		require.NoError(t, err)
		assert.Empty(t, c.triggers())
		assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
	})
	t.Run("relist fails", func(t *testing.T) {
		c, ro := retirementFixture(t)
		c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{{ID: 70, OwnerID: webhookPollerUserID}}
		_, err := runRetirement(t, &relistFailClient{webhookFake: c}, ro, 0)
		require.ErrorContains(t, err, "confirming the pipeline trigger tokens of the superseded GitLab Poller are revoked")
		assert.NotContains(t, err.Error(), "glpat-server-echo")
		assert.Empty(t, ro.retired)
	})
	t.Run("still listed", func(t *testing.T) {
		c, ro := retirementFixture(t)
		c.PipelineTriggerTokens[webhookTestOwner+"/"+webhookTestRepo] = []forge.PipelineTriggerToken{{ID: 70, OwnerID: webhookPollerUserID}}
		rc := &triggerRevokeClient{webhookFake: c, ignoreRevoke: true}
		_, err := runRetirement(t, rc, ro, 0)
		require.ErrorContains(t, err, "pipeline trigger token ID 70 of the superseded GitLab Poller (user ID 4004) is still listed after revocation")
		assert.Empty(t, ro.retired)
		assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
	})
}

func TestPreservedPollerMergeGrants(t *testing.T) {
	ctx := context.Background()
	rulesKey := webhookTestOwner + "/" + webhookTestRepo + "/"

	t.Run("counts only the exact-name rule's per-user entries and removes nothing", func(t *testing.T) {
		c := newWebhookFake()
		seedDefaultBranchGrants(t, c)
		wildcard := &forge.ProtectedBranchRule{Name: "ma*", MergeAccessLevels: []forge.ProtectedBranchAccess{{ID: 50, UserID: webhookPollerUserID}}}
		c.ProtectedBranchRules[rulesKey+"ma*"] = wildcard
		n, err := preservedPollerMergeGrants(ctx, c, webhookTestOwner, webhookTestRepo, webhookPollerUserID)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.ElementsMatch(t, []int{otherMergeUserID, webhookPollerUserID}, mergeGrantUsers(defaultBranchRule(t, c)))
		assert.Len(t, c.ProtectedBranchRules[rulesKey+"ma*"].MergeAccessLevels, 1)
		assert.Empty(t, c.RevokedProtectedBranchMergeEntries)
	})
	t.Run("no grant", func(t *testing.T) {
		c := newWebhookFake()
		n, err := preservedPollerMergeGrants(ctx, c, webhookTestOwner, webhookTestRepo, webhookPollerUserID)
		require.NoError(t, err)
		assert.Zero(t, n)
	})
	t.Run("unprotected forge", func(t *testing.T) {
		c := newWebhookFake()
		c.Errors["ListProtectedBranches"] = forge.ErrNotSupported
		n, err := preservedPollerMergeGrants(ctx, c, webhookTestOwner, webhookTestRepo, webhookPollerUserID)
		require.NoError(t, err)
		assert.Zero(t, n)
	})
	t.Run("blank default branch reads main", func(t *testing.T) {
		c := newWebhookFake()
		c.Repos[0].DefaultBranch = ""
		seedDefaultBranchGrants(t, c)
		n, err := preservedPollerMergeGrants(ctx, c, webhookTestOwner, webhookTestRepo, webhookPollerUserID)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
	})
	for name, tc := range map[string]struct {
		key  string
		want string
	}{
		"project lookup": {key: "GetRepo", want: "reading project to check"},
		"listing":        {key: "ListProtectedBranches", want: "listing protected branches"},
	} {
		t.Run(name, func(t *testing.T) {
			c := newWebhookFake()
			seedDefaultBranchGrants(t, c)
			c.Errors[tc.key] = errors.New("500 glpat-server-echo")
			_, err := preservedPollerMergeGrants(ctx, c, webhookTestOwner, webhookTestRepo, webhookPollerUserID)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "glpat-server-echo")
		})
	}
}

// Retirement never removes merge grants, and a protected-branch read failure
// after the account is gone only changes the note, not the outcome.
func TestRetireSupersededPollerGeneration_MergeGrantReadFailureDoesNotBlock(t *testing.T) {
	c, ro := retirementFixture(t)
	seedDefaultBranchGrants(t, c)
	c.Errors["ListProtectedBranches"] = errors.New("500 glpat-server-echo")
	res, err := runRetirement(t, c, ro, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{webhookPollerUserID}, ro.retired)
	assert.Zero(t, generationState(t, c).RetiringUserID)
	joined := strings.Join(res.details, "\n")
	assert.Contains(t, joined, "Could not check whether the protected default branch still lists a merge grant")
	assert.NotContains(t, joined, "glpat-server-echo")
	assert.Empty(t, c.RevokedProtectedBranchMergeEntries)
}

// failGenerationClearClient fails the write that clears retiring_user_id, or
// reports that a concurrent run already cleared it.
type failGenerationClearClient struct {
	webhookFake
	fail bool
}

func (c *failGenerationClearClient) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if name == forge.VarGitLabPollerGenerations && c.fail {
		return errors.New("500 internal error")
	}
	return c.webhookFake.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

func TestRetireSupersededPollerGeneration_RecordingRetirement(t *testing.T) {
	t.Run("write fails", func(t *testing.T) {
		c, ro := retirementFixture(t)
		_, err := runRetirement(t, &failGenerationClearClient{webhookFake: c, fail: true}, ro, 0)
		require.ErrorContains(t, err, "recording the retirement of the superseded GitLab Poller (user ID 4004)")
		assert.Equal(t, int64(webhookPollerUserID), generationState(t, c).RetiringUserID)
		// The account is already gone; the retry finishes the record.
		_, err = runRetirement(t, c, ro, 0)
		require.NoError(t, err)
		assert.Zero(t, generationState(t, c).RetiringUserID)
	})
	t.Run("cleared concurrently", func(t *testing.T) {
		c, ro := retirementFixture(t)
		cc := &concurrentClearOwner{retiringTriggerOwner: ro}
		res, err := runRetirement(t, c, cc, 0)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(res.details, "\n"), "Retired the superseded GitLab Poller generation")
		assert.Equal(t, PollerGenerationState{CurrentUserID: cutoverNewUID}, generationState(t, c))
	})
	t.Run("reread fails", func(t *testing.T) {
		c, ro := retirementFixture(t)
		cc := &concurrentClearOwner{retiringTriggerOwner: ro, blank: true}
		_, err := runRetirement(t, c, cc, 0)
		require.ErrorIs(t, err, errPollerGenerationsBlank)
	})
}

// concurrentClearOwner clears (or blanks) the generation document while the
// account is deleted, as a concurrent finisher or a damaged variable would.
type concurrentClearOwner struct {
	*retiringTriggerOwner
	blank bool
}

func (o *concurrentClearOwner) RetirePollerAccount(ctx context.Context, owner, repo string, uid int64) error {
	if err := o.retiringTriggerOwner.RetirePollerAccount(ctx, owner, repo, uid); err != nil {
		return err
	}
	if o.blank {
		o.c.VariableValues[owner+"/"+repo+"/"+forge.VarGitLabPollerGenerations] = " "
		return nil
	}
	return writePollerGenerations(ctx, o.c, owner, repo, PollerGenerationState{CurrentUserID: cutoverNewUID})
}
