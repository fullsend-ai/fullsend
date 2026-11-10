package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/e2eauth"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	e2eAuthOwner = "fullsend-ai"
	e2eAuthRepo  = "fullsend"
	e2eAuthPR    = 42
	e2eAuthKey   = "fullsend-ai/fullsend/42"
)

func e2eAuthCollabKey(user string) string {
	return e2eAuthOwner + "/" + e2eAuthRepo + "/" + user
}

func newE2EAuthFake() *forge.FakeClient {
	return &forge.FakeClient{
		PullRequestAuthContexts:       make(map[string]forge.PullRequestAuthorizationContext),
		IssueLabelEvents:              make(map[string][]forge.IssueLabelEvent),
		CollaboratorPermissions:       make(map[string]string),
		CollaboratorPermissionDetails: make(map[string]forge.GitHubCollaboratorPermissionDetail),
		CollaboratorPermissionErrors:  make(map[string]error),
	}
}

func e2eAuthGate(t *testing.T, fc *forge.FakeClient) e2eauth.GitHubGate {
	t.Helper()
	gate, err := e2eauth.GateFromForge(fc)
	require.NoError(t, err)
	return gate
}

func e2eAuthBaseOpts() checkE2EAuthOpts {
	return checkE2EAuthOpts{
		prNumber: e2eAuthPR,
		repo:     e2eAuthOwner + "/" + e2eAuthRepo,
		dryRun:   true,
	}
}

func runE2EAuth(t *testing.T, gate e2eauth.GitHubGate, opts checkE2EAuthOpts, githubOutput string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	err = runCheckE2EAuthorization(context.Background(), gate, opts, &out, &errBuf, githubOutput)
	return out.String(), errBuf.String(), err
}

func TestCiCommand_RegisteredInRoot(t *testing.T) {
	cmd := newRootCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "ci" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected ci subcommand registered in root")

	ci := newCiCmd()
	names := make(map[string]bool)
	for _, sub := range ci.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["check-e2e-authorization"], "expected check-e2e-authorization subcommand")
}

func clearE2EAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PR_NUMBER", "")
	t.Setenv("GITHUB_REPOSITORY", "")
	t.Setenv("EVENT_ACTION", "")
	t.Setenv("PR_UPDATED_AT", "")
	t.Setenv("PR_AUTHOR_LOGIN", "")
	t.Setenv("LABEL_ACTOR_LOGIN", "")
	t.Setenv("EVENT_LABEL", "")
	t.Setenv("EVENT_NAME", "")
	t.Setenv("COMMENT_ID", "")
	t.Setenv("COMMENTER_LOGIN", "")
	t.Setenv("CHECK_E2E_AUTH_DRY_RUN", "")
	t.Setenv("GITHUB_OUTPUT", "")
}

func TestCiCheckE2EAuthorizationCmd_RequiresPR(t *testing.T) {
	clearE2EAuthEnv(t)
	cmd := newCiCheckE2EAuthorizationCmd()
	cmd.SetArgs([]string{"--repo", "fullsend-ai/fullsend"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "PR number is required")
}

func TestCiCheckE2EAuthorizationCmd_RequiresRepo(t *testing.T) {
	clearE2EAuthEnv(t)
	cmd := newCiCheckE2EAuthorizationCmd()
	cmd.SetArgs([]string{"42"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestCiCheckE2EAuthorizationCmd_InvalidPR(t *testing.T) {
	clearE2EAuthEnv(t)
	cmd := newCiCheckE2EAuthorizationCmd()
	cmd.SetArgs([]string{"nope", "fullsend-ai/fullsend"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "positive integer")
}

func TestResolveCheckE2EAuthOpts_PositionalAndEnv(t *testing.T) {
	t.Run("positionals", func(t *testing.T) {
		clearE2EAuthEnv(t)
		opts := checkE2EAuthOpts{}
		err := resolveCheckE2EAuthOpts(&opts, []string{"42", "fullsend-ai/fullsend"}, false)
		require.NoError(t, err)
		assert.Equal(t, 42, opts.prNumber)
		assert.Equal(t, "fullsend-ai/fullsend", opts.repo)
	})

	t.Run("env fallbacks", func(t *testing.T) {
		t.Setenv("PR_NUMBER", "7")
		t.Setenv("GITHUB_REPOSITORY", "acme/widgets")
		t.Setenv("EVENT_ACTION", "labeled")
		t.Setenv("PR_UPDATED_AT", "2026-06-01T10:00:00Z")
		t.Setenv("PR_AUTHOR_LOGIN", "alice")
		t.Setenv("LABEL_ACTOR_LOGIN", "bob")
		t.Setenv("EVENT_LABEL", e2eauth.OkToTestLabel)
		t.Setenv("CHECK_E2E_AUTH_DRY_RUN", "true")
		opts := checkE2EAuthOpts{}
		err := resolveCheckE2EAuthOpts(&opts, nil, false)
		require.NoError(t, err)
		assert.Equal(t, 7, opts.prNumber)
		assert.Equal(t, "acme/widgets", opts.repo)
		assert.Equal(t, "labeled", opts.eventAction)
		assert.Equal(t, "2026-06-01T10:00:00Z", opts.prUpdatedAt)
		assert.Equal(t, "alice", opts.prAuthorLogin)
		assert.Equal(t, "bob", opts.labelActorLogin)
		assert.Equal(t, e2eauth.OkToTestLabel, opts.eventLabel)
		assert.True(t, opts.dryRun)
	})

	t.Run("invalid PR_NUMBER env", func(t *testing.T) {
		clearE2EAuthEnv(t)
		t.Setenv("PR_NUMBER", "nope")
		t.Setenv("GITHUB_REPOSITORY", "fullsend-ai/fullsend")
		opts := checkE2EAuthOpts{}
		err := resolveCheckE2EAuthOpts(&opts, nil, false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "positive integer")
	})

	t.Run("flags win over env", func(t *testing.T) {
		t.Setenv("EVENT_ACTION", "opened")
		t.Setenv("CHECK_E2E_AUTH_DRY_RUN", "true")
		opts := checkE2EAuthOpts{
			prNumber:    3,
			repo:        "fullsend-ai/fullsend",
			eventAction: "labeled",
			dryRun:      false,
		}
		err := resolveCheckE2EAuthOpts(&opts, nil, true)
		require.NoError(t, err)
		assert.Equal(t, "labeled", opts.eventAction)
		assert.False(t, opts.dryRun)
	})
}

func TestRunCheckE2EAuthorization_TrustedAuthor(t *testing.T) {
	fc := newE2EAuthFake()
	fc.CollaboratorPermissions[e2eAuthCollabKey("author")] = "write"
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{AuthorLogin: "author"}
	opts := e2eAuthBaseOpts()
	opts.prAuthorLogin = "author"

	stdout, stderr, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "authorized=true reason=trusted_author label_removed=false head_sha=\n", stdout)
}

func TestRunCheckE2EAuthorization_LabeledOkToTest(t *testing.T) {
	fc := newE2EAuthFake()
	fc.CollaboratorPermissions[e2eAuthCollabKey("contributor")] = "none"
	fc.CollaboratorPermissions[e2eAuthCollabKey("labeler")] = "write"
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{
		AuthorLogin: "contributor",
		Labels:      []string{e2eauth.OkToTestLabel},
	}
	opts := e2eAuthBaseOpts()
	opts.prAuthorLogin = "contributor"
	opts.eventAction = "labeled"
	opts.eventLabel = e2eauth.OkToTestLabel
	opts.labelActorLogin = "labeler"

	stdout, stderr, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "authorized=true reason=ok_to_test label_removed=false head_sha=\n", stdout)
}

func TestRunCheckE2EAuthorization_LabeledOtherLabel(t *testing.T) {
	fc := newE2EAuthFake()
	fc.CollaboratorPermissions[e2eAuthCollabKey("contributor")] = "none"
	fc.CollaboratorPermissions[e2eAuthCollabKey("labeler")] = "write"
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{
		AuthorLogin: "contributor",
		Labels:      []string{e2eauth.OkToTestLabel},
	}
	opts := e2eAuthBaseOpts()
	opts.prAuthorLogin = "contributor"
	opts.eventAction = "labeled"
	opts.eventLabel = "component/cli"
	opts.labelActorLogin = "labeler"

	stdout, stderr, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "authorized=false reason=unauthorized label_removed=false head_sha=\n", stdout)
}

func TestRunCheckE2EAuthorization_StaleRemovesLabel(t *testing.T) {
	fc := newE2EAuthFake()
	fc.CollaboratorPermissions[e2eAuthCollabKey("contributor")] = "none"
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{
		AuthorLogin: "contributor",
		Labels:      []string{e2eauth.OkToTestLabel},
	}
	fc.IssueLabelEvents[e2eAuthKey] = []forge.IssueLabelEvent{
		{LabelName: e2eauth.OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
	}
	opts := e2eAuthBaseOpts()
	opts.prAuthorLogin = "contributor"
	opts.eventAction = "synchronize"
	opts.prUpdatedAt = "2026-06-01T12:00:00Z"
	opts.dryRun = false

	stdout, stderr, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "authorized=false reason=stale_ok_to_test label_removed=true head_sha=\n", stdout)
	assert.Contains(t, fc.RemovedIssueLabels, e2eAuthKey+"/"+e2eauth.OkToTestLabel)
}

func TestRunCheckE2EAuthorization_DryRunDoesNotRemove(t *testing.T) {
	fc := newE2EAuthFake()
	fc.CollaboratorPermissions[e2eAuthCollabKey("contributor")] = "none"
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{
		AuthorLogin: "contributor",
		Labels:      []string{e2eauth.OkToTestLabel},
	}
	fc.IssueLabelEvents[e2eAuthKey] = []forge.IssueLabelEvent{
		{LabelName: e2eauth.OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
	}
	opts := e2eAuthBaseOpts()
	opts.prAuthorLogin = "contributor"
	opts.eventAction = "synchronize"
	opts.prUpdatedAt = "2026-06-01T12:00:00Z"
	opts.dryRun = true

	stdout, stderr, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "authorized=false reason=stale_ok_to_test label_removed=true head_sha=\n", stdout)
	assert.Empty(t, fc.RemovedIssueLabels)
}

func TestRunCheckE2EAuthorization_GitHubOutput(t *testing.T) {
	fc := newE2EAuthFake()
	fc.CollaboratorPermissions[e2eAuthCollabKey("author")] = "write"
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{AuthorLogin: "author"}
	opts := e2eAuthBaseOpts()
	opts.prAuthorLogin = "author"
	outPath := filepath.Join(t.TempDir(), "github-output")

	stdout, stderr, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, outPath)
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Equal(t, "authorized=true reason=trusted_author label_removed=false head_sha=\n", stdout)

	got, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, "authorized=true\nreason=trusted_author\nlabel_removed=false\nhead_sha=\n", string(got))
}

func TestCiCheckE2EAuthorizationCmd_ExecuteTrustedAuthor(t *testing.T) {
	clearE2EAuthEnv(t)
	fc := newE2EAuthFake()
	fc.CollaboratorPermissions[e2eAuthCollabKey("author")] = "write"
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{AuthorLogin: "author"}

	orig := newE2EAuthGateFn
	t.Cleanup(func() { newE2EAuthGateFn = orig })
	newE2EAuthGateFn = func(string) (e2eauth.GitHubGate, error) {
		return e2eAuthGate(t, fc), nil
	}

	outPath := filepath.Join(t.TempDir(), "github-output")
	t.Setenv("GITHUB_OUTPUT", outPath)

	cmd := newCiCheckE2EAuthorizationCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"42", "fullsend-ai/fullsend", "--pr-author-login", "author"})
	require.NoError(t, cmd.Execute())
	assert.Empty(t, stderr.String())
	assert.Equal(t, "authorized=true reason=trusted_author label_removed=false head_sha=\n", stdout.String())

	got, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, "authorized=true\nreason=trusted_author\nlabel_removed=false\nhead_sha=\n", string(got))
}

func TestCiCheckE2EAuthorizationCmd_MissingToken(t *testing.T) {
	clearE2EAuthEnv(t)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("PATH", "/nonexistent")

	var stdout, stderr strings.Builder
	cmd := newCiCheckE2EAuthorizationCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"42", "fullsend-ai/fullsend"})
	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "authorized=false reason=error label_removed=false head_sha=\n", stdout.String())
	assert.Contains(t, stderr.String(), "check-e2e-authorization:")
	assert.Contains(t, stderr.String(), "--forge-token")
}

func TestCiNewE2EAuthGate_WithToken(t *testing.T) {
	gate, err := newE2EAuthGateFn("ghp-test")
	require.NoError(t, err)
	require.NotNil(t, gate)
}

func TestRunCheckE2EAuthorization_InvalidRepo(t *testing.T) {
	fc := newE2EAuthFake()
	opts := checkE2EAuthOpts{prNumber: 1, repo: "nopath"}
	_, _, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("write fail")
}

func TestCiWriteE2EAuthOutput_Errors(t *testing.T) {
	res := e2eauth.Result{Reason: "unauthorized"}
	err := writeE2EAuthOutput(errWriter{}, "", res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write fail")

	dir := t.TempDir()
	var stdout bytes.Buffer
	err = writeE2EAuthOutput(&stdout, dir, res)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GITHUB_OUTPUT")
}

func TestRunCheckE2EAuthorization_IssueComment(t *testing.T) {
	const head = "1111111111111111111111111111111111111111"
	fc := newE2EAuthFake()
	fc.PullRequestHeadSHA = head
	key := e2eAuthOwner + "/" + e2eAuthRepo + "/" + fmt.Sprint(e2eAuthPR)
	fc.IssueComments = make(map[string][]forge.IssueComment)
	fc.IssueComments[key] = []forge.IssueComment{{
		ID:          900,
		Body:        "/ok-to-test " + head,
		Author:      "maint",
		IssueNumber: e2eAuthPR,
	}}
	fc.CollaboratorPermissions[e2eAuthCollabKey("maint")] = "write"

	opts := e2eAuthBaseOpts()
	opts.eventName = "issue_comment"
	opts.commentID = 900
	opts.commenterLogin = "maint"

	stdout, _, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.NoError(t, err)
	assert.Equal(t, "authorized=true reason=ok_to_test label_removed=false head_sha="+head+"\n", stdout)
}

func TestRunCheckE2EAuthorization_APIErrorStderr(t *testing.T) {
	fc := newE2EAuthFake()
	fc.CollaboratorPermissionErrors[e2eAuthCollabKey("author")] = fmt.Errorf("permission API down")
	fc.PullRequestAuthContexts[e2eAuthKey] = forge.PullRequestAuthorizationContext{AuthorLogin: "author"}
	opts := e2eAuthBaseOpts()
	opts.prAuthorLogin = "author"

	stdout, stderr, err := runE2EAuth(t, e2eAuthGate(t, fc), opts, "")
	require.NoError(t, err)
	assert.Equal(t, "authorized=false reason=error label_removed=false head_sha=\n", stdout)
	assert.True(t, strings.Contains(stderr, "check-e2e-authorization:"), stderr)
	assert.True(t, strings.Contains(stderr, "permission API down"), stderr)
}
