package repos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	trigOwner  = "grp"
	trigRepo   = "proj"
	trigBranch = "main"
)

var errTrigInjected = errors.New("injected failure")

type trigPush struct{ ID, DeployKeyID int }

// trigFakeGitLab is an in-memory GitLab project for the trigger-account
// lifecycle. fail makes the next call of a method fail without effect; lose
// makes the next call take effect and then fail, like a lost response.
type trigFakeGitLab struct {
	nextID      int
	exactRule   bool
	saDisabled  bool
	accounts    map[int]GitLabServiceAccount
	members     map[int]int
	pats        map[int]*ProjectAccessToken
	keys        map[int]GitLabDeployKey
	keyOwner    map[int]int
	pushes      []trigPush
	triggers    map[int64]forge.PipelineTriggerToken
	fail        map[string]bool
	lose        map[string]bool
	calls       map[string]int
	bootstrapAs int
	published   map[int64]string
	// sshKeys, activeJobs and schedules are the IDs owned by each account.
	sshKeys    map[int][]int
	activeJobs map[int][]int
	schedules  map[int][]int
	// ownerlessSchedules are schedules GitLab reported without an owner.
	ownerlessSchedules []int
	// noPushKeys are deploy key IDs GitLab reports with can_push false.
	noPushKeys map[int]bool
	// state is the project's schedule store, shared with the installer
	// client, so schedules the account creates are visible to both.
	state *forge.FakeClient
	// createIgnoresActive makes the account's schedule create ignore the
	// requested active state; see trigEnv.ignoreActiveOnCreate.
	createIgnoresActive bool
}

func (f *trigFakeGitLab) GetAuthenticatedUserID(context.Context) (int, error) {
	if _, err := f.enter("GetAuthenticatedUserID"); err != nil {
		return 0, err
	}
	return 42, nil
}

func newTrigFakeGitLab() *trigFakeGitLab {
	return &trigFakeGitLab{
		nextID:     100,
		exactRule:  true,
		accounts:   map[int]GitLabServiceAccount{},
		members:    map[int]int{},
		pats:       map[int]*ProjectAccessToken{},
		keys:       map[int]GitLabDeployKey{},
		keyOwner:   map[int]int{},
		triggers:   map[int64]forge.PipelineTriggerToken{},
		fail:       map[string]bool{},
		lose:       map[string]bool{},
		calls:      map[string]int{},
		published:  map[int64]string{},
		sshKeys:    map[int][]int{},
		activeJobs: map[int][]int{},
		schedules:  map[int][]int{},
	}
}

func (f *trigFakeGitLab) id() int { f.nextID++; return f.nextID }

// enter returns (proceed, err): proceed is false when the call must fail
// without effect; err is set when the call must fail after taking effect.
func (f *trigFakeGitLab) enter(method string) (bool, error) {
	f.calls[method]++
	if f.fail[method] {
		delete(f.fail, method)
		return false, errTrigInjected
	}
	if f.lose[method] {
		delete(f.lose, method)
		return true, errTrigInjected
	}
	return true, nil
}

func (f *trigFakeGitLab) ListProjectServiceAccounts(context.Context, string, string) ([]GitLabServiceAccount, error) {
	if _, err := f.enter("ListProjectServiceAccounts"); err != nil {
		return nil, err
	}
	if f.saDisabled {
		return nil, forge.ErrNotFound
	}
	var out []GitLabServiceAccount
	for _, a := range f.accounts {
		out = append(out, a)
	}
	return out, nil
}

func (f *trigFakeGitLab) CreateProjectServiceAccount(_ context.Context, _, _, name string) (GitLabServiceAccount, error) {
	ok, err := f.enter("CreateProjectServiceAccount")
	if !ok {
		return GitLabServiceAccount{}, err
	}
	a := GitLabServiceAccount{ID: f.id(), Name: name}
	f.accounts[a.ID] = a
	return a, err
}

func (f *trigFakeGitLab) DeleteProjectServiceAccount(_ context.Context, _, _ string, userID int) error {
	ok, err := f.enter("DeleteProjectServiceAccount")
	if !ok {
		return err
	}
	delete(f.accounts, userID)
	delete(f.members, userID)
	for id, p := range f.pats {
		if p.UserID == userID {
			delete(f.pats, id)
		}
	}
	return err
}

func (f *trigFakeGitLab) GetProjectMemberAccessLevel(_ context.Context, _, _ string, userID int64) (int, error) {
	if _, err := f.enter("GetProjectMemberAccessLevel"); err != nil {
		return 0, err
	}
	level, ok := f.members[int(userID)]
	if !ok {
		return 0, fmt.Errorf("member: %w", forge.ErrNotFound)
	}
	return level, nil
}

func (f *trigFakeGitLab) AddProjectMember(_ context.Context, _, _ string, userID int64, level int) error {
	ok, err := f.enter("AddProjectMember")
	if ok {
		f.members[int(userID)] = level
	}
	return err
}

func (f *trigFakeGitLab) UpdateProjectMemberAccessLevel(_ context.Context, _, _ string, userID int64, level int) error {
	ok, err := f.enter("UpdateProjectMemberAccessLevel")
	if ok {
		f.members[int(userID)] = level
	}
	return err
}

func (f *trigFakeGitLab) CreateServiceAccountPAT(_ context.Context, _, _ string, userID int, name string, _ []string, _ string) (*ProjectAccessToken, error) {
	ok, err := f.enter("CreateServiceAccountPAT")
	if !ok {
		return nil, err
	}
	p := &ProjectAccessToken{ID: f.id(), Name: name, Active: true, UserID: userID}
	f.pats[p.ID] = p
	out := *p
	out.Token = fmt.Sprintf("pat-%d", p.ID)
	return &out, err
}

func (f *trigFakeGitLab) ListServiceAccountPATs(_ context.Context, _, _ string, userID int) ([]ProjectAccessToken, error) {
	if _, err := f.enter("ListServiceAccountPATs"); err != nil {
		return nil, err
	}
	if _, exists := f.accounts[userID]; !exists {
		return nil, fmt.Errorf("user: %w", forge.ErrNotFound)
	}
	var out []ProjectAccessToken
	for _, p := range f.pats {
		if p.UserID == userID {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (f *trigFakeGitLab) RevokeServiceAccountPAT(_ context.Context, _, _ string, _, tokenID int) error {
	ok, err := f.enter("RevokeServiceAccountPAT")
	if ok {
		if p, exists := f.pats[tokenID]; exists {
			p.Active, p.Revoked = false, true
		}
	}
	return err
}

func (f *trigFakeGitLab) HasExactProtectedBranchRule(context.Context, string, string, string) (bool, error) {
	if _, err := f.enter("HasExactProtectedBranchRule"); err != nil {
		return false, err
	}
	return f.exactRule, nil
}

func (f *trigFakeGitLab) GrantProtectedBranchPushDeployKey(_ context.Context, _, _, _ string, keyID int) error {
	ok, err := f.enter("GrantProtectedBranchPushDeployKey")
	if !ok {
		return err
	}
	if _, exists := f.keys[keyID]; !exists {
		return errors.New("deploy key is not enabled for this project")
	}
	for _, p := range f.pushes {
		if p.DeployKeyID == keyID {
			return err
		}
	}
	f.pushes = append(f.pushes, trigPush{ID: f.id(), DeployKeyID: keyID})
	return err
}

func (f *trigFakeGitLab) ProtectedBranchPushDeployKeyEntryID(_ context.Context, _, _, _ string, keyID int) (int, error) {
	if _, err := f.enter("ProtectedBranchPushDeployKeyEntryID"); err != nil {
		return 0, err
	}
	for _, p := range f.pushes {
		if p.DeployKeyID == keyID {
			return p.ID, nil
		}
	}
	return 0, nil
}

func (f *trigFakeGitLab) RevokeProtectedBranchPushDeployKeyEntry(_ context.Context, _, _, _ string, entryID, keyID int) error {
	ok, err := f.enter("RevokeProtectedBranchPushDeployKeyEntry")
	if !ok {
		return err
	}
	for i, p := range f.pushes {
		if p.ID == entryID {
			if p.DeployKeyID != keyID {
				return errors.New("entry is not a grant for the deploy key")
			}
			f.pushes = append(f.pushes[:i], f.pushes[i+1:]...)
			break
		}
	}
	return err
}

func (f *trigFakeGitLab) ListProjectDeployKeys(context.Context, string, string) ([]GitLabDeployKey, error) {
	if _, err := f.enter("ListProjectDeployKeys"); err != nil {
		return nil, err
	}
	var out []GitLabDeployKey
	for _, k := range f.keys {
		if f.noPushKeys[k.ID] {
			k.CanPush = false
		}
		out = append(out, k)
	}
	return out, nil
}

func (f *trigFakeGitLab) DeleteProjectDeployKey(_ context.Context, _, _ string, keyID int) error {
	ok, err := f.enter("DeleteProjectDeployKey")
	if !ok {
		return err
	}
	delete(f.keys, keyID)
	var kept []trigPush
	for _, p := range f.pushes {
		if p.DeployKeyID != keyID {
			kept = append(kept, p)
		}
	}
	f.pushes = kept
	return err
}

func (f *trigFakeGitLab) ListPipelineTriggerTokens(context.Context, string, string) ([]forge.PipelineTriggerToken, error) {
	if _, err := f.enter("ListPipelineTriggerTokens"); err != nil {
		return nil, err
	}
	var out []forge.PipelineTriggerToken
	for _, t := range f.triggers {
		out = append(out, t)
	}
	return out, nil
}

func (f *trigFakeGitLab) RevokePipelineTriggerToken(_ context.Context, _, _ string, tokenID int64) error {
	ok, err := f.enter("RevokePipelineTriggerToken")
	if ok {
		delete(f.triggers, tokenID)
	}
	return err
}

func (f *trigFakeGitLab) AccountSSHKeyIDs(_ context.Context, userID int) ([]int, error) {
	if _, err := f.enter("AccountSSHKeyIDs"); err != nil {
		return nil, err
	}
	return f.sshKeys[userID], nil
}

func (f *trigFakeGitLab) AccountActiveJobIDs(_ context.Context, _, _ string, userID int) ([]int, error) {
	if _, err := f.enter("AccountActiveJobIDs"); err != nil {
		return nil, err
	}
	return f.activeJobs[userID], nil
}

func (f *trigFakeGitLab) AccountScheduleIDs(_ context.Context, _, _ string, userID int) ([]int, error) {
	if _, err := f.enter("AccountScheduleIDs"); err != nil {
		return nil, err
	}
	var out []int
	for _, id := range f.schedules[userID] {
		// A schedule the installer deleted is no longer reported.
		if f.state == nil || !slices.Contains(f.state.DeletedScheduleIDs, int64(id)) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *trigFakeGitLab) UnknownOwnerScheduleIDs(_ context.Context, _, _ string) ([]int, error) {
	if _, err := f.enter("UnknownOwnerScheduleIDs"); err != nil {
		return nil, err
	}
	return f.ownerlessSchedules, nil
}

// trigBootstrapClient acts as the account that owns the bootstrap PAT.
type trigBootstrapClient struct {
	f      *trigFakeGitLab
	userID int
}

func (b trigBootstrapClient) CreateProjectDeployKey(_ context.Context, _, _, title, publicKey string, canPush bool) (GitLabDeployKey, error) {
	ok, err := b.f.enter("CreateProjectDeployKey")
	if !ok {
		return GitLabDeployKey{}, err
	}
	if b.f.members[b.userID] < forge.GitLabAccessLevelMaintainer {
		return GitLabDeployKey{}, forge.ErrForbidden
	}
	if !canPush || !strings.HasPrefix(publicKey, "ssh-ed25519 ") {
		return GitLabDeployKey{}, errors.New("bad key request")
	}
	k := GitLabDeployKey{ID: b.f.id(), Title: title, FingerprintSHA256: trigFingerprint(publicKey), CanPush: canPush}
	b.f.keys[k.ID] = k
	b.f.keyOwner[k.ID] = b.userID
	return k, err
}

func (b trigBootstrapClient) CreatePipelineTriggerToken(_ context.Context, _, _, description string) (*forge.PipelineTriggerToken, error) {
	ok, err := b.f.enter("CreatePipelineTriggerToken")
	if !ok {
		return nil, err
	}
	if b.f.members[b.userID] < forge.GitLabAccessLevelMaintainer {
		return nil, forge.ErrForbidden
	}
	t := forge.PipelineTriggerToken{ID: int64(b.f.id()), Description: description, OwnerID: int64(b.userID)}
	b.f.triggers[t.ID] = t
	out := t
	out.Token = fmt.Sprintf("glptt-%d", t.ID)
	return &out, err
}

// trigFingerprint recomputes the fingerprint like GitLab's fingerprint_sha256:
// the bare digest, without the "SHA256:" prefix that the ledger records.
func trigFingerprint(authorized string) string {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(ssh.FingerprintSHA256(pub), "SHA256:")
}

func TestSameDeployKeyFingerprint(t *testing.T) {
	assert.True(t, sameDeployKeyFingerprint("SHA256:abc", "abc"), "bare GitLab digest matches the ledger form")
	assert.True(t, sameDeployKeyFingerprint("SHA256:abc", "SHA256:abc"))
	assert.True(t, sameDeployKeyFingerprint("abc", "abc"))
	assert.False(t, sameDeployKeyFingerprint("SHA256:abc", "abd"))
	assert.False(t, sameDeployKeyFingerprint("", ""), "an empty fingerprint never proves ownership")
	assert.False(t, sameDeployKeyFingerprint("SHA256:abc", ""))
}

type trigState struct {
	*forge.FakeClient
	failWrite int
	writes    int
	// failWhen fails (once) the first write whose ledger satisfies it.
	failWhen func(gitLabTriggerAccountState) bool
	// failAlwaysWhen fails every matching ledger write until cleared.
	failAlwaysWhen func(gitLabTriggerAccountState) bool
	// failDeleteAfter, when positive, lets that many schedule deletions
	// succeed and then fails every later one.
	failDeleteAfter int
	deletes         int
	// createIgnoresActive makes the installer's schedule create ignore the
	// requested active state, as a GitLab that does not honor the attribute
	// would.
	createIgnoresActive bool
	// createWithoutID models a successful response that omits the schedule ID.
	createWithoutID bool
	// createOwnerOverride models a server that reports an unexpected owner
	// for an installer-created schedule.
	createOwnerOverride int64
	// detailOwnerOverride models ownership changing between list and detail
	// reads, including the read immediately before a planned delete.
	detailOwnerOverride       map[int64]int64
	detailDescriptionOverride map[int64]string
	// afterDelete simulates a schedule firing just before hand-back deleted it.
	afterDelete func()
}

func (s *trigState) CreatePipelineScheduleWithActive(ctx context.Context, owner, repo, ref, description, cron string, variables map[string]string, active bool) (int64, error) {
	if s.createWithoutID {
		return 0, nil
	}
	id, err := s.FakeClient.CreatePipelineScheduleWithActive(ctx, owner, repo, ref, description, cron, variables, active || s.createIgnoresActive)
	if err == nil {
		for i := range s.PipelineSchedules[owner+"/"+repo] {
			if s.PipelineSchedules[owner+"/"+repo][i].ID == id {
				if s.createOwnerOverride != 0 {
					s.PipelineSchedules[owner+"/"+repo][i].OwnerID = s.createOwnerOverride
				} else {
					s.PipelineSchedules[owner+"/"+repo][i].OwnerID = 42
				}
			}
		}
	}
	return id, err
}

func (s *trigState) GetPipelineSchedule(ctx context.Context, owner, repo string, id int64) (*forge.PipelineSchedule, error) {
	detail, err := s.FakeClient.GetPipelineSchedule(ctx, owner, repo, id)
	if err == nil && detail != nil {
		if ownerID, ok := s.detailOwnerOverride[id]; ok {
			detail.OwnerID = ownerID
		}
		if description, ok := s.detailDescriptionOverride[id]; ok {
			detail.Description = description
		}
	}
	return detail, err
}

// ignoreActiveOnCreate makes every schedule create, the installer's and the
// trigger account's, ignore the requested active state, as a GitLab that does
// not honor the attribute would.
func (e *trigEnv) ignoreActiveOnCreate(ignore bool) {
	e.state.createIgnoresActive = ignore
	e.f.createIgnoresActive = ignore
}

func (s *trigState) DeletePipelineSchedule(ctx context.Context, owner, repo string, id int64) error {
	if s.failDeleteAfter > 0 {
		s.deletes++
		if s.deletes > s.failDeleteAfter {
			return errTrigInjected
		}
	}
	if err := s.FakeClient.DeletePipelineSchedule(ctx, owner, repo, id); err != nil {
		return err
	}
	if s.afterDelete != nil {
		s.afterDelete()
	}
	return nil
}

func (s *trigState) UpdateCIVariable(ctx context.Context, owner, repo, name, value string, protected bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.writes++
	if s.failWrite != 0 && s.writes == s.failWrite {
		return errTrigInjected
	}
	if s.failWhen != nil {
		if st, err := decodeGitLabTriggerAccountState(value); err == nil && s.failWhen(st) {
			s.failWhen = nil
			return errTrigInjected
		}
	}
	if s.failAlwaysWhen != nil {
		if st, err := decodeGitLabTriggerAccountState(value); err == nil && s.failAlwaysWhen(st) {
			return errTrigInjected
		}
	}
	return s.FakeClient.UpdateCIVariable(ctx, owner, repo, name, value, protected)
}

// setSink records whether the trigger-token variable exists.
func (e *trigEnv) setSink(present bool) {
	if e.state.Secrets == nil {
		e.state.Secrets = map[string]bool{}
	}
	e.state.Secrets[trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken] = present
}

func (e *trigEnv) sinkPresent() bool {
	return e.state.Secrets[trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken]
}

// trigTestLease returns a held lease capability for owner/repo.
func trigTestLease(owner, repo string) *GitLabProjectLease {
	l := &GitLabProjectLease{owner: owner, repo: repo}
	l.held.Store(true)
	return l
}

type trigEnv struct {
	f       *trigFakeGitLab
	state   *trigState
	pollers []int
	publish func(ctx context.Context, id int64, token string) error
}

func newTrigEnv() *trigEnv {
	e := &trigEnv{f: newTrigFakeGitLab(), state: &trigState{FakeClient: forge.NewFakeClient()}}
	e.f.state = e.state.FakeClient
	e.state.Repos = append(e.state.Repos, forge.Repository{FullName: trigOwner + "/" + trigRepo, DefaultBranch: trigBranch})
	e.state.PipelineVarOverrideRoles[trigOwner+"/"+trigRepo] = forge.PipelineVarOverrideNoOneAllowed
	e.publish = func(_ context.Context, id int64, token string) error {
		ok, err := e.f.enter("Publish")
		if ok {
			e.f.published[id] = token
			e.setSink(true)
			e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken] = token
		}
		return err
	}
	return e
}

func (e *trigEnv) opts() GitLabTriggerAccountOptions {
	return GitLabTriggerAccountOptions{
		Owner: trigOwner, Repo: trigRepo, Branch: trigBranch,
		PollerUserIDs:           e.pollers,
		PollerInventoryVerified: true,
		Lease:                   trigTestLease(trigOwner, trigRepo),
		API:                     e.f,
		Bootstrap: func(token string) (GitLabTriggerBootstrapAPI, error) {
			e.f.calls["Bootstrap"]++
			var id int
			if _, err := fmt.Sscanf(token, "pat-%d", &id); err != nil {
				return nil, err
			}
			p, ok := e.f.pats[id]
			if !ok || !p.Active {
				return nil, errors.New("401 unauthorized")
			}
			return trigBootstrapClient{f: e.f, userID: p.UserID}, nil
		},
		PublishTriggerToken: e.publish,
		UnpublishTriggerToken: func(context.Context) error {
			if _, err := e.f.enter("Unpublish"); err != nil {
				return err
			}
			e.setSink(false)
			delete(e.state.VariableValues, trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken)
			return nil
		},
		State: e.state,
		Now:   func() time.Time { return time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC) },
		Rand:  rand.New(rand.NewSource(int64(e.f.nextID))), //nolint:gosec // deterministic test entropy
	}
}

func (e *trigEnv) ledger(t *testing.T) (gitLabTriggerAccountState, bool) {
	t.Helper()
	raw, ok := e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabTriggerAccount]
	if !ok {
		return gitLabTriggerAccountState{}, false
	}
	st, err := decodeGitLabTriggerAccountState(raw)
	require.NoError(t, err)
	return st, true
}

func (e *trigEnv) setLedger(t *testing.T, st gitLabTriggerAccountState) {
	t.Helper()
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabTriggerAccount] = string(raw)
}

func (e *trigEnv) managedAccounts() []GitLabServiceAccount {
	var out []GitLabServiceAccount
	for _, a := range e.f.accounts {
		if strings.HasPrefix(a.Name, gitLabTriggerAccountNamePrefix) {
			out = append(out, a)
		}
	}
	return out
}

func (e *trigEnv) activePATs() int {
	n := 0
	for _, p := range e.f.pats {
		if p.Active && strings.HasPrefix(p.Name, gitLabTriggerBootstrapPATPrefix) {
			n++
		}
	}
	return n
}

// assertConverged checks the single-set invariant after a successful install.
func (e *trigEnv) assertConverged(t *testing.T, res GitLabTriggerAccountResult) {
	t.Helper()
	assert.True(t, res.Ready)
	accts := e.managedAccounts()
	require.Len(t, accts, 1, "exactly one managed trigger account")
	acct := accts[0]
	assert.Equal(t, acct.ID, res.AccountID)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[acct.ID], "account ends at Developer")
	assert.Zero(t, e.activePATs(), "bootstrap PAT revoked")
	managedKeys := 0
	for id, k := range e.f.keys {
		if k.Title == acct.Name {
			managedKeys++
			assert.Equal(t, id, res.DeployKeyID)
			assert.Equal(t, acct.ID, e.f.keyOwner[id], "key owned by the trigger account")
		}
	}
	assert.Equal(t, 1, managedKeys, "exactly one managed deploy key")
	grants := 0
	for _, p := range e.f.pushes {
		if p.DeployKeyID == res.DeployKeyID {
			grants++
			assert.Equal(t, p.ID, res.GrantEntryID)
		}
	}
	assert.Equal(t, 1, grants, "exactly one branch grant")
	managedTriggers := 0
	for id, tr := range e.f.triggers {
		if tr.Description == acct.Name {
			managedTriggers++
			assert.Equal(t, id, res.TriggerTokenID)
			assert.Equal(t, int64(acct.ID), tr.OwnerID)
		}
	}
	assert.Equal(t, 1, managedTriggers, "exactly one managed trigger token")
	assert.NotEmpty(t, e.f.published[res.TriggerTokenID], "live trigger value published")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.Equal(t, gitLabTriggerPhaseReady, st.Phase)
	assert.False(t, st.Elevated)
	assert.Zero(t, st.PATID)
	raw := e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.VarGitLabTriggerAccount]
	assert.NotContains(t, raw, "glptt-", "ledger never stores the trigger value")
	assert.NotContains(t, raw, "pat-", "ledger never stores the bootstrap PAT")
	assert.NotContains(t, raw, "PRIVATE", "ledger never stores a private key")
}

// assertRemoved checks that no managed resource remains.
func (e *trigEnv) assertRemoved(t *testing.T) {
	t.Helper()
	assert.Empty(t, e.managedAccounts())
	for _, k := range e.f.keys {
		assert.False(t, strings.HasPrefix(k.Title, gitLabTriggerAccountNamePrefix), "managed key %d remains", k.ID)
	}
	for _, tr := range e.f.triggers {
		assert.False(t, strings.HasPrefix(tr.Description, gitLabTriggerAccountNamePrefix), "managed trigger %d remains", tr.ID)
	}
	for _, p := range e.f.pushes {
		_, keyExists := e.f.keys[p.DeployKeyID]
		assert.True(t, p.DeployKeyID == 0 || keyExists, "orphan grant %d remains", p.ID)
	}
	_, ok := e.ledger(t)
	assert.False(t, ok, "ledger deleted")
}

// seedAdminResources adds resources fullsend must never touch.
func (e *trigEnv) seedAdminResources() {
	e.f.accounts[1] = GitLabServiceAccount{ID: 1, Name: "admin-bot"}
	e.f.members[1] = forge.GitLabAccessLevelMaintainer
	e.f.pats[2] = &ProjectAccessToken{ID: 2, Name: "admin-pat", Active: true, UserID: 1}
	e.f.keys[3] = GitLabDeployKey{ID: 3, Title: "admin-deploy", FingerprintSHA256: "SHA256:admin"}
	e.f.pushes = append(e.f.pushes, trigPush{ID: 4, DeployKeyID: 3}, trigPush{ID: 5})
	e.f.triggers[6] = forge.PipelineTriggerToken{ID: 6, Description: "admin-trigger", OwnerID: 1}
}

func (e *trigEnv) assertAdminResources(t *testing.T) {
	t.Helper()
	assert.Contains(t, e.f.accounts, 1)
	assert.Equal(t, forge.GitLabAccessLevelMaintainer, e.f.members[1])
	assert.True(t, e.f.pats[2].Active)
	assert.Contains(t, e.f.keys, 3)
	assert.Contains(t, e.f.pushes, trigPush{ID: 4, DeployKeyID: 3})
	assert.Contains(t, e.f.pushes, trigPush{ID: 5})
	assert.Contains(t, e.f.triggers, int64(6))
}

func TestConvergeGitLabTriggerAccount_FreshInstall(t *testing.T) {
	e := newTrigEnv()
	e.seedAdminResources()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res)
	e.assertAdminResources(t)
	assert.Equal(t, 1, e.f.calls["Bootstrap"])

	// Reinstall is a no-op on GitLab resources.
	before := map[string]int{}
	for k, v := range e.f.calls {
		before[k] = v
	}
	res2, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Equal(t, res, res2)
	for _, m := range []string{"CreateProjectServiceAccount", "CreateServiceAccountPAT", "CreateProjectDeployKey", "CreatePipelineTriggerToken", "Publish"} {
		assert.Equal(t, before[m], e.f.calls[m], "reinstall must not call %s", m)
	}
	e.assertConverged(t, res2)
}

func TestRotateGitLabTriggerAccount_DefersRetirementUntilWebhookCutover(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	pending, err := GitLabTriggerAccountRetirementPending(ctx, e.state, trigOwner, trigRepo)
	require.NoError(t, err)
	assert.False(t, pending)
	current, err := RotateGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	require.NotEqual(t, old.TriggerTokenID, current.TriggerTokenID)
	assert.Contains(t, e.f.triggers, old.TriggerTokenID, "the old webhook bearer stays usable until cutover")
	st, ok := e.ledger(t)
	require.True(t, ok)
	require.Len(t, st.Retired, 1)
	pending, err = GitLabTriggerAccountRetirementPending(ctx, e.state, trigOwner, trigRepo)
	require.NoError(t, err)
	assert.True(t, pending)

	require.NoError(t, RetireGitLabTriggerAccount(ctx, e.opts()))
	assert.NotContains(t, e.f.triggers, old.TriggerTokenID)
	st, ok = e.ledger(t)
	require.True(t, ok)
	assert.Empty(t, st.Retired)
	pending, err = GitLabTriggerAccountRetirementPending(ctx, e.state, trigOwner, trigRepo)
	require.NoError(t, err)
	assert.False(t, pending)
}

func TestRetireGitLabTriggerAccount_WaitsForReadyReplacement(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	e.f.fail["Unpublish"] = true
	_, err = RotateGitLabTriggerAccount(ctx, o)
	require.Error(t, err)
	st, _ := e.ledger(t)
	require.Len(t, st.Retired, 1)
	require.False(t, st.TriggerPublished)
	pending, err := GitLabTriggerAccountRetirementPending(ctx, e.state, trigOwner, trigRepo)
	require.NoError(t, err)
	assert.False(t, pending, "the webhook cannot cut over to an unfinished replacement")
	require.NoError(t, RetireGitLabTriggerAccount(ctx, o))
	assert.Contains(t, e.f.triggers, old.TriggerTokenID)
	st, _ = e.ledger(t)
	assert.Len(t, st.Retired, 1)
}

func TestConvergeGitLabTriggerAccount_LegacyDigestlessGenerationSurvivesCutover(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	st, _ := e.ledger(t)
	st.TriggerTokenDigest = "" // installed by a CLI before value binding
	e.setLedger(t, st)

	require.NoError(t, ContainGitLabTriggerAccount(ctx, o))
	assert.Contains(t, e.f.triggers, old.TriggerTokenID, "early containment must not break the existing webhook")
	assert.True(t, e.sinkPresent())
	current, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	assert.NotEqual(t, old.TriggerTokenID, current.TriggerTokenID)
	assert.Contains(t, e.f.triggers, old.TriggerTokenID, "legacy bearer stays available until webhook cutover")
	st, _ = e.ledger(t)
	assert.NotEmpty(t, st.TriggerTokenDigest)
	require.Len(t, st.Retired, 1)

	require.NoError(t, RetireGitLabTriggerAccount(ctx, e.opts()))
	assert.NotContains(t, e.f.triggers, old.TriggerTokenID)
}

func TestContainGitLabTriggerAccount_CurrentDigestTakesPrecedenceOverLegacyRetirement(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	st, _ := e.ledger(t)
	st.TriggerTokenDigest = "" // legacy generation without value binding
	e.setLedger(t, st)
	current, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	st, _ = e.ledger(t)
	require.Len(t, st.Retired, 1)
	require.NotEmpty(t, st.TriggerTokenDigest)

	key := trigOwner + "/" + trigRepo + "/" + forge.SecretGitLabTriggerAccountToken
	e.state.VariableValues[key] = "copied-foreign-token"
	require.NoError(t, ContainGitLabTriggerAccount(ctx, o))
	assert.False(t, e.sinkPresent(), "a legacy retirement cannot authorize the current sink")
	assert.NotContains(t, e.f.triggers, current.TriggerTokenID)
	assert.NotContains(t, e.f.triggers, old.TriggerTokenID)
}

func TestContainGitLabTriggerAccount_LostMarkerSaveKeepsUnsafeSinkForRetry(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	e.f.fail["Unpublish"] = true
	_, err = RotateGitLabTriggerAccount(ctx, o)
	require.Error(t, err)
	st, _ := e.ledger(t)
	require.True(t, st.PublicationPending)
	delete(e.f.triggers, st.TriggerTokenID) // only the retired revocation remains
	key := trigOwner + "/" + trigRepo + "/" + forge.SecretGitLabTriggerAccountToken
	e.state.SecretProtections[key] = forge.SecretProtection{Exists: true, Protected: true} // unmasked
	e.state.failAlwaysWhen = func(next gitLabTriggerAccountState) bool {
		return !next.PublicationPending && next.PublishStarted && len(next.Retired) > 0
	}
	e.f.fail["RevokePipelineTriggerToken"] = true
	require.Error(t, ContainGitLabTriggerAccount(ctx, o))
	assert.True(t, e.sinkPresent(), "an unrecorded containment must leave the sink visible for retry")
	assert.Contains(t, e.f.triggers, old.TriggerTokenID)
	st, _ = e.ledger(t)
	assert.True(t, st.PublicationPending, "the failed marker write did not reach the ledger")

	e.state.failAlwaysWhen = nil
	require.NoError(t, ContainGitLabTriggerAccount(ctx, o))
	assert.False(t, e.sinkPresent())
	assert.NotContains(t, e.f.triggers, old.TriggerTokenID)
}

func TestConvergeGitLabTriggerAccount_InterruptedPublishKeepsOldWebhookBearer(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	oldValue := e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken]
	st, _ := e.ledger(t)
	st.TriggerTokenDigest = "" // generation installed before bearer binding
	e.setLedger(t, st)
	e.f.fail["Unpublish"] = true // crash after recording the new digest
	_, err = ConvergeGitLabTriggerAccount(ctx, o)
	require.Error(t, err)
	st, _ = e.ledger(t)
	require.True(t, st.PublishStarted)
	require.False(t, st.TriggerPublished)
	require.Len(t, st.Retired, 1)
	assert.Equal(t, triggerTokenDigest(oldValue), st.Retired[0].TriggerTokenDigest)

	require.NoError(t, ContainGitLabTriggerAccount(ctx, o))
	assert.True(t, e.sinkPresent())
	assert.Contains(t, e.f.triggers, old.TriggerTokenID)
	assert.Equal(t, oldValue, e.state.VariableValues[trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken])

	_, err = ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	assert.Contains(t, e.f.triggers, old.TriggerTokenID, "the old hook remains usable until cutover")
}

func TestConvergeGitLabTriggerAccount_InterruptedPublishAfterClearKeepsOldTrigger(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	e.f.fail["Unpublish"] = true // stop after the new publication marker
	_, err = RotateGitLabTriggerAccount(ctx, o)
	require.Error(t, err)
	st, _ := e.ledger(t)
	require.True(t, st.PublicationPending)
	require.Len(t, st.Retired, 1)

	// Simulate a crash after the clear call but before publishing the new value.
	e.setSink(false)
	delete(e.state.VariableValues, trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken)
	require.NoError(t, ContainGitLabTriggerAccount(ctx, o))
	assert.Contains(t, e.f.triggers, old.TriggerTokenID)
	_, err = ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	assert.Contains(t, e.f.triggers, old.TriggerTokenID, "the old hook remains usable until cutover")
}

// A second interrupted retry retires the pending generation and clears its
// PublicationPending marker; the cleared sink must still not revoke the older
// generation the webhook uses before it is repointed.
func TestContainGitLabTriggerAccount_ClearedSinkSurvivesSecondInterruptedRetry(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	old, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	e.f.fail["Unpublish"] = true // stop after the new publication marker
	_, err = RotateGitLabTriggerAccount(ctx, o)
	require.Error(t, err)
	st, _ := e.ledger(t)
	require.True(t, st.PublicationPending)
	require.Len(t, st.Retired, 1)

	// The next retry retired the pending generation (startFreshGeneration) and
	// then failed before publishing: the current generation is empty and the
	// sink was cleared.
	pending := st
	pending.Retired = nil
	pending.PublicationPending = false
	st.Nonce = "0123456789abcdef"
	st.AccountID, st.DeployKeyID, st.DeployKeyFingerprint, st.GrantEntryID = 0, 0, "", 0
	st.TriggerTokenID, st.TriggerTokenDigest = 0, ""
	st.TriggerPublished, st.PublishStarted, st.PublicationPending = false, false, false
	st.Retired = append(st.Retired, pending)
	e.setLedger(t, st)
	e.setSink(false)
	delete(e.state.VariableValues, trigOwner+"/"+trigRepo+"/"+forge.SecretGitLabTriggerAccountToken)

	require.NoError(t, ContainGitLabTriggerAccount(ctx, o))
	assert.Contains(t, e.f.triggers, old.TriggerTokenID, "the old hook remains usable until cutover")
}

func TestConvergeGitLabTriggerAccount_InterruptedLedgerSaveAfterPublish(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	current, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	st, _ := e.ledger(t)
	st.PublicationPending = true
	st.TriggerPublished = false // publish succeeded; the final save did not
	e.setLedger(t, st)

	resumed, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	assert.Equal(t, current.TriggerTokenID, resumed.TriggerTokenID)
	st, _ = e.ledger(t)
	assert.True(t, st.TriggerPublished)
	assert.False(t, st.PublicationPending)
}

func TestRemoveGitLabTriggerAccount_InterruptedPublicationRetriesAfterTeardownFailure(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	o := e.opts()
	o.DeferRetirement = true
	_, err := ConvergeGitLabTriggerAccount(ctx, o)
	require.NoError(t, err)
	e.f.fail["Unpublish"] = true
	_, err = RotateGitLabTriggerAccount(ctx, o)
	require.Error(t, err)
	st, _ := e.ledger(t)
	require.True(t, st.PublicationPending)

	e.f.fail["RevokePipelineTriggerToken"] = true
	require.Error(t, RemoveGitLabTriggerAccount(ctx, o))
	st, _ = e.ledger(t)
	assert.False(t, st.PublicationPending)
	assert.False(t, st.PublishStarted)
	_, err = GitLabTriggerAccountRetirementPending(ctx, e.state, trigOwner, trigRepo)
	require.NoError(t, err, "the partly removed ledger must remain readable")
	require.NoError(t, RemoveGitLabTriggerAccount(ctx, o))
	e.assertRemoved(t)
}

func TestConvergeGitLabTriggerAccount_RepairsDrift(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)

	// An administrator elevates the account and deletes the trigger token.
	e.f.members[res.AccountID] = forge.GitLabAccessLevelMaintainer
	delete(e.f.triggers, res.TriggerTokenID)
	res2, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res2)
	assert.NotEqual(t, res.TriggerTokenID, res2.TriggerTokenID)
	// The lost trigger value was distributed, so the identity is replaced by
	// a fresh generation and the previous one is retired.
	assert.NotEqual(t, res.AccountID, res2.AccountID)
	assert.NotEqual(t, res.DeployKeyID, res2.DeployKeyID)

	// The deploy key is deleted out of band; its grant goes with it.
	require.NoError(t, e.f.DeleteProjectDeployKey(context.Background(), trigOwner, trigRepo, res2.DeployKeyID))
	res3, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res3)
	assert.NotEqual(t, res2.DeployKeyID, res3.DeployKeyID)
	// Repairing the key needs a Maintainer window, so the distributed identity
	// is replaced by a fresh generation instead of being elevated again.
	assert.NotEqual(t, res2.AccountID, res3.AccountID)
	assert.NotEqual(t, res2.TriggerTokenID, res3.TriggerTokenID)
	assert.NotContains(t, e.f.triggers, res2.TriggerTokenID)

	// A grant revoked out of band is restored.
	e.f.pushes = nil
	res4, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, res4)
}

// trigSteps lists every GitLab call a fresh install makes that can be
// interrupted, both before it takes effect (fail) and after (lose).
var trigSteps = []string{
	"HasExactProtectedBranchRule",
	"ListProjectServiceAccounts",
	"CreateProjectServiceAccount",
	"ListServiceAccountPATs",
	"GetProjectMemberAccessLevel",
	"AddProjectMember",
	"UpdateProjectMemberAccessLevel",
	"CreateServiceAccountPAT",
	"ListProjectDeployKeys",
	"CreateProjectDeployKey",
	"ListPipelineTriggerTokens",
	"CreatePipelineTriggerToken",
	"Publish",
	"RevokeServiceAccountPAT",
	"GrantProtectedBranchPushDeployKey",
	"ProtectedBranchPushDeployKeyEntryID",
}

func TestConvergeGitLabTriggerAccount_InterruptedInstallConverges(t *testing.T) {
	for _, mode := range []string{"fail", "lose"} {
		for _, step := range trigSteps {
			t.Run(mode+"/"+step, func(t *testing.T) {
				e := newTrigEnv()
				e.seedAdminResources()
				if mode == "fail" {
					e.f.fail[step] = true
				} else {
					e.f.lose[step] = true
				}
				_, firstErr := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
				if e.f.calls[step] > 0 {
					require.Error(t, firstErr)
				}
				clear(e.f.fail)
				clear(e.f.lose)
				res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
				require.NoError(t, err)
				e.assertConverged(t, res)
				e.assertAdminResources(t)

				// And uninstall after an interrupted install converges too.
				require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
				e.assertRemoved(t)
				e.assertAdminResources(t)
			})
		}
	}
}

func TestConvergeGitLabTriggerAccount_InterruptedThenUninstall(t *testing.T) {
	for _, step := range trigSteps {
		t.Run(step, func(t *testing.T) {
			e := newTrigEnv()
			e.seedAdminResources()
			e.f.lose[step] = true
			_, _ = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			clear(e.f.lose)
			require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
			e.assertRemoved(t)
			e.assertAdminResources(t)
			assert.Zero(t, e.activePATs(), "no bootstrap PAT stays active")
		})
	}
}

func TestConvergeGitLabTriggerAccount_LedgerWriteFailures(t *testing.T) {
	// Fail each ledger write in turn; a re-run must converge every time.
	probe := newTrigEnv()
	_, err := ConvergeGitLabTriggerAccount(context.Background(), probe.opts())
	require.NoError(t, err)
	total := probe.state.writes
	require.Greater(t, total, 5)
	for n := 1; n <= total; n++ {
		t.Run(fmt.Sprintf("write-%d", n), func(t *testing.T) {
			e := newTrigEnv()
			e.state.failWrite = n
			_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.Error(t, err)
			e.state.failWrite = 0
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			e.assertConverged(t, res)
		})
	}
}

func TestRemoveGitLabTriggerAccount_InterruptedUninstallConverges(t *testing.T) {
	steps := []string{
		"ListPipelineTriggerTokens", "RevokePipelineTriggerToken",
		"RevokeProtectedBranchPushDeployKeyEntry", "ListProjectDeployKeys",
		"DeleteProjectDeployKey", "ListServiceAccountPATs",
		"ListProjectServiceAccounts", "DeleteProjectServiceAccount",
	}
	for _, mode := range []string{"fail", "lose"} {
		for _, step := range steps {
			t.Run(mode+"/"+step, func(t *testing.T) {
				e := newTrigEnv()
				e.seedAdminResources()
				_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
				require.NoError(t, err)
				if mode == "fail" {
					e.f.fail[step] = true
				} else {
					e.f.lose[step] = true
				}
				require.Error(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
				st, ok := e.ledger(t)
				require.True(t, ok, "obligation kept after a failed uninstall")
				assert.Equal(t, gitLabTriggerPhaseRemoving, st.Phase)

				// Install refuses to run over an unfinished uninstall.
				_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
				require.ErrorContains(t, err, "uninstall is still in progress")

				require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
				e.assertRemoved(t)
				e.assertAdminResources(t)
			})
		}
	}
}

func TestRemoveGitLabTriggerAccount_LedgerDeleteFailureKeepsObligation(t *testing.T) {
	e := newTrigEnv()
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.state.Errors = map[string]error{"DeleteRepoVariable": errTrigInjected}
	require.Error(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	e.state.Errors = nil
	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	e.assertRemoved(t)
	// Nothing recorded: uninstall is a no-op.
	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
}

func TestConvergeGitLabTriggerAccount_WildcardOnlyBranchDefers(t *testing.T) {
	e := newTrigEnv()
	e.f.exactRule = false
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	assert.False(t, res.Ready)
	assert.Contains(t, res.Deferred, "no exact-name protected-branch rule")
	assert.Zero(t, e.f.calls["CreateProjectServiceAccount"])
	assert.Zero(t, e.f.calls["GrantProtectedBranchPushDeployKey"])
	_, ok := e.ledger(t)
	assert.False(t, ok, "a deferral records nothing")
}

func TestConvergeGitLabTriggerAccount_RejectsWildcardBranch(t *testing.T) {
	e := newTrigEnv()
	for _, b := range []string{"", "*", "release/*"} {
		o := e.opts()
		o.Branch = b
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.Error(t, err)
	}
	assert.Empty(t, e.f.calls)
}

func TestConvergeGitLabTriggerAccount_UnsupportedCapabilityDefers(t *testing.T) {
	e := newTrigEnv()
	e.f.saDisabled = true
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	assert.Contains(t, res.Deferred, "service accounts are unavailable")
	assert.Zero(t, e.f.calls["CreateProjectServiceAccount"])
}

func TestConvergeGitLabTriggerAccount_WeakOverrideRoleDefersBeforeCreatingAnything(t *testing.T) {
	key := trigOwner + "/" + trigRepo
	for name, role := range map[string]string{
		"developer": forge.PipelineVarOverrideDeveloper,
		"unset":     "",
	} {
		t.Run(name, func(t *testing.T) {
			e := newTrigEnv()
			e.state.PipelineVarOverrideRoles[key] = role
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			assert.False(t, res.Ready)
			assert.Contains(t, res.Deferred, "not no_one_allowed")
			assert.Zero(t, e.f.calls["CreateProjectServiceAccount"])
			assert.Zero(t, e.f.calls["CreateProjectDeployKey"])
			assert.Zero(t, e.f.calls["CreatePipelineTriggerToken"])
			assert.Empty(t, e.f.published, "no trigger credential is published")
			_, ok := e.ledger(t)
			assert.False(t, ok, "nothing is recorded for a deferral")
		})
	}
}

func TestConvergeGitLabTriggerAccount_OverrideRoleReadFailureFailsClosed(t *testing.T) {
	e := newTrigEnv()
	e.state.Errors = map[string]error{"GetPipelineVariablesMinimumOverrideRole": errors.New("boom")}
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "override role")
	assert.Zero(t, e.f.calls["CreateProjectServiceAccount"])
	assert.Empty(t, e.f.published)
}

func TestRotateGitLabTriggerAccount_WeakOverrideRoleDefersBeforePublishing(t *testing.T) {
	ctx := context.Background()
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	require.True(t, res.Ready)
	published := len(e.f.published)

	e.state.PipelineVarOverrideRoles[trigOwner+"/"+trigRepo] = forge.PipelineVarOverrideDeveloper
	got, err := RotateGitLabTriggerAccount(ctx, e.opts())
	require.NoError(t, err)
	assert.Contains(t, got.Deferred, "not no_one_allowed")
	assert.Len(t, e.f.published, published, "rotation publishes no replacement credential")
}

func TestConvergeGitLabTriggerAccount_PollerIdentitySeparation(t *testing.T) {
	e := newTrigEnv()
	// The first ID the fake hands out is the trigger account's.
	e.pollers = []int{e.f.nextID + 1}
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "Poller role identity")
	assert.Zero(t, e.f.calls["CreateServiceAccountPAT"], "no bootstrap credential for a Poller identity")
	assert.Zero(t, e.f.calls["CreateProjectDeployKey"])

	// Uninstall also refuses to delete a Poller identity.
	err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "Poller role identity")
	assert.Len(t, e.managedAccounts(), 1)
}

func TestConvergeGitLabTriggerAccount_ForeignPATFailsClosed(t *testing.T) {
	e := newTrigEnv()
	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: "someone", Active: true, UserID: res.AccountID}
	_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "did not create")
	assert.True(t, e.f.pats[9].Active, "foreign PAT is not revoked by inference")
}

func TestGitLabTriggerAccount_OwnershipMismatchFailsClosed(t *testing.T) {
	t.Run("renamed account", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		e.f.accounts[res.AccountID] = GitLabServiceAccount{ID: res.AccountID, Name: "admin-renamed"}
		_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "refusing")
		require.ErrorContains(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()), "refusing")
		assert.Contains(t, e.f.accounts, res.AccountID, "unproven account kept")
		_, ok := e.ledger(t)
		assert.True(t, ok, "obligation kept")
	})
	t.Run("replaced key", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		k := e.f.keys[res.DeployKeyID]
		k.FingerprintSHA256 = "SHA256:other"
		e.f.keys[res.DeployKeyID] = k
		_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "refusing")
		require.ErrorContains(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()), "refusing")
		assert.Contains(t, e.f.keys, res.DeployKeyID, "unproven key kept")
	})
	for _, op := range []string{"remove", "rotate"} {
		// Rotation moves the proven generation to the retired list before it
		// removes anything, so its obligations are recorded there.
		recorded := func(t *testing.T, st gitLabTriggerAccountState) gitLabTriggerAccountState {
			t.Helper()
			if op == "rotate" {
				require.Len(t, st.Retired, 1)
				return st.Retired[0]
			}
			return st
		}
		t.Run("replaced key keeps its grant on "+op, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			k := e.f.keys[res.DeployKeyID]
			k.FingerprintSHA256 = "SHA256:other"
			e.f.keys[res.DeployKeyID] = k
			if op == "remove" {
				err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
			} else {
				_, err = RotateGitLabTriggerAccount(context.Background(), e.opts())
			}
			require.ErrorContains(t, err, "refusing")
			assert.Contains(t, e.f.keys, res.DeployKeyID, "unproven key kept")
			assert.Contains(t, e.f.pushes, trigPush{ID: res.GrantEntryID, DeployKeyID: res.DeployKeyID}, "grant naming the unproven key kept")
			assert.Zero(t, e.f.calls["RevokeProtectedBranchPushDeployKeyEntry"], "no grant revocation attempted")
			st, ok := e.ledger(t)
			require.True(t, ok, "obligation kept")
			got := recorded(t, st)
			assert.Equal(t, res.GrantEntryID, got.GrantEntryID)
			assert.Equal(t, res.DeployKeyID, got.DeployKeyID)
		})
		t.Run("relabelled recorded trigger on "+op, func(t *testing.T) {
			e := newTrigEnv()
			res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
			require.NoError(t, err)
			tr := e.f.triggers[res.TriggerTokenID]
			tr.Description = "admin"
			e.f.triggers[res.TriggerTokenID] = tr
			if op == "remove" {
				err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
			} else {
				_, err = RotateGitLabTriggerAccount(context.Background(), e.opts())
			}
			require.ErrorContains(t, err, "refusing")
			assert.Contains(t, e.f.triggers, res.TriggerTokenID, "relabelled token not revoked")
			assert.Zero(t, e.f.calls["RevokePipelineTriggerToken"])
			st, ok := e.ledger(t)
			require.True(t, ok, "obligation kept")
			assert.Equal(t, res.TriggerTokenID, recorded(t, st).TriggerTokenID, "ledger keeps the recorded token ID")
		})
		for _, owner := range []int64{1, 0} {
			t.Run(fmt.Sprintf("recorded trigger owned by %d on %s", owner, op), func(t *testing.T) {
				e := newTrigEnv()
				res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
				require.NoError(t, err)
				tr := e.f.triggers[res.TriggerTokenID]
				tr.OwnerID = owner
				e.f.triggers[res.TriggerTokenID] = tr
				if op == "remove" {
					err = RemoveGitLabTriggerAccount(context.Background(), e.opts())
				} else {
					_, err = RotateGitLabTriggerAccount(context.Background(), e.opts())
				}
				want := "not owned"
				if owner == 0 && op != "remove" {
					// Rotation re-converges, which refuses an unknown-owner
					// trigger before any elevation and reports its ID.
					want = "unknown owner"
				}
				require.ErrorContains(t, err, want)
				assert.Contains(t, e.f.triggers, res.TriggerTokenID, "foreign token not revoked")
				assert.Zero(t, e.f.calls["RevokePipelineTriggerToken"])
				st, ok := e.ledger(t)
				require.True(t, ok, "obligation kept")
				assert.Equal(t, res.TriggerTokenID, recorded(t, st).TriggerTokenID)
			})
		}
	}
	t.Run("recorded bootstrap PAT with another name", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: "someone", Active: true, UserID: res.AccountID}
		st, _ := e.ledger(t)
		st.PATID = 9
		e.setLedger(t, st)
		require.ErrorContains(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()), "refusing")
		assert.True(t, e.f.pats[9].Active, "mismatched PAT is not revoked")
		assert.Contains(t, e.f.accounts, res.AccountID, "account kept")
		got, ok := e.ledger(t)
		require.True(t, ok, "obligation kept")
		assert.Equal(t, 9, got.PATID)
	})
	t.Run("uninstall proves the account before revoking its tokens", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		st, _ := e.ledger(t)
		e.f.pats[9] = &ProjectAccessToken{ID: 9, Name: st.patName(), Active: true, UserID: res.AccountID}
		e.f.accounts[res.AccountID] = GitLabServiceAccount{ID: res.AccountID, Name: "admin-renamed"}
		revokes := e.f.calls["RevokeServiceAccountPAT"]
		require.ErrorContains(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()), "refusing")
		assert.True(t, e.f.pats[9].Active, "no account-scoped cleanup before the account is proven")
		assert.Equal(t, revokes, e.f.calls["RevokeServiceAccountPAT"])
	})
	t.Run("relabelled trigger", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		tr := e.f.triggers[res.TriggerTokenID]
		tr.Description = "admin"
		e.f.triggers[res.TriggerTokenID] = tr
		_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "refusing")
	})
	t.Run("trigger owned by someone else", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		tr := e.f.triggers[res.TriggerTokenID]
		tr.OwnerID = 1
		e.f.triggers[res.TriggerTokenID] = tr
		_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "not owned")
	})
	t.Run("recorded account deleted", func(t *testing.T) {
		e := newTrigEnv()
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		delete(e.f.accounts, res.AccountID)
		_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "no longer exists")
		require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
		e.assertRemoved(t)
	})
	t.Run("duplicate nonce accounts", func(t *testing.T) {
		e := newTrigEnv()
		e.f.lose["CreateProjectServiceAccount"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err)
		st, _ := e.ledger(t)
		e.f.accounts[999] = GitLabServiceAccount{ID: 999, Name: st.accountName()}
		_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "refusing to choose")
	})
}

func TestConvergeGitLabTriggerAccount_BranchChangeAndBadLedger(t *testing.T) {
	e := newTrigEnv()
	_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	o := e.opts()
	o.Branch = "develop"
	before := len(e.f.pushes)
	res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	assert.False(t, res.Ready)
	assert.Contains(t, res.Deferred, "Run repos uninstall")
	res, err = RotateGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	assert.Contains(t, res.Deferred, "recorded for protected branch")
	assert.Len(t, e.f.pushes, before, "nothing is granted on the new branch")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.Equal(t, trigBranch, st.Branch, "the ledger keeps the recorded branch so uninstall stays exact")
	require.NoError(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()))
	e.assertRemoved(t)
	_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)

	key := trigOwner + "/" + trigRepo + "/" + forge.VarGitLabTriggerAccount
	e.state.VariableValues[key] = "  "
	_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "blank")
	require.ErrorContains(t, RemoveGitLabTriggerAccount(context.Background(), e.opts()), "blank")

	e.state.Errors = map[string]error{"GetRepoVariable": errTrigInjected}
	_, err = ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.Error(t, err)
}

func TestConvergeGitLabTriggerAccount_RequiresDependencies(t *testing.T) {
	e := newTrigEnv()
	o := e.opts()
	o.Bootstrap = nil
	_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.Error(t, err)
	o = e.opts()
	o.API = nil
	_, err = ConvergeGitLabTriggerAccount(context.Background(), o)
	require.Error(t, err)
	o = e.opts()
	o.Rand = strings.NewReader("")
	_, err = ConvergeGitLabTriggerAccount(context.Background(), o)
	require.ErrorContains(t, err, "nonce")
	o = e.opts()
	o.Now, o.Rand = nil, nil
	res, err := ConvergeGitLabTriggerAccount(context.Background(), o)
	require.NoError(t, err)
	e.assertConverged(t, res)
}

func TestConvergeGitLabTriggerAccount_BadCreateResponses(t *testing.T) {
	t.Run("account without ID", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.API = trigZeroAccount{e.f}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "no account ID")
	})
	t.Run("bootstrap auth fails", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.Bootstrap = func(string) (GitLabTriggerBootstrapAPI, error) { return nil, errTrigInjected }
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "authenticating")
		assert.Zero(t, e.activePATs(), "PAT revoked after a failed bootstrap")
		for id, level := range e.f.members {
			assert.Equal(t, forge.GitLabAccessLevelDeveloper, level, "account %d demoted", id)
		}
	})
	t.Run("bad bootstrap responses", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.Bootstrap = func(string) (GitLabTriggerBootstrapAPI, error) { return trigBadBootstrap{}, nil }
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "no key ID")
	})
	t.Run("wrong fingerprint", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.Bootstrap = func(string) (GitLabTriggerBootstrapAPI, error) {
			return trigBadBootstrap{keyID: 50, fingerprint: "SHA256:x"}, nil
		}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "unexpected fingerprint")
	})
	t.Run("trigger without token", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.Bootstrap = func(string) (GitLabTriggerBootstrapAPI, error) { return trigBadBootstrap{keyID: 50}, nil }
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "no token")
	})
	t.Run("PAT without token", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.API = trigEmptyPAT{e.f}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "no token")
	})
	t.Run("grant not readable", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.API = trigNoEntry{e.f}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "could not be read back")
	})
}

type trigZeroAccount struct{ *trigFakeGitLab }

func (trigZeroAccount) CreateProjectServiceAccount(context.Context, string, string, string) (GitLabServiceAccount, error) {
	return GitLabServiceAccount{}, nil
}

type trigEmptyPAT struct{ *trigFakeGitLab }

func (trigEmptyPAT) CreateServiceAccountPAT(context.Context, string, string, int, string, []string, string) (*ProjectAccessToken, error) {
	return &ProjectAccessToken{ID: 1}, nil
}

type trigNoEntry struct{ *trigFakeGitLab }

func (trigNoEntry) ProtectedBranchPushDeployKeyEntryID(context.Context, string, string, string, int) (int, error) {
	return 0, nil
}

type trigBadBootstrap struct {
	keyID       int
	fingerprint string
}

func (b trigBadBootstrap) CreateProjectDeployKey(context.Context, string, string, string, string, bool) (GitLabDeployKey, error) {
	return GitLabDeployKey{ID: b.keyID, FingerprintSHA256: b.fingerprint, CanPush: true}, nil
}

func (trigBadBootstrap) CreatePipelineTriggerToken(context.Context, string, string, string) (*forge.PipelineTriggerToken, error) {
	return &forge.PipelineTriggerToken{ID: 1}, nil
}

func TestRotateGitLabTriggerAccount(t *testing.T) {
	e := newTrigEnv()
	e.seedAdminResources()
	_, err := RotateGitLabTriggerAccount(context.Background(), e.opts())
	require.ErrorContains(t, err, "run install first")

	res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	rotated, err := RotateGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, rotated)
	e.assertAdminResources(t)
	assert.NotEqual(t, res.AccountID, rotated.AccountID, "rotation starts a fresh generation")
	assert.NotContains(t, e.f.accounts, res.AccountID, "the previous generation is retired")
	assert.NotEqual(t, res.DeployKeyID, rotated.DeployKeyID)
	assert.NotEqual(t, res.TriggerTokenID, rotated.TriggerTokenID)
	assert.NotContains(t, e.f.keys, res.DeployKeyID)
	assert.NotContains(t, e.f.triggers, res.TriggerTokenID)

	// An interrupted rotation resumes through install.
	e.f.fail["CreateProjectDeployKey"] = true
	_, err = RotateGitLabTriggerAccount(context.Background(), e.opts())
	require.Error(t, err)
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.Equal(t, gitLabTriggerPhaseRotating, st.Phase, "an unfinished rotation is recorded as such")
	again, err := RotateGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, again)

	// A failed retirement step keeps the previous generation in the ledger
	// and leaves the rotation resumable.
	e.f.fail["RevokePipelineTriggerToken"] = true
	_, err = RotateGitLabTriggerAccount(context.Background(), e.opts())
	require.Error(t, err)
	st, ok = e.ledger(t)
	require.True(t, ok)
	assert.Len(t, st.Retired, 1, "the unfinished retirement is recorded")
	final, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
	require.NoError(t, err)
	e.assertConverged(t, final)
}

// TestRotateGitLabTriggerAccount_InterruptedRemovalReplacesEveryResource
// interrupts each deletion of the old resource set, both before and after it
// takes effect, and resumes through rotate and through install. Either way no
// old resource survives and every resource ID is replaced.
func TestRotateGitLabTriggerAccount_InterruptedRemovalReplacesEveryResource(t *testing.T) {
	steps := []string{
		"ListPipelineTriggerTokens", "RevokePipelineTriggerToken",
		"RevokeProtectedBranchPushDeployKeyEntry", "ListProjectDeployKeys",
		"DeleteProjectDeployKey",
	}
	resume := map[string]func(context.Context, GitLabTriggerAccountOptions) (GitLabTriggerAccountResult, error){
		"rotate":  RotateGitLabTriggerAccount,
		"install": ConvergeGitLabTriggerAccount,
	}
	for _, mode := range []string{"fail", "lose"} {
		for _, step := range steps {
			for name, again := range resume {
				t.Run(mode+"/"+step+"/"+name, func(t *testing.T) {
					e := newTrigEnv()
					e.seedAdminResources()
					old, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
					require.NoError(t, err)
					if mode == "fail" {
						e.f.fail[step] = true
					} else {
						e.f.lose[step] = true
					}
					_, err = RotateGitLabTriggerAccount(context.Background(), e.opts())
					require.Error(t, err)
					st, ok := e.ledger(t)
					require.True(t, ok)
					assert.NotEmpty(t, st.Retired, "the unfinished retirement is recorded")

					res, err := again(context.Background(), e.opts())
					require.NoError(t, err)
					e.assertConverged(t, res)
					e.assertAdminResources(t)
					assert.NotEqual(t, old.AccountID, res.AccountID, "fresh generation")
					assert.NotContains(t, e.f.accounts, old.AccountID, "previous generation retired")
					assert.NotEqual(t, old.DeployKeyID, res.DeployKeyID)
					assert.NotEqual(t, old.GrantEntryID, res.GrantEntryID)
					assert.NotEqual(t, old.TriggerTokenID, res.TriggerTokenID)
					assert.NotContains(t, e.f.keys, old.DeployKeyID)
					assert.NotContains(t, e.f.triggers, old.TriggerTokenID)
					for _, p := range e.f.pushes {
						assert.NotEqual(t, old.GrantEntryID, p.ID, "old grant removed")
					}
				})
			}
		}
	}
}

// trigCtxAPI makes the account-scoped calls honor cancellation like a real
// HTTP client, so a test can prove cleanup does not depend on the caller's ctx.
type trigCtxAPI struct{ *trigFakeGitLab }

func (a trigCtxAPI) GetProjectMemberAccessLevel(ctx context.Context, o, r string, id int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return a.trigFakeGitLab.GetProjectMemberAccessLevel(ctx, o, r, id)
}

func (a trigCtxAPI) UpdateProjectMemberAccessLevel(ctx context.Context, o, r string, id int64, level int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.trigFakeGitLab.UpdateProjectMemberAccessLevel(ctx, o, r, id, level)
}

func (a trigCtxAPI) ListServiceAccountPATs(ctx context.Context, o, r string, id int) ([]ProjectAccessToken, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return a.trigFakeGitLab.ListServiceAccountPATs(ctx, o, r, id)
}

func (a trigCtxAPI) RevokeServiceAccountPAT(ctx context.Context, o, r string, id, tokenID int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.trigFakeGitLab.RevokeServiceAccountPAT(ctx, o, r, id, tokenID)
}

// trigCanceledBootstrap is a bootstrap client whose calls fail as a canceled
// request does.
type trigCanceledBootstrap struct{}

func (trigCanceledBootstrap) CreateProjectDeployKey(context.Context, string, string, string, string, bool) (GitLabDeployKey, error) {
	return GitLabDeployKey{}, context.Canceled
}

func (trigCanceledBootstrap) CreatePipelineTriggerToken(context.Context, string, string, string) (*forge.PipelineTriggerToken, error) {
	return nil, context.Canceled
}

// assertContained checks the account is back at Developer with no active
// bootstrap PAT and that the ledger no longer records either obligation.
func (e *trigEnv) assertContained(t *testing.T) {
	t.Helper()
	accts := e.managedAccounts()
	require.Len(t, accts, 1)
	assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[accts[0].ID], "account demoted")
	assert.Zero(t, e.activePATs(), "bootstrap PAT revoked")
	st, ok := e.ledger(t)
	require.True(t, ok)
	assert.False(t, st.Elevated)
	assert.Zero(t, st.PATID)
}

func TestConvergeGitLabTriggerAccount_TeardownRunsIndependently(t *testing.T) {
	t.Run("demotion is attempted when PAT revocation fails", func(t *testing.T) {
		e := newTrigEnv()
		e.f.fail["RevokeServiceAccountPAT"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err)
		accts := e.managedAccounts()
		require.Len(t, accts, 1)
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[accts[0].ID], "demoted despite the failed revocation")
		assert.Equal(t, 1, e.activePATs(), "the unrevoked PAT is still tracked by GitLab")
		st, _ := e.ledger(t)
		assert.False(t, st.Elevated)
		assert.NotZero(t, st.PATID, "the unresolved PAT obligation stays recorded")

		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		e.assertConverged(t, res)
	})
	t.Run("demotion is attempted when the revocation ledger write fails", func(t *testing.T) {
		e := newTrigEnv()
		seenPAT := false
		e.state.failWhen = func(st gitLabTriggerAccountState) bool {
			if st.PATID != 0 {
				seenPAT = true
			}
			return seenPAT && st.PATID == 0 && st.Elevated
		}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err)
		e.assertContained(t)
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		e.assertConverged(t, res)
	})
	t.Run("a failed PAT listing does not skip demotion", func(t *testing.T) {
		e := newTrigEnv()
		e.f.exactRule = true
		// The install lists PATs three times; fail the one that ends bootstrap.
		calls := 0
		o := e.opts()
		o.API = trigListFailAPI{trigFakeGitLab: e.f, failOn: 3, calls: &calls}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.Error(t, err)
		accts := e.managedAccounts()
		require.Len(t, accts, 1)
		assert.Equal(t, forge.GitLabAccessLevelDeveloper, e.f.members[accts[0].ID])
	})
	t.Run("cleanup survives a canceled context", func(t *testing.T) {
		e := newTrigEnv()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		o := e.opts()
		o.API = trigCtxAPI{e.f}
		inner := o.Bootstrap
		o.Bootstrap = func(token string) (GitLabTriggerBootstrapAPI, error) {
			if _, err := inner(token); err != nil {
				return nil, err
			}
			cancel()
			return trigCanceledBootstrap{}, nil
		}
		_, err := ConvergeGitLabTriggerAccount(ctx, o)
		require.ErrorIs(t, err, context.Canceled)
		e.assertContained(t)
	})
}

// trigListFailAPI fails the failOn-th ListServiceAccountPATs call.
type trigListFailAPI struct {
	*trigFakeGitLab
	failOn int
	calls  *int
}

func (a trigListFailAPI) ListServiceAccountPATs(ctx context.Context, o, r string, id int) ([]ProjectAccessToken, error) {
	*a.calls++
	if *a.calls == a.failOn {
		return nil, errTrigInjected
	}
	return a.trigFakeGitLab.ListServiceAccountPATs(ctx, o, r, id)
}

func TestConvergeGitLabTriggerAccount_ContainsBootstrapBeforeAnythingElse(t *testing.T) {
	// leftover leaves the account Maintainer by failing the demotion once.
	leftover := func(t *testing.T) *trigEnv {
		e := newTrigEnv()
		e.f.fail["UpdateProjectMemberAccessLevel"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err)
		accts := e.managedAccounts()
		require.Len(t, accts, 1)
		require.Equal(t, forge.GitLabAccessLevelMaintainer, e.f.members[accts[0].ID], "the interrupted run left the account elevated")
		return e
	}

	t.Run("removed exact rule", func(t *testing.T) {
		e := leftover(t)
		e.f.exactRule = false
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		assert.Contains(t, res.Deferred, "no exact-name protected-branch rule")
		e.assertContained(t)
	})
	t.Run("unavailable service accounts", func(t *testing.T) {
		e := leftover(t)
		e.f.saDisabled = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err, "containment needs the account inventory and says so")
		assert.ErrorContains(t, err, "could not be contained")
	})
	t.Run("unreadable key inventory", func(t *testing.T) {
		e := leftover(t)
		e.f.fail["ListProjectDeployKeys"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err)
		e.assertContained(t)
	})
	t.Run("unreadable trigger inventory", func(t *testing.T) {
		e := leftover(t)
		e.f.fail["ListPipelineTriggerTokens"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err)
		e.assertContained(t)
	})
	t.Run("active PAT and removed rule", func(t *testing.T) {
		e := newTrigEnv()
		e.f.fail["RevokeServiceAccountPAT"] = true
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.Error(t, err)
		require.Equal(t, 1, e.activePATs())
		e.f.exactRule = false
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		assert.NotEmpty(t, res.Deferred)
		e.assertContained(t)
	})
	t.Run("unresolved containment is an error, not a deferral", func(t *testing.T) {
		e := leftover(t)
		e.f.exactRule = false
		e.f.fail["UpdateProjectMemberAccessLevel"] = true
		res, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.ErrorContains(t, err, "could not be contained")
		assert.Empty(t, res.Deferred)
		st, _ := e.ledger(t)
		assert.True(t, st.Elevated, "the obligation stays recorded")
	})
	t.Run("deleted account has nothing left to contain", func(t *testing.T) {
		e := leftover(t)
		st, _ := e.ledger(t)
		delete(e.f.accounts, st.AccountID)
		e.f.exactRule = false
		_, err := ConvergeGitLabTriggerAccount(context.Background(), e.opts())
		require.NoError(t, err)
		got, _ := e.ledger(t)
		assert.False(t, got.Elevated)
		assert.Zero(t, got.PATID)
	})
}

func TestGitLabTriggerAccount_CallbackErrorsDoNotLeakSecrets(t *testing.T) {
	t.Run("bootstrap factory", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.Bootstrap = func(token string) (GitLabTriggerBootstrapAPI, error) {
			return nil, fmt.Errorf("rejected credential %s", token)
		}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "authenticating")
		assert.NotContains(t, err.Error(), "pat-")
	})
	t.Run("trigger publisher", func(t *testing.T) {
		e := newTrigEnv()
		o := e.opts()
		o.PublishTriggerToken = func(_ context.Context, _ int64, token string) error {
			return fmt.Errorf("store rejected %s: %w", token, errTrigInjected)
		}
		_, err := ConvergeGitLabTriggerAccount(context.Background(), o)
		require.ErrorContains(t, err, "publishing")
		assert.NotContains(t, err.Error(), "glptt-")
		assert.ErrorIs(t, err, errTrigInjected)
	})
}

func TestDecodeGitLabTriggerAccountState(t *testing.T) {
	valid := `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":5,"deploy_key_fingerprint":"SHA256:x","deploy_key_id":6,"grant_entry_id":7,"trigger_token_id":8,"trigger_published":true,"publish_started":true}`
	_, err := decodeGitLabTriggerAccountState(valid)
	require.NoError(t, err)

	bad := map[string]string{
		"not json":                 `{`,
		"trailing":                 valid + `{}`,
		"unknown field":            `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","x":1}`,
		"duplicate key":            `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":5,"account_id":0}`,
		"version":                  `{"version":2,"phase":"ready","branch":"main","nonce":"0123456789abcdef"}`,
		"phase":                    `{"version":1,"phase":"done","branch":"main","nonce":"0123456789abcdef"}`,
		"nonce":                    `{"version":1,"phase":"ready","branch":"main","nonce":"XYZ"}`,
		"upper nonce":              `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789ABCDEF"}`,
		"wildcard branch":          `{"version":1,"phase":"ready","branch":"*","nonce":"0123456789abcdef"}`,
		"negative":                 `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","pat_id":-1}`,
		"pat no account":           `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","pat_id":3}`,
		"key no print":             `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","deploy_key_id":3}`,
		"grant no key":             `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","grant_entry_id":3}`,
		"published no ID":          `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","trigger_published":true,"publish_started":true}`,
		"published without marker": `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","account_id":5,"trigger_token_id":8,"trigger_published":true}`,
		"nested retired":           `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","retired":[{"version":1,"phase":"removing","branch":"main","nonce":"1123456789abcdef","retired":[{"version":1,"phase":"removing","branch":"main","nonce":"2123456789abcdef"}]}]}`,
		"duplicate retired nonce":  `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","retired":[{"version":1,"phase":"removing","branch":"main","nonce":"0123456789abcdef"}]}`,
		"retired wildcard branch":  `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","retired":[{"version":1,"phase":"removing","branch":"*","nonce":"1123456789abcdef"}]}`,
		"elevated no acct":         `{"version":1,"phase":"ready","branch":"main","nonce":"0123456789abcdef","elevated":true}`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := decodeGitLabTriggerAccountState(raw)
			require.Error(t, err)
		})
	}
}
