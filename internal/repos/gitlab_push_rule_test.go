package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/poll"
)

func gitlabInstallConfig() InstallConfig {
	return InstallConfig{
		Owner:  "acme",
		Repo:   "widgets",
		Forge:  ForgeGitLab,
		Roles:  []string{"triage"},
		Direct: true,
	}
}

func TestInstall_GitLab_PushRuleRejectsPollStateBranches(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.PushRuleBranchNameRegexes = map[string]string{"acme/widgets": "master"}

	sc := &fakeScaffoldCommit{}
	_, err := Install(context.Background(), gitlabInstallConfig(), fc, sc.fn(), noopProgress)
	if err == nil {
		t.Fatal("expected Install to refuse a push rule that rejects poll-state branches")
	}
	for _, want := range []string{"push rule", `"master"`, poll.PollStateBranchSlash, poll.PollStateBranchEvents} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if sc.called {
		t.Error("scaffold commit must not run after the push-rule preflight fails")
	}
	if len(fc.Variables) != 0 || len(fc.CreatedSecrets) != 0 || len(fc.ForceCommittedFiles) != 0 {
		t.Errorf("expected no writes, got %d variables, %d secrets, %d branch commits",
			len(fc.Variables), len(fc.CreatedSecrets), len(fc.ForceCommittedFiles))
	}
}

func TestInstall_GitLab_PushRulePermitsInstall(t *testing.T) {
	for name, setup := range map[string]func(fc *forge.FakeClient){
		"regex allows fullsend branches": func(fc *forge.FakeClient) {
			fc.PushRuleBranchNameRegexes = map[string]string{"acme/widgets": "^(master|fullsend-.*)$"}
		},
		"no push rule": func(*forge.FakeClient) {},
		"push rules unavailable": func(fc *forge.FakeClient) {
			fc.Errors["GetPushRuleBranchNameRegex"] = fmt.Errorf("get push rule: %w", forge.ErrNotFound)
		},
		"push rules forbidden": func(fc *forge.FakeClient) {
			fc.Errors["GetPushRuleBranchNameRegex"] = fmt.Errorf("get push rule: %w", forge.ErrForbidden)
		},
		"push rules not supported": func(fc *forge.FakeClient) {
			fc.Errors["GetPushRuleBranchNameRegex"] = forge.ErrNotSupported
		},
		"unparseable regex": func(fc *forge.FakeClient) {
			fc.PushRuleBranchNameRegexes = map[string]string{"acme/widgets": "(master"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			setup(fc)
			sc := &fakeScaffoldCommit{}
			result, err := Install(context.Background(), gitlabInstallConfig(), fc, sc.fn(), noopProgress)
			if err != nil {
				t.Fatalf("Install returned error: %v", err)
			}
			if !result.Success {
				t.Error("expected Success=true")
			}
			assertPollStateBranchesSeeded(t, fc, "acme", "widgets")
		})
	}
}

func TestRequireGitLabPushRuleAllowsPollStateBranches_ReadError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.Errors["GetPushRuleBranchNameRegex"] = errors.New("boom")
	err := requireGitLabPushRuleAllowsPollStateBranches(context.Background(), fc, "acme", "widgets")
	if err == nil || !strings.Contains(err.Error(), "reading GitLab push rule") {
		t.Fatalf("expected a push-rule read error, got %v", err)
	}
}

func TestRequireGitLabPushRuleAllowsPollStateBranches_ExistingBranchesExempt(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.PushRuleBranchNameRegexes = map[string]string{"acme/widgets": "master"}
	for _, branch := range gitlabPollStateBranches {
		fc.FileContentsRef["acme/widgets/"+poll.PollStateFileName+"@"+branch] = []byte(`{}`)
	}
	if err := requireGitLabPushRuleAllowsPollStateBranches(context.Background(), fc, "acme", "widgets"); err != nil {
		t.Fatalf("already-seeded branches must not be rejected: %v", err)
	}
}

func TestRequireGitLabPushRuleAllowsPollStateBranches_StateProbeError(t *testing.T) {
	fc := newFakeClientWithRepo()
	fc.PushRuleBranchNameRegexes = map[string]string{"acme/widgets": "master"}
	fc.Errors["GetFileContentAtRef"] = errors.New("boom")
	err := requireGitLabPushRuleAllowsPollStateBranches(context.Background(), fc, "acme", "widgets")
	if err == nil || !strings.Contains(err.Error(), "checking poll state") {
		t.Fatalf("expected a poll-state probe error, got %v", err)
	}
}

func TestGitLabFreshInstallDryRunPreflight_PushRule(t *testing.T) {
	for name, tc := range map[string]struct {
		regex   string
		wantErr bool
	}{
		"rejects":  {regex: "master", wantErr: true},
		"allows":   {regex: "^(master|fullsend-poll-state-.*)$"},
		"no rule":  {regex: ""},
		"badregex": {regex: "[", wantErr: false},
	} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			fc.PushRuleBranchNameRegexes = map[string]string{"acme/widgets": tc.regex}
			resolved := ResolvedConfig{ForgeConfig: ForgeConfig{Client: fc}}
			err := gitlabFreshInstallDryRunPreflight(context.Background(), resolved, gitlabInstallConfig())
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "push rule") {
					t.Fatalf("expected push-rule error, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(fc.Variables) != 0 || len(fc.ForceCommittedFiles) != 0 {
				t.Error("dry-run preflight must not write")
			}
		})
	}
}
