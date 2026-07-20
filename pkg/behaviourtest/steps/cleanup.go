package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func CleanupScenario(w *world.World) {
	ctx := context.Background()

	// --- Issue / PR cleanup ---
	if w.IssueNumber > 0 {
		if err := w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, w.IssueNumber); err != nil {
			worldLogf(w, "behaviour cleanup: close issue #%d: %v", w.IssueNumber, err)
		}
	}
	if w.ForkPRNumber > 0 {
		// Fork PRs are opened against the base repo, so close on base repo.
		if err := w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, w.ForkPRNumber); err != nil {
			worldLogf(w, "behaviour cleanup: close fork PR #%d: %v", w.ForkPRNumber, err)
		}
	}

	// --- Fork branch cleanup ---
	if w.ForkPRBranch != "" && w.ForkOwner != "" && w.ForkRepo != "" {
		// Delete the test branch on the fork repo. The fork repo itself is
		// long-lived and must not be deleted per-scenario; only per-scenario
		// branches are removed.
		if err := w.SCM.DeleteBranch(ctx, w.ForkOwner, w.ForkRepo, w.ForkPRBranch); err != nil {
			if !forge.IsNotFound(err) {
				worldLogf(w, "behaviour cleanup: delete fork branch %s: %v", w.ForkPRBranch, err)
			}
		}
	}

	// --- Artifact cleanup ---
	if w.ArtifactDir != "" && shouldRemoveArtifactDir(w.ArtifactDir, os.Getenv("BEHAVIOUR_ARTIFACT_DIR")) {
		if err := os.RemoveAll(w.ArtifactDir); err != nil {
			worldLogf(w, "behaviour cleanup: remove artifact dir: %v", err)
		}
	}

	// --- Dummy script cleanup ---
	if len(w.DummyOps) > 0 {
		empty := []byte("ops: []\n")
		if err := w.SCM.CommitFile(ctx, w.Install.ConfigOwner(), w.Install.ConfigRepo(), w.BehaviourScriptPath(), "behaviour: clear dummy agent script", empty); err != nil {
			worldLogf(w, "behaviour cleanup: clear dummy script: %v", err)
		}
	}
}

// shouldRemoveArtifactDir reports whether cleanup may delete artifactDir.
// Dirs under BEHAVIOUR_ARTIFACT_DIR are preserved for CI upload-artifact.
func shouldRemoveArtifactDir(artifactDir, ciArtifactDir string) bool {
	ciArtifactDir = strings.TrimSpace(ciArtifactDir)
	if ciArtifactDir == "" {
		return true
	}
	return !artifactDirUnderCIRoot(artifactDir, ciArtifactDir)
}

func artifactDirUnderCIRoot(dir, ciRoot string) bool {
	cleanDir := filepath.Clean(dir)
	cleanRoot := filepath.Clean(ciRoot)
	if cleanDir == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator))
}

func worldLogf(w *world.World, format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}
