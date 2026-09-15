//go:build behaviour

package behaviour_test

import (
	"testing"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest"
)

func TestBehaviourSuite(t *testing.T) {
	behaviourtest.RunSuite(t, behaviourtest.SuiteOptions{
		FeaturePaths: []string{"features"},
		FixturesRoot: "e2e/behaviour",
	})
}
