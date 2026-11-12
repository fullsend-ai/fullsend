package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitlabroles"
)

// The verbose API error state is process-wide, so the tests that enable it
// never call t.Parallel and always restore the default before returning.

// Fake token-shaped values are assembled at run time so no credential-shaped
// literal appears in the source.
var (
	fakeGitLabPAT     = "glpat" + "-" + strings.Repeat("Ab1", 8)
	fakeGitLabTrigger = "glptt" + "-" + strings.Repeat("Cd2", 8)
)

type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return fmt.Sprintf("gitlab api: %d %s", e.status, e.msg) }
func (e *statusError) Unwrap() error {
	if e.status == 403 {
		return forge.ErrForbidden
	}
	return nil
}

func enableVerbose(t *testing.T, scrub func(string) string) {
	t.Helper()
	t.Cleanup(EnableVerboseAPIErrors(scrub))
}

func TestSafeAPIErrorDefaultWithholdsStatusAndBody(t *testing.T) {
	cause := &statusError{status: 403, msg: "You are not allowed to push " + fakeGitLabPAT}
	err := safeAPIError("granting access", cause)
	assert.Equal(t, "granting access: forbidden (server error text withheld)", err.Error())
	assert.NotContains(t, err.Error(), "403")
	assert.NotContains(t, err.Error(), "not allowed")
}

func TestSafeAPIErrorVerboseShowsScrubbedServerText(t *testing.T) {
	enableVerbose(t, nil)
	hex := strings.Repeat("0123456789abcdef", 4)
	cause := &statusError{status: 403, msg: strings.Join([]string{
		"You are not allowed to push",
		fakeGitLabPAT,
		fakeGitLabTrigger,
		"https://gitlab.example.com/api/v4/projects/1/ref/main/trigger/pipeline?token=opaque-trigger&ref=main",
		"https://user:pass@gitlab.example.com/x",
		hex,
	}, " ")}
	err := safeAPIError("granting access", cause)

	msg := err.Error()
	assert.True(t, strings.HasPrefix(msg, "granting access: forbidden: gitlab api: 403 You are not allowed to push"), msg)
	assert.NotContains(t, msg, "withheld")
	for _, secret := range []string{fakeGitLabPAT, fakeGitLabTrigger, "opaque-trigger", "user:pass", hex} {
		assert.NotContains(t, msg, secret)
	}
	assert.Contains(t, msg, "?token="+credentialRedacted+"&ref=main")
	assert.Contains(t, msg, "https://"+credentialRedacted+"@gitlab.example.com/x")

	require.ErrorIs(t, err, forge.ErrForbidden)
	var se *statusError
	require.ErrorAs(t, err, &se)
	assert.Equal(t, 403, se.status)
}

func TestSafeAPIErrorVerboseScrubsRegisteredAndSuppliedValues(t *testing.T) {
	enableVerbose(t, func(s string) string { return strings.ReplaceAll(s, "cli-flag-secret", "[CLI]") })
	var red credentialRedactor
	red.add("opaque-registered-value")

	err := safeAPIError("storing", errors.New("rejected opaque-registered-value and cli-flag-secret"))
	assert.Equal(t, "storing: request failed: rejected "+credentialRedacted+" and [CLI]", err.Error())

	reason := failureReason("storing role credential failed", errors.New("value submitted-value is invalid"), "submitted-value", "")
	assert.Equal(t, "storing role credential failed: value "+credentialRedacted+" is invalid", reason)
}

func TestVerboseAPIErrorsRestore(t *testing.T) {
	restore := EnableVerboseAPIErrors(nil)
	var red credentialRedactor
	red.add("opaque-registered-value")
	restore()

	err := safeAPIError("listing", errors.New("server said no"))
	assert.Equal(t, "listing: request failed (server error text withheld)", err.Error())
	assert.Equal(t, "creation failed", failureReason("creation failed", errors.New("server said no")))
	assert.Empty(t, verbose.secrets, "restoring forgets remembered values")

	red.add("not-remembered-while-disabled")
	assert.Empty(t, verbose.secrets)
	assert.Empty(t, verboseDetail(nil))
}

func TestSafeAPIErrorShowsSuppliedUnresolvedRecoveryText(t *testing.T) {
	const recovery = "rotation state does not record who provisioned the installed credential; re-enroll the credential with --gitlab-role-token"
	built := fmt.Errorf("%w: %s", ErrPollerSuppliedUnresolved, recovery)
	raw := errors.New("raw server text " + fakeGitLabPAT)

	for name, cause := range map[string]error{
		"direct":           built,
		"wrapped":          fmt.Errorf("reading the poller (%v): %w", raw, built),
		"joined":           errors.Join(raw, built),
		"nested safe text": safeAPIError("inner", built),
	} {
		t.Run(name, func(t *testing.T) {
			err := safeAPIError("resolving the GitLab Poller identity", cause)
			assert.Equal(t, "resolving the GitLab Poller identity: "+ErrSuppliedCredentialUnresolved.Error()+": "+recovery, err.Error())
			assert.NotContains(t, err.Error(), "raw server text")
			require.ErrorIs(t, err, ErrPollerSuppliedUnresolved)
		})
	}

	err := safeAPIError("resolving", ErrSuppliedCredentialUnresolved)
	assert.Equal(t, "resolving: "+ErrSuppliedCredentialUnresolved.Error(), err.Error())

	err = safeAPIError("resolving", fmt.Errorf("outer: %w", raw))
	assert.Equal(t, "resolving: request failed (server error text withheld)", err.Error())
}

func TestSafeAPIErrorVerboseShowsSuppliedUnresolvedWithCause(t *testing.T) {
	enableVerbose(t, nil)
	built := fmt.Errorf("%w: re-enroll the credential with --gitlab-role-token", ErrPollerSuppliedUnresolved)
	err := safeAPIError("resolving the GitLab Poller identity", fmt.Errorf("reading the poller: %w", built))
	assert.Contains(t, err.Error(), "re-enroll the credential with --gitlab-role-token")
	assert.Contains(t, err.Error(), "reading the poller")
}

func TestProvisionGitLabRoleCredentials_VerboseFailureReason(t *testing.T) {
	enableVerbose(t, nil)
	ctx := context.Background()
	fc := provisionClient(t)
	tokens := &fakeTokens{failCreate: map[string]error{
		gitlabroles.AnalystTokenName: fmt.Errorf("gitlab api: 403 insufficient plan %s", fakeGitLabPAT),
	}}

	result, err := ProvisionGitLabRoleCredentials(ctx, RoleProvisionConfig{
		Owner:    "group",
		Repo:     "project",
		Client:   fc,
		Tokens:   tokens,
		Registry: gitlabroles.BuiltinRegistry(),
	})
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)
	assert.Equal(t, "project access token creation failed: gitlab api: 403 insufficient plan "+credentialRedacted, result.Failed[0].Reason)
}
