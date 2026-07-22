package githubactions

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestSelectWorkflowRun_ReturnsFailedRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", Event: "issues", CreatedAt: "2026-01-01T01:00:00Z"},
	}

	got := selectWorkflowRun(runs, after, "issues")
	require.NotNil(t, got)
	assert.Equal(t, 1, got.ID)
	assert.Equal(t, "failure", got.Conclusion)
}

func TestSelectSuccessfulWorkflowRun_SkipsFailed(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Status: "completed", Conclusion: "failure", Event: "issues", CreatedAt: "2026-01-01T01:00:00Z"},
		{ID: 2, Status: "completed", Conclusion: "success", Event: "issues", CreatedAt: "2026-01-01T02:00:00Z"},
	}

	got := selectSuccessfulWorkflowRun(runs, after, "issues")
	require.NotNil(t, got)
	assert.Equal(t, 2, got.ID)
}

func TestExtractArtifactZip_RejectsCorruptZip(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	err := extractArtifactZip("artifact", []byte("not-a-zip"), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse artifact zip")
}

func TestExtractArtifactZip_RejectsSymlink(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	hdr := &zip.FileHeader{Name: "link", Method: zip.Store}
	hdr.SetMode(os.ModeSymlink | 0o755)
	_, err := zw.CreateHeader(hdr)
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err = extractArtifactZip("../escape", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")
}

func TestExtractArtifactZip_RejectsPathTraversal(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../escape.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("nope"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err = extractArtifactZip("artifact", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path traversal")
}

func TestExtractArtifactZip_SanitizesName(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("ok.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	require.NoError(t, extractArtifactZip("../../weird/name", buf.Bytes(), dest))
	_, err = os.Stat(filepath.Join(dest, "name", "ok.txt"))
	require.NoError(t, err)
}

func TestExtractArtifactZip_RejectsAggregateLimit(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	const chunk = 10 << 20 // per-file limit
	for i := 0; i < 11; i++ {
		w, err := zw.Create(fmt.Sprintf("part-%d.bin", i))
		require.NoError(t, err)
		_, err = w.Write(bytes.Repeat([]byte("x"), chunk))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	err := extractArtifactZip("artifact", buf.Bytes(), dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aggregate extraction limit")
}

func TestNewestRepositoryArtifactCreatedAt(t *testing.T) {
	t.Parallel()

	arts := []forge.RepositoryArtifact{
		{CreatedAt: "2026-01-01T00:00:00Z"},
		{CreatedAt: "2026-01-02T00:00:00Z"},
	}
	assert.Equal(t, "2026-01-02T00:00:00Z", newestRepositoryArtifactCreatedAt(arts))
}

func TestCountHarnessDispatches_ZeroArtifacts(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	// No artifacts at all.

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestCountHarnessDispatches_SingleMatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_MultipleMatches(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10},
			{ID: 2, Name: "fullsend-triage", CreatedAt: "2026-01-03T00:00:00Z", WorkflowRunID: 20},
			{ID: 3, Name: "fullsend-triage", CreatedAt: "2026-01-04T00:00:00Z", WorkflowRunID: 30},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 3, count)
}

func TestCountHarnessDispatches_FiltersBeforeTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10}, // before
			{ID: 2, Name: "fullsend-triage", CreatedAt: "2026-07-01T00:00:00Z", WorkflowRunID: 20}, // after
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_FiltersOtherAgents(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 10},
			{ID: 2, Name: "fullsend-review", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 20},
			{ID: 3, Name: "fullsend-code", CreatedAt: "2026-01-02T00:00:00Z", WorkflowRunID: 30},
		},
	}

	d := &Driver{Client: client}
	count, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestCountHarnessDispatches_APIError(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.Errors["ListRepositoryArtifacts"] = fmt.Errorf("API error")

	d := &Driver{Client: client}
	_, err := d.CountHarnessDispatches(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API error")
}

func TestWaitForHarnessAgent_FromRepositoryArtifact(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend-issue-ping",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 99, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := &Driver{Client: client}
	run, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "issue-ping", after)
	require.NoError(t, err)
	require.NotNil(t, run)
	assert.Equal(t, 99, run.ID)
}

func TestWaitForHarnessAgent_NonSuccessConclusion(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{
				ID:            10,
				Name:          "fullsend-triage",
				CreatedAt:     "2026-01-02T00:00:00Z",
				WorkflowRunID: 99,
			},
		},
	}
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/fullsend.yaml": {
			ID: 99, Status: "completed", Conclusion: "failure",
			CreatedAt: "2026-01-02T00:00:00Z", HTMLURL: "https://github.com/org/repo/actions/runs/99",
		},
	}

	d := &Driver{Client: client}
	_, err := d.WaitForHarnessAgent(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concluded with \"failure\"")
	assert.Contains(t, err.Error(), "run 99")
}

func TestWaitForHarnessAgent_ContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()

	d := &Driver{Client: client}
	_, err := d.WaitForHarnessAgent(ctx, "org", "repo", "triage", after)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestAssertNoWorkflow_NoRuns(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()

	d := &Driver{Client: client}
	err := d.AssertNoWorkflow(context.Background(), "org", "repo", "dispatch.yml", after)
	require.NoError(t, err)
}

func TestAssertNoWorkflow_UnexpectedRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/dispatch.yml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-02T00:00:00Z",
		},
	}

	d := &Driver{Client: client}
	err := d.AssertNoWorkflow(context.Background(), "org", "repo", "dispatch.yml", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected workflow run")
}

func TestAssertNoWorkflow_RunBeforeTrigger(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.WorkflowRuns = map[string]*forge.WorkflowRun{
		"org/repo/dispatch.yml": {
			ID: 10, Status: "completed", Conclusion: "success",
			CreatedAt: "2026-01-01T00:00:00Z", // before trigger
		},
	}

	d := &Driver{Client: client}
	err := d.AssertNoWorkflow(context.Background(), "org", "repo", "dispatch.yml", after)
	require.NoError(t, err)
}

func TestAssertNoHarnessAgentArtifact_NoMatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-other", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}

	d := &Driver{Client: client}
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
}

func TestAssertNoHarnessAgentArtifact_FoundMatch(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}

	d := &Driver{Client: client}
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected harness \"triage\" not to run")
	assert.Contains(t, err.Error(), "fullsend-triage")
}

func TestAssertNoHarnessAgentArtifact_ArtifactBeforeAfterTime(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.RepositoryArtifacts = map[string][]forge.RepositoryArtifact{
		"org/repo": {
			// Artifact created before the trigger time — should be ignored.
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z"},
		},
	}

	d := &Driver{Client: client}
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.NoError(t, err)
}

func TestAssertNoHarnessAgentArtifact_APIError(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	client := forge.NewFakeClient()
	client.Errors["ListRepositoryArtifacts"] = fmt.Errorf("API unavailable")

	d := &Driver{Client: client}
	err := d.AssertNoHarnessAgentArtifact(context.Background(), "org", "repo", "triage", after)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API unavailable")
}

func TestSelectCompletedSuccessRun(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("selects successful run", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectCompletedSuccessRun(runs, after)
		require.NotNil(t, got)
		assert.Equal(t, 1, got.ID)
	})

	t.Run("skips failed runs", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectCompletedSuccessRun(runs, after)
		assert.Nil(t, got)
	})

	t.Run("skips in-progress runs", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Status: "in_progress", Conclusion: "", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectCompletedSuccessRun(runs, after)
		assert.Nil(t, got)
	})

	t.Run("skips runs before trigger time", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Status: "completed", Conclusion: "success", CreatedAt: "2025-12-01T00:00:00Z"},
		}
		got := selectCompletedSuccessRun(runs, after)
		assert.Nil(t, got)
	})

	t.Run("picks newest run", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 5, Status: "completed", Conclusion: "success", CreatedAt: "2026-01-03T00:00:00Z"},
		}
		got := selectCompletedSuccessRun(runs, after)
		require.NotNil(t, got)
		assert.Equal(t, 5, got.ID)
	})
}

func TestSelectCompletedSuccessRunByName(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("selects matching name", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Name: "Triage Agent", Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 2, Name: "Other Workflow", Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectCompletedSuccessRunByName(runs, after, "Triage Agent")
		require.NotNil(t, got)
		assert.Equal(t, 1, got.ID)
	})

	t.Run("returns nil when name not found", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Name: "Other Workflow", Status: "completed", Conclusion: "success", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectCompletedSuccessRunByName(runs, after, "Triage Agent")
		assert.Nil(t, got)
	})

	t.Run("skips non-success", func(t *testing.T) {
		runs := []forge.WorkflowRun{
			{ID: 1, Name: "Triage Agent", Status: "completed", Conclusion: "failure", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectCompletedSuccessRunByName(runs, after, "Triage Agent")
		assert.Nil(t, got)
	})
}

func TestWorkflowRunMatches(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("matches when after and event match", func(t *testing.T) {
		run := forge.WorkflowRun{CreatedAt: "2026-01-02T00:00:00Z", Event: "issues"}
		assert.True(t, workflowRunMatches(run, after, "issues"))
	})

	t.Run("matches when no event filter", func(t *testing.T) {
		run := forge.WorkflowRun{CreatedAt: "2026-01-02T00:00:00Z", Event: "push"}
		assert.True(t, workflowRunMatches(run, after, ""))
	})

	t.Run("rejects before trigger time", func(t *testing.T) {
		run := forge.WorkflowRun{CreatedAt: "2025-12-01T00:00:00Z", Event: "issues"}
		assert.False(t, workflowRunMatches(run, after, "issues"))
	})

	t.Run("rejects wrong event", func(t *testing.T) {
		run := forge.WorkflowRun{CreatedAt: "2026-01-02T00:00:00Z", Event: "push"}
		assert.False(t, workflowRunMatches(run, after, "issues"))
	})

	t.Run("rejects bad timestamp", func(t *testing.T) {
		run := forge.WorkflowRun{CreatedAt: "not-a-date", Event: "issues"}
		assert.False(t, workflowRunMatches(run, after, "issues"))
	})
}

func TestSelectRepositoryArtifactAfter(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("selects matching artifact", func(t *testing.T) {
		arts := []forge.RepositoryArtifact{
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectRepositoryArtifactAfter(arts, "fullsend-triage", after)
		require.NotNil(t, got)
		assert.Equal(t, 1, got.ID)
	})

	t.Run("skips wrong name", func(t *testing.T) {
		arts := []forge.RepositoryArtifact{
			{ID: 1, Name: "fullsend-other", CreatedAt: "2026-01-02T00:00:00Z"},
		}
		got := selectRepositoryArtifactAfter(arts, "fullsend-triage", after)
		assert.Nil(t, got)
	})

	t.Run("skips before trigger time", func(t *testing.T) {
		arts := []forge.RepositoryArtifact{
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2025-12-01T00:00:00Z"},
		}
		got := selectRepositoryArtifactAfter(arts, "fullsend-triage", after)
		assert.Nil(t, got)
	})

	t.Run("picks newest artifact", func(t *testing.T) {
		arts := []forge.RepositoryArtifact{
			{ID: 1, Name: "fullsend-triage", CreatedAt: "2026-01-02T00:00:00Z"},
			{ID: 5, Name: "fullsend-triage", CreatedAt: "2026-01-03T00:00:00Z"},
		}
		got := selectRepositoryArtifactAfter(arts, "fullsend-triage", after)
		require.NotNil(t, got)
		assert.Equal(t, 5, got.ID)
	})
}

func TestExtractArtifactZip_CreatesSubdirectories(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("subdir/file.txt")
	require.NoError(t, err)
	_, err = w.Write([]byte("nested content"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	dest := t.TempDir()
	require.NoError(t, extractArtifactZip("test-art", buf.Bytes(), dest))
	data, err := os.ReadFile(filepath.Join(dest, "test-art", "subdir", "file.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nested content", string(data))
}

func TestReadLimited_ExceedsLimit(t *testing.T) {
	t.Parallel()

	r := bytes.NewReader(bytes.Repeat([]byte("x"), 100))
	_, err := readLimited(r, 50)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
}

func TestReadLimited_WithinLimit(t *testing.T) {
	t.Parallel()

	r := bytes.NewReader([]byte("hello"))
	data, err := readLimited(r, 100)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}
