package steps

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/artifacts"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const issueOpenEvent = "issues"

func triageWorkflowEvent(w *world.World) string {
	if w.TriageTriggerEvent != "" {
		return w.TriageTriggerEvent
	}
	return issueOpenEvent
}

func ensureTriageWorkflowComplete(w *world.World) error {
	if w.WorkflowRun != nil {
		return nil
	}
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time: create an issue and label it first")
	}
	ctx := context.Background()
	run, err := w.CI.WaitForWorkflow(ctx, w.Org, w.RepoName, install.PerRepoTriageWorkflow, w.ScenarioStart, triageWorkflowEvent(w))
	if err != nil {
		return err
	}
	w.WorkflowRun = run
	return nil
}

// ensureArtifacts downloads the agent artifact of a dummy-runtime run,
// recognised by behaviour-results.json.
func ensureArtifacts(w *world.World) error {
	return ensureRunArtifacts(w, "behaviour-results.json")
}

// ensureRunArtifacts downloads the agent artifact for the scenario's
// workflow run into w.ArtifactDir. marker names a file that must be
// present for the download to count (behaviour-results.json for the
// dummy runtime; metrics.json for any runtime), so a partial or wrong
// artifact is retried rather than accepted.
func ensureRunArtifacts(w *world.World, marker string) error {
	if w.ArtifactDir != "" {
		return nil
	}
	if err := ensureTriageWorkflowComplete(w); err != nil {
		return err
	}
	if w.ArtifactDir != "" {
		return nil
	}
	ctx := context.Background()
	dest, err := prepareArtifactDir()
	if err != nil {
		return err
	}
	// The agent workflow uploads `fullsend-<agent>`; follow the harness
	// this scenario dispatched rather than assuming the triage stage.
	artifactName := install.PerRepoAgentArtifact
	if w.DispatchAgent != "" {
		artifactName = "fullsend-" + w.DispatchAgent
	}

	tryDownloadRun := func(runID int) error {
		if err := w.CI.DownloadNamedArtifactFromRun(ctx, w.Org, w.RepoName, runID, artifactName, dest); err != nil {
			return err
		}
		if _, findErr := artifacts.FindOutputFile(dest, marker); findErr != nil {
			return findErr
		}
		return nil
	}

	resetDest := func() error {
		_ = os.RemoveAll(dest)
		dest, err = prepareArtifactDir()
		return err
	}

	if w.WorkflowRun != nil {
		if err := tryDownloadRun(w.WorkflowRun.ID); err == nil {
			w.ArtifactDir = dest
			return nil
		}
		if err := resetDest(); err != nil {
			return err
		}
	}

	// Reusable triage uploads artifacts on the nested agent workflow run, not the shim.
	if agentRun, err := w.CI.FindCompletedWorkflowRun(ctx, w.Org, w.RepoName, install.PerRepoAgentWorkflow, w.ScenarioStart); err == nil && agentRun != nil {
		if err := tryDownloadRun(agentRun.ID); err == nil {
			w.ArtifactDir = dest
			return nil
		}
		if err := resetDest(); err != nil {
			return err
		}
	}

	if err := w.CI.DownloadNamedArtifactAfter(ctx, w.Org, w.RepoName, artifactName, w.ScenarioStart, dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	if _, err := artifacts.FindOutputFile(dest, marker); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	w.ArtifactDir = dest
	return nil
}

func prepareArtifactDir() (string, error) {
	ciArtifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
	if ciArtifactDir != "" {
		if err := os.MkdirAll(ciArtifactDir, 0o755); err != nil {
			return "", fmt.Errorf("creating behaviour artifact dir: %w", err)
		}
		sub, err := os.MkdirTemp(ciArtifactDir, "run-*")
		if err != nil {
			return "", fmt.Errorf("creating behaviour artifact subdir: %w", err)
		}
		return sub, nil
	}
	return os.MkdirTemp("", "behaviour-artifacts-*")
}
