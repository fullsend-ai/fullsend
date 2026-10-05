package repos

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestEnsureGitLabPipelineVariableOverrideRole_AlreadyOwner(t *testing.T) {
	// Already-correct scenario: a repo whose GitLab project is already
	// set to owner must be left untouched, whether or not enforcement is
	// enabled, and reported as Action "none".
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideOwner}

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "none" {
		t.Errorf("Action = %q, want %q", res.Action, "none")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideOwner {
		t.Errorf("role changed to %q, want unchanged owner", got)
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_FreshInstall_ReportOnlyByDefault(t *testing.T) {
	// Fresh install scenario: a newly enrolled repo has no override role
	// set yet (empty string, mirroring GitLab's "field absent" case).
	// Enforcement defaults to off, so this must report drift without
	// calling Set.
	t.Setenv(GitLabPipelineVarRestrictionEnv, "")
	fc := forge.NewFakeClient()

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "new-repo", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "report-only" {
		t.Errorf("Action = %q, want %q (report-only default)", res.Action, "report-only")
	}
	if _, ok := fc.PipelineVarOverrideRoles["acme/new-repo"]; ok {
		t.Errorf("Set should not have been called while enforcement is disabled")
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_ConvergeRepair_Enforced(t *testing.T) {
	// Converge/repair scenario: an already-installed repo drifted away
	// from (or never had) owner. With enforcement enabled, converge must
	// call Set to bring it to owner.
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideDeveloper}

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "update" {
		t.Errorf("Action = %q, want %q", res.Action, "update")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideOwner {
		t.Errorf("role = %q, want %q", got, forge.PipelineVarOverrideOwner)
	}

	// Idempotent: converging again is a no-op Set-wise.
	res2, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err != nil {
		t.Fatalf("unexpected error on second converge: %v", err)
	}
	if res2.Action != "none" {
		t.Errorf("second converge Action = %q, want %q (idempotent)", res2.Action, "none")
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_Enforced_DryRun(t *testing.T) {
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideMaintainer}

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "would-update" {
		t.Errorf("Action = %q, want %q", res.Action, "would-update")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideMaintainer {
		t.Errorf("dry run must not write; role = %q, want unchanged maintainer", got)
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_Unsupported(t *testing.T) {
	// Mirrors GitHub's forge.Client implementation, which returns
	// forge.ErrNotSupported for both methods.
	fc := &forge.FakeClient{
		Errors: map[string]error{"GetPipelineVariablesMinimumOverrideRole": forge.ErrNotSupported},
	}

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "unsupported" {
		t.Errorf("Action = %q, want %q", res.Action, "unsupported")
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_GetError(t *testing.T) {
	wantErr := errors.New("boom")
	fc := &forge.FakeClient{
		Errors: map[string]error{"GetPipelineVariablesMinimumOverrideRole": wantErr},
	}

	_, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err == nil || !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapping %v", err, wantErr)
	}
}

func TestGitLabPipelineVarRestrictionEnforced(t *testing.T) {
	cases := map[string]bool{
		"":         false,
		"disabled": false,
		"Enforced": false, // exact match only
		"enforced": true,
	}
	for val, want := range cases {
		t.Run(val, func(t *testing.T) {
			old, hadOld := os.LookupEnv(GitLabPipelineVarRestrictionEnv)
			os.Setenv(GitLabPipelineVarRestrictionEnv, val)
			defer func() {
				if hadOld {
					os.Setenv(GitLabPipelineVarRestrictionEnv, old)
				} else {
					os.Unsetenv(GitLabPipelineVarRestrictionEnv)
				}
			}()
			if got := GitLabPipelineVarRestrictionEnforced(); got != want {
				t.Errorf("GitLabPipelineVarRestrictionEnforced() with env=%q = %v, want %v", val, got, want)
			}
		})
	}
}
