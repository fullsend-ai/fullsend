package steps

import (
	"context"
	"os"
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
	if w.ArtifactDir != "" {
		ciArtifactDir := strings.TrimSpace(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"))
		if ciArtifactDir == "" || w.ArtifactDir != ciArtifactDir {
			if err := os.RemoveAll(w.ArtifactDir); err != nil {
				worldLogf(w, "behaviour cleanup: remove artifact dir: %v", err)
			}
		}
	}
	if len(w.DummyOps) > 0 {
		empty := []byte("ops: []\n")
		if err := w.SCM.CommitFile(ctx, w.Install.ConfigOwner(), w.Install.ConfigRepo(), w.BehaviourScriptPath(), "behaviour: clear dummy agent script", empty); err != nil {
			worldLogf(w, "behaviour cleanup: clear dummy script: %v", err)
		}
	}
}

func worldLogf(w *world.World, format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}
