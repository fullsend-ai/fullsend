package repos

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestEnsureGitLabPipelineVariableOverrideRole_AlreadyNoOneAllowed(t *testing.T) {
	// Already-correct scenario: a repo whose GitLab project is already
	// set to no_one_allowed must be left untouched, whether or not
	// enforcement is enabled, and reported as Action "none".
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideNoOneAllowed}

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "none" {
		t.Errorf("Action = %q, want %q", res.Action, "none")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideNoOneAllowed {
		t.Errorf("role changed to %q, want unchanged no_one_allowed", got)
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_FreshInstall_FailsClosedByDefault(t *testing.T) {
	// Fresh install scenario: a newly enrolled repo has no override role
	// set yet (empty string, mirroring GitLab's "field absent" case).
	// Enforcement defaults to off, so this must block activation without
	// calling Set.
	t.Setenv(GitLabPipelineVarRestrictionEnv, "")
	fc := forge.NewFakeClient()

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "new-repo", false)
	if err == nil {
		t.Fatal("expected unsafe activation to fail")
	}
	if res.Action != "blocked" {
		t.Errorf("Action = %q, want blocked", res.Action)
	}
	if _, ok := fc.PipelineVarOverrideRoles["acme/new-repo"]; ok {
		t.Errorf("Set should not have been called while enforcement is disabled")
	}
}

// withPipelineScheduleSpecsOverride temporarily replaces the package-level
// managed-schedule list with specs, restoring the original list via
// t.Cleanup. Tests use this to simulate poll-mode selection having been
// migrated off pipeline variables (empty Variables), which is the
// precondition gitlabScheduledPollModeUsesPipelineVariables checks before
// EnsureGitLabPipelineVariableOverrideRole will enforce no_one_allowed.
func withPipelineScheduleSpecsOverride(t *testing.T, specs []ScheduleSpec) {
	t.Helper()
	original := pipelineScheduleSpecs
	pipelineScheduleSpecs = specs
	t.Cleanup(func() { pipelineScheduleSpecs = original })
}

func TestEnsureGitLabPipelineVariableOverrideRole_ConvergeRepair_Enforced(t *testing.T) {
	// Converge/repair scenario: an already-installed repo drifted away
	// from (or never had) no_one_allowed. With enforcement enabled and
	// poll-mode selection no longer tied to pipeline variables (the
	// prerequisite this file's doc comment describes), converge must call
	// Set to bring it to no_one_allowed.
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	withPipelineScheduleSpecsOverride(t, []ScheduleSpec{{ComponentName: "schedule:slash-poll"}})
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideDeveloper}

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "update" {
		t.Errorf("Action = %q, want %q", res.Action, "update")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideNoOneAllowed {
		t.Errorf("role = %q, want %q", got, forge.PipelineVarOverrideNoOneAllowed)
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
	withPipelineScheduleSpecsOverride(t, []ScheduleSpec{{ComponentName: "schedule:slash-poll"}})
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

func TestEnsureGitLabPipelineVariableOverrideRole_OwnerIsNotTheTarget(t *testing.T) {
	// A project set to owner (e.g. from a manual change, or a future
	// rollback of the #7850 direction) is not the no_one_allowed target
	// this function now converges to, so enforcement must still update
	// it rather than treating owner as already-correct.
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	withPipelineScheduleSpecsOverride(t, []ScheduleSpec{{ComponentName: "schedule:slash-poll"}})
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideOwner}

	res, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Action != "update" {
		t.Errorf("Action = %q, want %q", res.Action, "update")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideNoOneAllowed {
		t.Errorf("role = %q, want %q", got, forge.PipelineVarOverrideNoOneAllowed)
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_RefusesWhileSchedulesUsePipelineVariables(t *testing.T) {
	// A regression to legacy variable-based canonical specs must block
	// enforcement for live operations and dry runs alike.
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	withPipelineScheduleSpecsOverride(t, []ScheduleSpec{{Variables: map[string]string{forge.VarPollMode: "slash"}}})
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideDeveloper}

	if _, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false); err == nil {
		t.Fatal("expected an error refusing to enforce while schedules use pipeline variables, got nil")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideDeveloper {
		t.Errorf("role changed to %q, want unchanged developer (Set must not be called)", got)
	}

	if _, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", true); err == nil {
		t.Fatal("expected an error on dry run too, got nil")
	}
}

func TestGitlabScheduledPollModeUsesPipelineVariables(t *testing.T) {
	if gitlabScheduledPollModeUsesPipelineVariables() {
		t.Error("real schedules must not carry pipeline variables")
	}

	withPipelineScheduleSpecsOverride(t, []ScheduleSpec{
		{ComponentName: "schedule:slash-poll", Variables: map[string]string{forge.VarPollMode: "slash"}},
		{ComponentName: "schedule:event-poll"},
	})
	if !gitlabScheduledPollModeUsesPipelineVariables() {
		t.Error("legacy variable schedules must block activation")
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

func TestValidateGitLabPipelineVarRestrictionEnv(t *testing.T) {
	cases := map[string]bool{
		"":          true,
		"disabled":  true,
		"enforced":  true,
		"Enforced":  false, // case mismatch must be rejected, not silently treated as report-only
		"true":      false,
		"yes":       false,
		"enforced ": false, // stray whitespace must be rejected
	}
	for val, wantOK := range cases {
		t.Run(val, func(t *testing.T) {
			t.Setenv(GitLabPipelineVarRestrictionEnv, val)
			err := ValidateGitLabPipelineVarRestrictionEnv()
			if wantOK && err != nil {
				t.Errorf("ValidateGitLabPipelineVarRestrictionEnv() with env=%q = %v, want nil", val, err)
			}
			if !wantOK && err == nil {
				t.Errorf("ValidateGitLabPipelineVarRestrictionEnv() with env=%q = nil, want error", val)
			}
		})
	}
}

func TestEnsureGitLabPipelineVariableOverrideRole_MalformedEnv(t *testing.T) {
	// A misspelled or malformed enforcement value must hard-fail instead
	// of silently falling back to report-only (fail closed, not open).
	t.Setenv(GitLabPipelineVarRestrictionEnv, "Enforced")
	fc := forge.NewFakeClient()
	fc.PipelineVarOverrideRoles = map[string]string{"acme/widgets": forge.PipelineVarOverrideDeveloper}

	_, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), fc, "acme", "widgets", false)
	if err == nil {
		t.Fatal("expected an error for a malformed enforcement value, got nil")
	}
	if got := fc.PipelineVarOverrideRoles["acme/widgets"]; got != forge.PipelineVarOverrideDeveloper {
		t.Errorf("role changed to %q, want unchanged on validation failure", got)
	}
}
