package executionplan

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func examplePlan() Plan {
	return Plan{Version: 1, ID: "case-123", Request: "advisory-123", Authorization: "reviewed-grant", Harness: "assessment", ControlSHA: strings.Repeat("a", 40), Deadline: time.Now().Add(time.Hour), MaxParallel: 2, MaxAttempts: 2, Targets: []Target{
		{ID: "release-1", Forge: "github", Host: "github.com", Repository: "example/project", Ref: "refs/heads/release-1", SHA: strings.Repeat("b", 40)},
		{ID: "release-2", Forge: "gitlab", Host: "gitlab.example.org", Repository: "example/project", Ref: "refs/heads/release-2", SHA: strings.Repeat("c", 40)},
		{ID: "release-3", Forge: "forgejo", Host: "forgejo.example.org", Repository: "example/project", Ref: "refs/heads/release-3", SHA: strings.Repeat("d", 40)},
	}}
}

type testDriver struct {
	denied       string
	receipts     map[string]*Receipt
	submitted    []Attempt
	submitErr    error
	lookupErr    error
	beforeSubmit func(Attempt)
}

func (d *testDriver) Authorize(_ context.Context, _ Plan, target Target) error {
	if target.ID == d.denied {
		return errors.New("denied")
	}
	return nil
}
func (d *testDriver) Submit(_ context.Context, _ Plan, a Attempt) (*Receipt, error) {
	if d.beforeSubmit != nil {
		d.beforeSubmit(a)
	}
	d.submitted = append(d.submitted, a)
	r := &Receipt{AttemptID: a.ID, PlanDigest: a.PlanDigest, TargetID: a.Target.ID, SourceSHA: a.Target.SHA, RunID: "run-" + a.ID, State: Running}
	if d.receipts == nil {
		d.receipts = map[string]*Receipt{}
	}
	d.receipts[a.ID] = r
	if d.submitErr != nil {
		return nil, d.submitErr
	}
	return r, nil
}
func (d *testDriver) Lookup(_ context.Context, _ Plan, a Attempt) (*Receipt, error) {
	return d.receipts[a.ID], d.lookupErr
}

func TestBoundedFanoutAndCompleteAccounting(t *testing.T) {
	ctx := context.Background()
	p := examplePlan()
	store := &FileStore{Dir: t.TempDir()}
	d := &testDriver{}
	state, err := Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	require.Len(t, d.submitted, 2)
	require.False(t, state.Complete())
	require.Len(t, state.Targets, 3)
	for _, r := range d.receipts {
		r.State = Succeeded
	}
	state, err = Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	require.Len(t, d.submitted, 3)
	require.False(t, state.Complete())
	require.Equal(t, Succeeded, state.Targets[0].Attempts[0].State)
	d.receipts[d.submitted[2].ID].State = Succeeded
	state, err = Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	require.True(t, state.Complete())
	require.Len(t, d.submitted, 3)
}

func TestUnknownDispatchIsNeverBlindlyRepeated(t *testing.T) {
	ctx := context.Background()
	p := examplePlan()
	p.MaxParallel = 1
	store := &FileStore{Dir: t.TempDir()}
	d := &testDriver{submitErr: errors.New("lost acknowledgement")}
	d.beforeSubmit = func(a Attempt) {
		// The durable intent must exist before an external effect.
		state, err := store.Read(p.ID)
		require.NoError(t, err)
		require.Equal(t, a.ID, state.Targets[0].Attempts[0].ID)
		require.Equal(t, Uncertain, state.Targets[0].Attempts[0].State)
	}
	_, err := Reconcile(ctx, p, store, d)
	require.Error(t, err)
	d.beforeSubmit = nil
	d.receipts = nil
	d.submitErr = nil
	state, err := Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	require.Len(t, d.submitted, 1)
	require.Equal(t, Uncertain, state.Targets[0].Attempts[0].State)
	require.False(t, state.Complete())
}

func TestRecoveryBindsResultsAndRetainsBudgets(t *testing.T) {
	ctx := context.Background()
	p := examplePlan()
	p.Targets = p.Targets[:1]
	store := &FileStore{Dir: t.TempDir()}
	d := &testDriver{}
	_, err := Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	first := d.submitted[0]
	r := d.receipts[first.ID]
	r.SourceSHA = strings.Repeat("e", 40)
	r.State = Succeeded
	_, err = Reconcile(ctx, p, store, d)
	require.Error(t, err)
	require.Len(t, d.submitted, 1)
	r.SourceSHA = first.Target.SHA
	r.State = Failed
	state, err := Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	require.Len(t, d.submitted, 2)
	require.NotEqual(t, first.ID, d.submitted[1].ID)
	require.Len(t, state.Targets[0].Attempts, 2)
	d.receipts[d.submitted[1].ID].State = Failed
	state, err = Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	require.False(t, state.Complete())
	require.Len(t, d.submitted, 2)
	require.Equal(t, "exhausted", state.Targets[0].Blocker)
	changed := p
	changed.Targets = append([]Target(nil), p.Targets...)
	changed.Targets[0].SHA = strings.Repeat("f", 40)
	_, err = Reconcile(ctx, changed, store, d)
	require.ErrorIs(t, err, ErrPlanChanged)
	state, err = Reconcile(ctx, p, store, d)
	require.NoError(t, err)
	require.Len(t, state.Targets[0].Attempts, 2)
}

func TestAuthorizationAndDeadline(t *testing.T) {
	ctx := context.Background()
	p := examplePlan()
	d := &testDriver{denied: "release-1"}
	state, err := Reconcile(ctx, p, &FileStore{Dir: t.TempDir()}, d)
	require.NoError(t, err)
	require.Equal(t, "authorization", state.Targets[0].Blocker)
	require.Empty(t, state.Targets[0].Attempts)
	require.Len(t, d.submitted, 2)
	require.False(t, state.Complete())
	p.Deadline = time.Now().Add(-time.Second)
	d = &testDriver{}
	state, err = Reconcile(ctx, p, &FileStore{Dir: t.TempDir()}, d)
	require.NoError(t, err)
	require.Empty(t, d.submitted)
	require.Equal(t, "deadline", state.Targets[0].Blocker)
}

func TestPlanValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Plan)
	}{
		{"version", func(p *Plan) { p.Version = 2 }},
		{"unsafe-id", func(p *Plan) { p.ID = "../escape" }},
		{"authorization", func(p *Plan) { p.Authorization = "" }},
		{"harness", func(p *Plan) { p.Harness = "../harness" }},
		{"control", func(p *Plan) { p.ControlSHA = "main" }},
		{"deadline", func(p *Plan) { p.Deadline = time.Time{} }},
		{"targets", func(p *Plan) { p.Targets = nil }},
		{"parallel", func(p *Plan) { p.MaxParallel = 0 }},
		{"attempts", func(p *Plan) { p.MaxAttempts = 11 }},
		{"duplicate", func(p *Plan) { p.Targets[1].ID = p.Targets[0].ID }},
		{"host", func(p *Plan) { p.Targets[0].Host = "github.com/path" }},
		{"userinfo", func(p *Plan) { p.Targets[0].Host = "user@github.com" }},
		{"forge", func(p *Plan) { p.Targets[0].Forge = "unsupported" }},
		{"repo", func(p *Plan) { p.Targets[0].Repository = "project" }},
		{"traversal", func(p *Plan) { p.Targets[0].Repository = "example/../project" }},
		{"repo-character", func(p *Plan) { p.Targets[0].Repository = "example/proj$ect" }},
		{"ref", func(p *Plan) { p.Targets[0].Ref = "release-1" }},
		{"ref-traversal", func(p *Plan) { p.Targets[0].Ref = "refs/heads/../main" }},
		{"context", func(p *Plan) { p.Targets[0].Context = []byte(`{`) }},
	} {
		t.Run(test.name, func(t *testing.T) { p := examplePlan(); test.change(&p); require.Error(t, p.Validate()) })
	}
	p := examplePlan()
	p.Targets[0].Repository = "group/subgroup/repo.name"
	require.NoError(t, p.Validate())
	for _, part := range []string{"", ".", "..", "-repo", strings.Repeat("x", 256)} {
		require.False(t, repositoryPart(part))
	}
}

func TestLateAuthorizationAndLookupFailureDoNotLaunch(t *testing.T) {
	ctx := context.Background()
	p := examplePlan()
	s := &FileStore{Dir: t.TempDir()}
	d := &testDriver{}
	_, err := Reconcile(ctx, p, s, d)
	require.NoError(t, err)
	d.lookupErr = errors.New("backend unavailable")
	_, err = Reconcile(ctx, p, s, d)
	require.Error(t, err)
	require.Len(t, d.submitted, 2)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = Reconcile(cancelled, p, s, d)
	require.ErrorIs(t, err, context.Canceled)
	_, err = Reconcile(ctx, p, nil, d)
	require.Error(t, err)
	_, err = Reconcile(ctx, p, s, nil)
	require.Error(t, err)
	p.Version = 2
	_, err = Reconcile(ctx, p, s, d)
	require.Error(t, err)
	require.False(t, (State{}).Complete())
}

func TestReceiptCannotChangeRunIdentity(t *testing.T) {
	ctx := context.Background()
	p := examplePlan()
	p.Targets = p.Targets[:1]
	s := &FileStore{Dir: t.TempDir()}
	d := &testDriver{}
	_, err := Reconcile(ctx, p, s, d)
	require.NoError(t, err)
	r := d.receipts[d.submitted[0].ID]
	r.RunID = "different-run"
	_, err = Reconcile(ctx, p, s, d)
	require.Error(t, err)
	state, err := s.Read(p.ID)
	require.NoError(t, err)
	require.Equal(t, Running, state.Targets[0].Attempts[0].State)
}
