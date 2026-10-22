package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/forge/gitlab"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// A client without the live GitLab API (the fake) has no trigger-account
// lifecycle: install neither fails nor reports anything.
func TestMaybeConvergeGitLabTriggerAccount_SkipsNonLiveClient(t *testing.T) {
	var buf bytes.Buffer
	err := maybeConvergeGitLabTriggerAccount(context.Background(), &reposInstallConfig{}, forge.NewFakeClient(), ui.New(&buf), "g", "p", true, false, nil)
	require.NoError(t, err)
	assert.Empty(t, buf.String())

	st := &repos.RepoStatus{Owner: "g", Repo: "p"}
	assert.False(t, appendGitLabTriggerAccountStatus(context.Background(), forge.NewFakeClient(), st))
	assert.Nil(t, st.GitLabTriggerAccount)
}

// A dry run reports what would happen and calls GitLab for nothing: the server
// catch-all route fails the test on any request.
func TestMaybeConvergeGitLabTriggerAccount_DryRunMakesNoRequests(t *testing.T) {
	s := newSATestServer(t)
	s.handle("/", func(_ http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s during a dry run", r.Method, r.URL.Path)
	})
	var buf bytes.Buffer
	var client forge.Client = s.client("admin-token")
	_, isLive := client.(*gitlab.LiveClient)
	require.True(t, isLive)

	err := maybeConvergeGitLabTriggerAccount(context.Background(), &reposInstallConfig{dryRun: true}, client, ui.New(&buf), "g", "p", false, false, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Would converge the GitLab trigger account")
}

// --reactivate-schedules reaches the trigger account lifecycle only while the
// committed-template prerequisites convergeSchedules requires hold; a stale
// agent template keeps disabled schedules disabled during ownership
// reconciliation (#8508).
func TestGitLabReactivateSchedules_RequiresTemplateReadiness(t *testing.T) {
	const owner, repo = "g", "p"
	seed := func(t *testing.T) *forge.FakeClient {
		t.Helper()
		fc := forge.NewFakeClient()
		fc.Repos = append(fc.Repos, forge.Repository{FullName: owner + "/" + repo, DefaultBranch: "main"})
		fc.PipelineVarOverrideRoles[owner+"/"+repo] = forge.PipelineVarOverrideNoOneAllowed
		for _, path := range []string{".gitlab-ci.yml", ".gitlab/ci/fullsend-pipeline.yml", ".gitlab/ci/fullsend-agent.yml", ".gitlab/ci/fullsend-poll.yml"} {
			content, err := scaffold.GitLabPerRepoFile(path)
			require.NoError(t, err)
			fc.FileContents[owner+"/"+repo+"/"+path] = content
		}
		return fc
	}
	run := func(fc *forge.FakeClient, flag bool) (bool, string) {
		var out bytes.Buffer
		got := gitlabReactivateSchedules(context.Background(), &reposInstallConfig{reactivateSchedules: flag}, fc, ui.New(&out), owner, repo)
		return got, out.String()
	}

	got, out := run(seed(t), true)
	assert.True(t, got)
	assert.Empty(t, out)

	got, _ = run(seed(t), false)
	assert.False(t, got, "without the flag nothing is reactivated")

	stale := seed(t)
	key := owner + "/" + repo + "/.gitlab/ci/fullsend-agent.yml"
	stale.FileContents[key] = []byte(strings.ReplaceAll(string(stale.FileContents[key]), "event_payload_chunk_00", "event_payload_record"))
	got, out = run(stale, true)
	assert.False(t, got, "an unmerged agent template repair keeps disabled schedules disabled")
	assert.Contains(t, out, "Not reactivating disabled GitLab polling schedules")
}
