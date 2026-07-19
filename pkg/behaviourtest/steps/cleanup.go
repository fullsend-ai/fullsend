package steps

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const githubAPIBaseURL = "https://api.github.com"

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
	if w.ForkPRBranch != "" && w.ForkOwner != "" && w.ForkRepo != "" && w.Token != "" {
		// Delete the test branch on the fork repo. The fork repo itself is
		// long-lived and must not be deleted per-scenario; only per-scenario
		// branches are removed. forge.Client does not expose DeleteBranch,
		// so we use the GitHub REST API directly (same pattern as
		// pkg/e2etest/cleanup.go).
		deleteForkBranch(ctx, githubAPIBaseURL, w.Token, w.ForkOwner, w.ForkRepo, w.ForkPRBranch, w)
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

// deleteForkBranch deletes a branch from a fork repo using the GitHub API
// directly (forge.Client doesn't have DeleteBranch). Idempotent: 404 is
// ignored. Errors are logged but do not fail the cleanup. baseURL is the
// GitHub API root (e.g. githubAPIBaseURL); it is a parameter so tests can
// substitute an httptest server.
func deleteForkBranch(ctx context.Context, baseURL, token, owner, repo, branch string, w *world.World) {
	ref := fmt.Sprintf("heads/%s", branch)
	url := fmt.Sprintf("%s/repos/%s/%s/git/refs/%s", baseURL, owner, repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		worldLogf(w, "behaviour cleanup: create branch delete request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		worldLogf(w, "behaviour cleanup: delete fork branch %s: %v", branch, err)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusNoContent:
		worldLogf(w, "behaviour cleanup: deleted fork branch %s on %s/%s", branch, owner, repo)
	case http.StatusNotFound:
		// Branch doesn't exist; nothing to do.
	default:
		worldLogf(w, "behaviour cleanup: unexpected status %d deleting fork branch %s", resp.StatusCode, branch)
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
