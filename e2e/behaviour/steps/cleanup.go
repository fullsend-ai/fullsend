package steps

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/world"
)

func CleanupScenario(w *world.World) {
	ctx := context.Background()
	if w.IssueNumber > 0 {
		if err := w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, w.IssueNumber); err != nil {
			worldLogf(w, "behaviour cleanup: close issue #%d: %v", w.IssueNumber, err)
		}
	}
	if w.ArtifactDir != "" && shouldRemoveArtifactDir(w.ArtifactDir, os.Getenv("BEHAVIOUR_ARTIFACT_DIR")) {
		if err := os.RemoveAll(w.ArtifactDir); err != nil {
			worldLogf(w, "behaviour cleanup: remove artifact dir: %v", err)
		}
	}
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
