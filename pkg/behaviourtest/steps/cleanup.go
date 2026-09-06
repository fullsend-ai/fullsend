package steps

import (
	"os"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// CleanupScenario releases per-scenario resources that are not covered
// by the driver's Finalize. Repos are retained across runs to boost
// the GitHub App installation rate limit (12,500/hr at 170+ repos);
// old runs are pruned by composedDriver.pruneOldRuns in Finalize.
func CleanupScenario(w *world.World) error {
	if w.JiraMockServer != nil {
		w.JiraMockServer.Close()
	}
	if w.JiraConfigDir != "" {
		os.RemoveAll(w.JiraConfigDir)
	}
	return nil
}
