package layers

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

func newEnrollmentLayer(t *testing.T, client forge.Client, repos []string) (*EnrollmentLayer, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	printer := ui.New(&buf)
	layer := NewEnrollmentLayer("test-org", client, repos, printer)
	return layer, &buf
}

func TestEnrollmentLayer_Name(t *testing.T) {
	layer, _ := newEnrollmentLayer(t, &forge.FakeClient{}, nil)
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
	layer, buf := newEnrollmentLayer(t, client, repos)

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
				{Title: "Connect to fullsend agent pipeline", URL: "https://github.com/test-org/repo-a/pull/1"},
			},
		},
	}
	repos := []string{"repo-a", "repo-b"}
	layer, buf := newEnrollmentLayer(t, client, repos)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "repo-a/pull/1")
}

func TestEnrollmentLayer_Install_NoRepos(t *testing.T) {
	client := &forge.FakeClient{}
	layer, buf := newEnrollmentLayer(t, client, nil)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "no repositories to enroll")
}

func TestEnrollmentLayer_Install_DispatchError(t *testing.T) {
	client := &forge.FakeClient{
		Errors: map[string]error{
			"DispatchWorkflow": assert.AnError,
		},
	}
	repos := []string{"repo-a"}
	layer, _ := newEnrollmentLayer(t, client, repos)

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
	layer, buf := newEnrollmentLayer(t, client, repos)

	err := layer.Install(context.Background())
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "conclusion: failure")
}

func TestEnrollmentLayer_Uninstall_Noop(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newEnrollmentLayer(t, client, []string{"repo-a"})

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
	layer, _ := newEnrollmentLayer(t, client, repos)

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
	layer, _ := newEnrollmentLayer(t, client, repos)

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
	layer, _ := newEnrollmentLayer(t, client, repos)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusDegraded, report.Status)

	require.Len(t, report.Details, 1)
	assert.Contains(t, report.Details[0], "repo-a")

	require.Len(t, report.WouldFix, 1)
	assert.Contains(t, report.WouldFix[0], "repo-b")
}

func TestEnrollmentLayer_Analyze_NoReposEnabled(t *testing.T) {
	client := &forge.FakeClient{}
	layer, _ := newEnrollmentLayer(t, client, nil)

	report, err := layer.Analyze(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "enrollment", report.Name)
	assert.Equal(t, StatusInstalled, report.Status)
	require.Len(t, report.Details, 1)
	assert.Equal(t, "no repositories enrolled", report.Details[0])
	assert.Empty(t, report.WouldInstall)
	assert.Empty(t, report.WouldFix)
}

func TestShimTemplateMatchesTargetRepoScaffold(t *testing.T) {
	templateContent, err := scaffold.FullsendRepoFile("templates/shim-workflow.yaml")
	require.NoError(t, err)

	targetContent, err := scaffold.TargetRepoFile(".github/workflows/fullsend.yaml")
	require.NoError(t, err)

	assert.Equal(t, string(targetContent), string(templateContent),
		"templates/shim-workflow.yaml must match target-repo scaffold shim")
}
