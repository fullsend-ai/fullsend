package cli

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	gl "github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Diagnostic env vars published after GitLab role selection. They carry
// names and policy labels, never token values.
const (
	envGitLabRole       = "FULLSEND_GITLAB_ROLE"
	envGitLabRoleSecret = "FULLSEND_GITLAB_ROLE_SECRET"
	envGitLabRoleSource = "FULLSEND_GITLAB_ROLE_SOURCE"
)

func resolveGitLabPollerCredential(getenv func(string) string) (gitlabroles.Selection, string, error) {
	sel, err := gitlabroles.Select(gitlabroles.PollerJob(), getenv)
	if err != nil {
		return sel, "", err
	}
	token, err := sel.Token(getenv)
	if err != nil {
		return sel, "", err
	}
	return sel, token, nil
}

func resolveGitLabAgentCredential(agentName, harnessRole string, getenv func(string) string) (gitlabroles.Selection, string, error) {
	sel, err := gitlabroles.SelectAgent(agentName, harnessRole, getenv)
	if err != nil {
		return sel, "", err
	}
	token, err := sel.Token(getenv)
	if err != nil {
		return sel, "", err
	}
	return sel, token, nil
}

func applyGitLabAgentCredentials(agentName, harnessRole string, getenv func(string) string, setenv func(string, string), printer *ui.Printer) error {
	sel, token, err := resolveGitLabAgentCredential(agentName, harnessRole, getenv)
	if err != nil {
		return err
	}
	applyGitLabRoleSelection(sel, token, setenv, printer)
	return nil
}

func applyGitLabRoleSelection(sel gitlabroles.Selection, token string, setenv func(string, string), printer *ui.Printer) {
	if printer != nil {
		for _, line := range sel.Diagnostics() {
			printer.StepInfo(line)
		}
	}
	if setenv == nil {
		setenv = func(k, v string) { _ = os.Setenv(k, v) }
	}
	setenv("GITLAB_TOKEN", token)
	setenv(envGitLabRole, string(sel.Source.Role))
	setenv(envGitLabRoleSecret, sel.Source.SecretName)
	setenv(envGitLabRoleSource, sel.IdentitySource())
	if sel.Mode.UsesSharedOnly() {
		return
	}
	clearSiblingGitLabRoleSecrets(sel, setenv)
	if sel.Registration.Has(gitlabroles.CapWriteRepository) {
		setenv("PUSH_TOKEN", token)
		setenv("PUSH_TOKEN_SOURCE", "pat")
		return
	}
	// Analyst, Poller, and custom roles without write_repository must
	// not inherit the shared PUSH_TOKEN exported by CI templates.
	setenv("PUSH_TOKEN", "")
}

// clearSiblingGitLabRoleSecrets blanks every registered role secret (and
// the shared FULLSEND_FORGE_TOKEN) that sel.Present reports as configured
// but that is not the credential this job selected. Without this, a
// sibling secret such as FULLSEND_GITLAB_ANALYST_TOKEN remains sitting in
// the process environment after a Coder job selects its own token; a
// host-side pre/post-script inherits the whole process environment (see
// childScriptEnv) and could read that sibling secret directly and
// authenticate to GitLab as Analyst, bypassing checkGitLabApprovalCapability
// entirely — that check only runs inside `fullsend post-review` itself, not
// for arbitrary script code reading a raw CI/CD variable (see PR #7510).
func clearSiblingGitLabRoleSecrets(sel gitlabroles.Selection, setenv func(string, string)) {
	for name, present := range sel.Present {
		if !present || name == sel.Source.SecretName {
			continue
		}
		setenv(name, "")
	}
}

func logGitLabRoleDiagnostics(sel gitlabroles.Selection, printer *ui.Printer) {
	if printer == nil {
		return
	}
	for _, line := range sel.Diagnostics() {
		printer.StepInfo(line)
	}
}

func isGitLabAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	if forge.IsForbidden(err) {
		return true
	}
	var api *gl.APIError
	if errors.As(err, &api) && api != nil {
		return api.StatusCode == http.StatusUnauthorized || api.StatusCode == http.StatusForbidden
	}
	return false
}

func wrapGitLabAuthFailure(sel gitlabroles.Selection, err error) error {
	if err == nil || !isGitLabAuthFailure(err) {
		return err
	}
	return fmt.Errorf("%w: %w", gitlabroles.AuthFailed(sel.Source.Role, sel.Mode, sel.Source.SecretName), err)
}

// checkGitLabApprovalCapability rejects GitLab APPROVE reviews when the
// identity that will actually authenticate the approve call does not
// declare approve_merge_request. Disabled and rollback keep the
// shared-token path so existing installations are unchanged. getenv nil
// means os.Getenv.
//
// token is the credential post-review will use to authenticate the
// approve call (resolvePostReviewClient prefers --token, then
// GITLAB_TOKEN). The running identity is derived from
// FULLSEND_GITLAB_ROLE / STAGE labels, which is necessary to select a
// registration but is not sufficient on its own: label and token can
// diverge (e.g. --token pointing at a different role's secret), so the
// selected identity's own secret value is compared against token before
// its capability is trusted. A mismatch fails closed with
// ErrIdentityMismatch rather than silently checking the wrong identity's
// capabilities (see PR #7510).
func checkGitLabApprovalCapability(forgeName, action, token string, getenv func(string) string) error {
	if forgeName != repos.ForgeGitLab {
		return nil
	}
	event, ok := reviewActionToEvent(action)
	if !ok || event != "APPROVE" {
		return nil
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	mode, err := gitlabroles.ModeFrom(getenv)
	if err != nil {
		return err
	}
	if mode.UsesSharedOnly() {
		return nil
	}
	agentName := strings.TrimSpace(getenv(envGitLabRole))
	if agentName == "" {
		agentName = strings.TrimSpace(getenv("STAGE"))
	}
	sel, err := gitlabroles.SelectAgent(agentName, agentName, getenv)
	if err != nil {
		return err
	}
	authToken := strings.TrimSpace(token)
	if authToken == "" {
		authToken = strings.TrimSpace(getenv("GITLAB_TOKEN"))
	}
	secretValue := strings.TrimSpace(getenv(sel.Source.SecretName))
	if authToken == "" || secretValue == "" || authToken != secretValue {
		return &gitlabroles.Error{
			Role:   sel.Source.Role,
			Mode:   mode,
			Secret: sel.Source.SecretName,
			Err:    gitlabroles.ErrIdentityMismatch,
		}
	}
	return gitlabroles.Require(sel.Registration, gitlabroles.CapApproveMergeRequest)
}
