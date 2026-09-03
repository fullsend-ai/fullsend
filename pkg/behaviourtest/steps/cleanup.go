package steps

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// CleanupScenario deletes all repos created during the scenario: the
// primary test repo, fork, and URL harness host. When KeepRepos returns
// true, no repos are deleted. Successfully deleted repos are removed
// from the driver's tracking list via MarkDeleted so Finalize does not
// re-attempt them.
func CleanupScenario(w *world.World) error {
	if install.KeepRepos() {
		return nil
	}

	ctx := context.Background()
	var firstErr error

	record := func(err error, what string) {
		if err != nil && !forge.IsNotFound(err) {
			worldLogf(w, "behaviour cleanup: %s: %v", what, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", what, err)
			}
		}
	}

	if w.RepoOwner != "" && w.RepoName != "" {
		err := w.SCM.DeleteRepo(ctx, w.RepoOwner, w.RepoName)
		if err == nil || forge.IsNotFound(err) {
			if w.Driver != nil {
				w.Driver.MarkDeleted(w.RepoName)
			}
		}
		record(err, fmt.Sprintf("delete repo %s/%s", w.RepoOwner, w.RepoName))
	}

	if w.ForkOwner != "" && w.ForkRepo != "" && w.ForkRepo != w.RepoName {
		err := w.SCM.DeleteRepo(ctx, w.ForkOwner, w.ForkRepo)
		record(err, fmt.Sprintf("delete fork repo %s/%s", w.ForkOwner, w.ForkRepo))
	}

	if w.URLHarnessRepoOwner != "" && w.URLHarnessRepoName != "" && w.URLHarnessRepoName != w.RepoName {
		err := w.SCM.DeleteRepo(ctx, w.URLHarnessRepoOwner, w.URLHarnessRepoName)
		record(err, fmt.Sprintf("delete harness-hosting repo %s/%s", w.URLHarnessRepoOwner, w.URLHarnessRepoName))
	}

	return firstErr
}

func worldLogf(w *world.World, format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}
