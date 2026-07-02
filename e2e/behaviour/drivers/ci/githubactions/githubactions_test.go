package githubactions

import (
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestSelectWorkflowRun_FiltersByEvent(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 7, 1, 11, 33, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 1, Event: "issues", Status: "completed", Conclusion: "success", CreatedAt: "2026-07-01T11:33:09Z"},
		{ID: 2, Event: "issue_comment", Status: "completed", Conclusion: "skipped", CreatedAt: "2026-07-01T11:33:09Z"},
		{ID: 3, Event: "issue_comment", Status: "in_progress", Conclusion: "", CreatedAt: "2026-07-01T11:33:10Z"},
	}

	got := selectWorkflowRun(runs, after, "issue_comment")
	if got == nil || got.ID != 3 {
		t.Fatalf("selectWorkflowRun() = %#v, want issue_comment run 3", got)
	}

	got = selectWorkflowRun(runs, after, "issues")
	if got == nil || got.ID != 1 {
		t.Fatalf("selectWorkflowRun(issues) = %#v, want run 1", got)
	}
}

func TestSelectWorkflowRun_IgnoresFailedBeforeSuccess(t *testing.T) {
	t.Parallel()
	after := time.Date(2026, 7, 1, 11, 33, 0, 0, time.UTC)
	runs := []forge.WorkflowRun{
		{ID: 10, Event: "issue_comment", Status: "completed", Conclusion: "skipped", CreatedAt: "2026-07-01T11:33:09Z"},
	}

	if got := selectWorkflowRun(runs, after, "issue_comment"); got != nil {
		t.Fatalf("selectWorkflowRun() = %#v, want nil for skipped-only runs", got)
	}
}
