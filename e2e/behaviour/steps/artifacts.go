package steps

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/artifacts"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/world"
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
	run, err := w.CI.WaitForWorkflow(ctx, w.Org, w.Install.TriageWorkflowRepo(), w.Install.TriageWorkflowFile(), w.ScenarioStart, triageWorkflowEvent(w))
	if err != nil {
		return err
	}
	w.WorkflowRun = run
	return nil
}

func ensureArtifacts(w *world.World) error {
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

	tryDownloadRun := func(runID int) error {
		if err := w.CI.DownloadArtifacts(ctx, w.Org, w.Install.TriageWorkflowRepo(), runID, dest); err != nil {
			return err
		}
		if _, findErr := artifacts.FindBehaviourResults(dest); findErr != nil {
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
	if agentRun, err := w.CI.FindCompletedWorkflowRun(ctx, w.Org, w.Install.TriageWorkflowRepo(), w.Install.AgentWorkflowFile(), w.ScenarioStart); err == nil && agentRun != nil {
		if err := tryDownloadRun(agentRun.ID); err == nil {
			w.ArtifactDir = dest
			return nil
		}
		if err := resetDest(); err != nil {
			return err
		}
	}

	if err := w.CI.DownloadNamedArtifactAfter(ctx, w.Org, w.Install.TriageWorkflowRepo(), w.Install.AgentArtifactName(), w.ScenarioStart, dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	if _, err := artifacts.FindBehaviourResults(dest); err != nil {
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
