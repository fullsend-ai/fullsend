package layers

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newEnrollmentLayer(t *testing.T, client forge.Client, enabledRepos, disabledRepos []string) (*EnrollmentLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewEnrollmentLayer("test-org", client, enabledRepos, disabledRepos, printer)
	return layer, &buf
}

func TestEnrollmentLayer_Name(t *testing.T) {
	layer, _ := newEnrollmentLayer(t, &forge.FakeClient{}, nil, nil)
	assert.Equal(t, "enrollment", layer.Name())
}

func TestEnrollmentLayer_Install_DispatchesWorkflow(t *testing.T) {
	now := time.Now().UTC()
	client := &forge.FakeClient{
		WorkflowRuns: map[string]*forge.WorkflowRun{
			"test-org/.fullsend/repo-maintenance.yml": {
				ID:         1,
				Status:     "completed",
				Conclusion: "success",
				CreatedAt:  now.Add(time.Minute).Format(time.RFC3339),
				HTMLURL:    "https://github.com/test-org/.fullsend/actions/runs/1",
			},
		},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, buf := newEnrollmentLayer(t, client, repos, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "dispatched repo-maintenance workflow")
	assert.Contains(t, output, "enrollment completed successfully")
}

func TestEnrollmentLayer_Install_ReportsEnrollmentPRs(t *testing.T) {
	now := time.Now().UTC()
	client := &forge.FakeClient{
		WorkflowRuns: map[string]*forge.WorkflowRun{
			"test-org/.fullsend/repo-maintenance.yml": {
				ID:         1,
				Status:     "completed",
				Conclusion: "success",
				CreatedAt:  now.Add(time.Minute).Format(time.RFC3339),
				HTMLURL:    "https://github.com/test-org/.fullsend/actions/runs/1",
			},
		},
		PullRequests: map[string][]forge.ChangeProposal{
			"test-org/repo-a": {
				{Title: "chore: connect to fullsend agent pipeline", URL: "https://github.com/test-org/repo-a/pull/1"},
			},
		},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, buf := newEnrollmentLayer(t, client, repos, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "repo-a/pull/1")
}

func TestEnrollmentLayer_Install_ReportsRemovalPRs(t *testing.T) {
	now := time.Now().UTC()
	client := &forge.FakeClient{
		WorkflowRuns: map[string]*forge.WorkflowRun{
			"test-org/.fullsend/repo-maintenance.yml": {
				ID:         1,
				Status:     "completed",
				Conclusion: "success",
				CreatedAt:  now.Add(time.Minute).Format(time.RFC3339),
				HTMLURL:    "https://github.com/test-org/.fullsend/actions/runs/1",
			},
		},
		PullRequests: map[string][]forge.ChangeProposal{
			"test-org/repo-x": {
				{Title: "chore: disconnect from fullsend agent pipeline", URL: "https://github.com/test-org/repo-x/pull/5"},
			},
		},
	}
	layer, buf := newEnrollmentLayer(t, client, nil, []string{"repo-x"})

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "repo-x/pull/5")
}

func TestEnrollmentLayer_Install_NoRepos(t *testing.T) {
	client := &forge.FakeClient{}
	layer, buf := newEnrollmentLayer(t, client, nil, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "no repositories to reconcile")
}

func TestEnrollmentLayer_Install_DispatchError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{
			"DispatchWorkflow": assert.AnError,
		},
	}
	repos := []string{"repo-a"}
	layer, _ := newEnrollmentLayer(t, client, repos, nil)

	err := layer.Install(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dispatching repo-maintenance")
}

func TestEnrollmentLayer_Install_WorkflowWarning(t *testing.T) {
	now := time.Now().UTC()
	client := &forge.FakeClient{
		WorkflowRuns: map[string]*forge.WorkflowRun{
			"test-org/.fullsend/repo-maintenance.yml": {
				ID:         1,
				Status:     "completed",
				Conclusion: "failure",
				CreatedAt:  now.Add(time.Minute).Format(time.RFC3339),
				HTMLURL:    "https://github.com/test-org/.fullsend/actions/runs/1",
			},
		},
	}
	repos := []string{"repo-a"}
	layer, buf := newEnrollmentLayer(t, client, repos, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "conclusion: failure")
}

func TestEnrollmentLayer_Uninstall_Noop(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a"}, nil)

	err := layer.Uninstall(context.Background())
	require.NoError(t, err)

	assert.Empty(t, client.CreatedBranches)
	assert.Empty(t, client.CreatedFiles)
	assert.Empty(t, client.CreatedProposals)
	assert.Empty(t, client.DeletedRepos)
}

func TestEnrollmentLayer_Analyze_AllEnrolled(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/repo-a/.github/workflows/fullsend.yaml": []byte("shim"),
			"test-org/repo-b/.github/workflows/fullsend.yaml": []byte("shim"),
		},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, _ := newEnrollmentLayer(t, client, repos, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	assert.Len(t, report.Details, 2)
	joined := strings.Join(report.Details, " ")
	assert.Contains(t, joined, "repo-a")
	assert.Contains(t, joined, "repo-b")
	assert.Empty(t, report.WouldInstall)
	assert.Empty(t, report.WouldFix)
}

func TestEnrollmentLayer_Analyze_NoneEnrolled(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, _ := newEnrollmentLayer(t, client, repos, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusNotInstalled, report.Status)
	assert.Empty(t, report.Details)
	assert.Len(t, report.WouldInstall, 2)
	joined := strings.Join(report.WouldInstall, " ")
	assert.Contains(t, joined, "repo-a")
	assert.Contains(t, joined, "repo-b")
}

func TestEnrollmentLayer_Analyze_Partial(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/repo-a/.github/workflows/fullsend.yaml": []byte("shim"),
		},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, _ := newEnrollmentLayer(t, client, repos, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)

	require.Len(t, report.Details, 1)
	assert.Contains(t, report.Details[0], "repo-a")

	require.Len(t, report.WouldInstall, 1)
	assert.Contains(t, report.WouldInstall[0], "repo-b")
}

func TestEnrollmentLayer_Analyze_NoReposConfigured(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newEnrollmentLayer(t, client, nil, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	require.Len(t, report.Details, 1)
	assert.Equal(t, "no repositories configured", report.Details[0])
	assert.Empty(t, report.WouldInstall)
	assert.Empty(t, report.WouldFix)
}

func TestEnrollmentLayer_Analyze_DisabledWithStaleShim(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/repo-x/.github/workflows/fullsend.yaml": []byte("shim"),
		},
	}
	layer, _ := newEnrollmentLayer(t, client, nil, []string{"repo-x"})

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusDegraded, report.Status)
	require.Len(t, report.WouldFix, 1)
	assert.Contains(t, report.WouldFix[0], "removal PR for repo-x")
}

func TestEnrollmentLayer_Analyze_DisabledAlreadyClean(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{},
	}
	layer, _ := newEnrollmentLayer(t, client, nil, []string{"repo-x"})

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusInstalled, report.Status)
	assert.Empty(t, report.WouldFix)
}

func TestEnrollmentLayer_Analyze_MixedEnabledAndDisabled(t *testing.T) {
	client := &forge.FakeClient{
		FileContents: map[string][]byte{
			"test-org/repo-a/.github/workflows/fullsend.yaml": []byte("shim"),
			"test-org/repo-x/.github/workflows/fullsend.yaml": []byte("shim"),
		},
	}
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a"}, []string{"repo-x"})

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusDegraded, report.Status)
	assert.Contains(t, report.Details[0], "repo-a")
	require.Len(t, report.WouldFix, 1)
	assert.Contains(t, report.WouldFix[0], "removal PR for repo-x")
}

func TestEnrollmentLayer_Analyze_PerRepoGuardSkips(t *testing.T) {
	client := forge.NewFakeClient()
	client.VariableValues["test-org/repo-a/FULLSEND_PER_REPO_INSTALL"] = "true"
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a"}, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusInstalled, report.Status)
	require.Len(t, report.Details, 1)
	assert.Contains(t, report.Details[0], "per-repo install, skipped")
	assert.Empty(t, report.WouldInstall)
}

func TestEnrollmentLayer_Analyze_PerRepoGuardFalse(t *testing.T) {
	client := forge.NewFakeClient()
	client.VariableValues["test-org/repo-a/FULLSEND_PER_REPO_INSTALL"] = "false"
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a"}, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusNotInstalled, report.Status)
	require.Len(t, report.WouldInstall, 1)
	assert.Contains(t, report.WouldInstall[0], "repo-a")
}

func TestEnrollmentLayer_Analyze_MixedPerRepoAndOrg(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["test-org/repo-b/.github/workflows/fullsend.yaml"] = []byte("shim")
	client.VariableValues["test-org/repo-a/FULLSEND_PER_REPO_INSTALL"] = "true"
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a", "repo-b", "repo-c"}, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusDegraded, report.Status)
	// repo-a is per-repo, repo-b is enrolled, repo-c is not enrolled
	detailsJoined := strings.Join(report.Details, " | ")
	assert.Contains(t, detailsJoined, "repo-a (per-repo install, skipped)")
	assert.Contains(t, detailsJoined, "repo-b enrolled")
	require.Len(t, report.WouldInstall, 1)
	assert.Contains(t, report.WouldInstall[0], "repo-c")
}

func TestEnrollmentLayer_Analyze_DisabledWithPerRepoGuard(t *testing.T) {
	client := forge.NewFakeClient()
	client.FileContents["test-org/repo-x/.github/workflows/fullsend.yaml"] = []byte("shim")
	client.VariableValues["test-org/repo-x/FULLSEND_PER_REPO_INSTALL"] = "true"
	layer, _ := newEnrollmentLayer(t, client, nil, []string{"repo-x"})

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusInstalled, report.Status)
	require.Len(t, report.Details, 1)
	assert.Contains(t, report.Details[0], "per-repo install, skipped")
	assert.Empty(t, report.WouldFix)
}

func TestEnrollmentLayer_Analyze_PerRepoGuardCheckError(t *testing.T) {
	client := forge.NewFakeClient()
	client.Errors["GetRepoVariable"] = fmt.Errorf("permission denied")
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a"}, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, StatusDegraded, report.Status)
	// First detail is the all-failed warning, second is the per-repo detail.
	require.Len(t, report.Details, 2)
	assert.Contains(t, report.Details[0], "all 1 repos failed guard check")
	assert.Contains(t, report.Details[1], "guard check failed, skipped")
}
