//go:build playback

package behaviour_test

import (
	"testing"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest"
)

func TestPlaybackSuite(t *testing.T) {
	behaviourtest.RunPlaybackSuite(t, behaviourtest.SuiteOptions{
		FeaturePaths: []string{"features"},
		FixturesRoot: "e2e/behaviour",
	})
}
