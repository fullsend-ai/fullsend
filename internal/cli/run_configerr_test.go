package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/mintclient"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// stubConfigErrStatus replaces the status mint and forge client seams and
// returns the fake forge client plus a pointer to the mint call count.
func stubConfigErrStatus(t *testing.T, mintErr error) (*forge.FakeClient, *int) {
	t.Helper()
	origMint := statusMintToken
	origNewClient := statusNewForgeClientFn
	t.Cleanup(func() {
		statusMintToken = origMint
		statusNewForgeClientFn = origNewClient
	})
	mintCalls := 0
	statusMintToken = func(_ context.Context, _ mintclient.MintRequest) (*mintclient.MintResult, error) {
		mintCalls++
		if mintErr != nil {
			return nil, mintErr
		}
		return &mintclient.MintResult{Token: "ghs_test_token", ExpiresAt: "2026-06-15T12:00:00Z"}, nil
	}
	fc := forge.NewFakeClient()
	statusNewForgeClientFn = func(string) forge.Client { return fc }
	t.Setenv("GITHUB_RUN_ID", "run-9098")
	t.Setenv("PR_HEAD_SHA", "abc1234")
	t.Setenv("FULLSEND_MINT_URL", "")
	t.Setenv("GITHUB_EVENT_PATH", "")
	return fc, &mintCalls
}

var configErrStatusOpts = statusOpts{
	statusRepo: "org/my-repo",
	statusNum:  42,
	mintURL:    "https://mint.example.com",
}

func TestRunAgent_MalformedConfigPostsFailureStatus(t *testing.T) {
	// A config.yaml with a type mismatch still fails the run (fail-closed),
	// but the error is surfaced on the PR as a failure status comment
	// instead of only in the job log (#9098).
	useFakeOpenshell(t)
	fc, _ := stubConfigErrStatus(t, nil)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.yaml"),
		[]byte("version: \"1\"\nmint_url:\n  nested: map\n"),
		0o644,
	))

	var buf bytes.Buffer
	err := runAgent(context.Background(), "review", dir, "", t.TempDir(), "", nil, false, "", "github", "",
		resolveFlags{maxDepth: 10, maxResources: 50}, configErrStatusOpts, ui.New(&buf), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading fullsend config")

	comments := fc.IssueComments["org/my-repo/42"]
	require.Len(t, comments, 1, "output: %s", buf.String())
	body := comments[0].Body
	assert.Contains(t, body, "<!-- fullsend:agent-status:run-9098 -->")
	assert.Contains(t, body, "<!-- fullsend:status:terminal -->")
	assert.Contains(t, body, "Finished Review")
	assert.Contains(t, body, "Failure")
	assert.Contains(t, body, "fullsend config could not be loaded, agent did not run")
	assert.Contains(t, body, "cannot unmarshal")
	assert.Contains(t, buf.String(), "Posted config load failure status comment")
}

func TestRunAgent_MalformedConfigWithoutStatusOptsPostsNothing(t *testing.T) {
	useFakeOpenshell(t)
	fc, mintCalls := stubConfigErrStatus(t, nil)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{{invalid yaml"), 0o644))

	err := runAgent(context.Background(), "review", dir, "", t.TempDir(), "", nil, false, "", "github", "",
		resolveFlags{maxDepth: 10, maxResources: 50}, statusOpts{}, ui.New(&bytes.Buffer{}), false, runOverrideFlags{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading fullsend config")
	assert.Zero(t, *mintCalls)
	assert.Empty(t, fc.IssueComments)
}

func TestReportConfigLoadFailure_RedactsRunnerSecrets(t *testing.T) {
	fc, _ := stubConfigErrStatus(t, nil)
	secret := "s3cr3t-runner-value-9098"
	var buf bytes.Buffer
	reportConfigLoadFailure(context.Background(), "code", t.TempDir(), "github", "", configErrStatusOpts,
		map[string]string{"MY_SECRET": secret}, errors.New("parsing per-repo config: "+secret), ui.New(&buf))

	comments := fc.IssueComments["org/my-repo/42"]
	require.Len(t, comments, 1, "output: %s", buf.String())
	assert.NotContains(t, comments[0].Body, secret)
	assert.Contains(t, comments[0].Body, "Finished Code")
}

func TestReportConfigLoadFailure_Skips(t *testing.T) {
	jiraEvent := filepath.Join(t.TempDir(), "event.json")
	require.NoError(t, os.WriteFile(jiraEvent, []byte(`{"source":{"system":"jira"}}`), 0o644))
	badEvent := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(badEvent, []byte(`{not json`), 0o644))

	tests := []struct {
		name      string
		forgeFlag string
		eventFile string
		sOpts     statusOpts
		loadErr   error
		wantWarn  string
	}{
		{name: "no error", forgeFlag: "github", sOpts: configErrStatusOpts},
		{name: "no status repo", forgeFlag: "github", sOpts: statusOpts{statusNum: 42}, loadErr: errors.New("boom")},
		{name: "no status number", forgeFlag: "github", sOpts: statusOpts{statusRepo: "org/my-repo"}, loadErr: errors.New("boom")},
		{name: "jira event file", forgeFlag: "github", eventFile: jiraEvent, sOpts: configErrStatusOpts, loadErr: errors.New("boom"), wantWarn: "Jira status routing"},
		{name: "unparseable event file", forgeFlag: "github", eventFile: badEvent, sOpts: configErrStatusOpts, loadErr: errors.New("boom"), wantWarn: "Jira status routing"},
		{name: "missing event file", forgeFlag: "github", eventFile: filepath.Join(t.TempDir(), "absent.json"), sOpts: configErrStatusOpts, loadErr: errors.New("boom"), wantWarn: "Jira status routing"},
		{name: "invalid forge flag", forgeFlag: "nope", sOpts: configErrStatusOpts, loadErr: errors.New("boom"), wantWarn: "not a valid forge platform"},
		{name: "invalid status repo", forgeFlag: "github", sOpts: statusOpts{statusRepo: "no-slash", statusNum: 42}, loadErr: errors.New("boom"), wantWarn: "owner/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc, mintCalls := stubConfigErrStatus(t, nil)
			var buf bytes.Buffer
			reportConfigLoadFailure(context.Background(), "code", t.TempDir(), tt.forgeFlag, tt.eventFile, tt.sOpts, nil, tt.loadErr, ui.New(&buf))
			assert.Zero(t, *mintCalls)
			assert.Empty(t, fc.IssueComments)
			if tt.wantWarn != "" {
				assert.Contains(t, buf.String(), tt.wantWarn)
			}
		})
	}
}

func TestReportConfigLoadFailure_SkipsJiraDispatchPayload(t *testing.T) {
	fc, mintCalls := stubConfigErrStatus(t, nil)
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "dispatch"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "dispatch", "event-payload.json"),
		[]byte(`{"_normalized_event":{"source":{"system":"jira"}}}`),
		0o644,
	))
	var buf bytes.Buffer
	reportConfigLoadFailure(context.Background(), "code", dir, "github", "", configErrStatusOpts, nil, errors.New("boom"), ui.New(&buf))
	assert.Zero(t, *mintCalls)
	assert.Empty(t, fc.IssueComments)
	assert.Contains(t, buf.String(), "Jira status routing")
}

func TestReportConfigLoadFailure_MintFailureWarns(t *testing.T) {
	fc, mintCalls := stubConfigErrStatus(t, errors.New("mint unavailable"))
	var buf bytes.Buffer
	reportConfigLoadFailure(context.Background(), "code", t.TempDir(), "github", "", configErrStatusOpts, nil, errors.New("boom"), ui.New(&buf))
	assert.NotZero(t, *mintCalls)
	assert.Empty(t, fc.IssueComments)
	assert.Contains(t, buf.String(), "Failed to post config load failure status")
}
