//go:build behaviour

package behaviour_test

import (
	"os"
	"strings"
	"testing"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest"
)

func TestBehaviourSuite(t *testing.T) {
	behaviourtest.RunSuite(t, behaviourtest.SuiteOptions{
		FeaturePaths: featurePaths(),
		FixturesRoot: "e2e/behaviour",
	})
}

// featurePaths is every feature, or the space-separated godog paths in
// BEHAVIOUR_FEATURES (relative to this directory, e.g.
// "features/triage/triage.feature" or "features/dispatch"), so one feature
// can run without a tag on it.
func featurePaths() []string {
	if paths := strings.Fields(os.Getenv("BEHAVIOUR_FEATURES")); len(paths) > 0 {
		return paths
	}
	return []string{"features"}
}
