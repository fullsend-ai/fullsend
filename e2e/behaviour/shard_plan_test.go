//go:build behaviourplan

package behaviour_test

import (
	"testing"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest"
)

// TestBehaviourShardPlan prints the DEV CI shard plan for the behaviour
// suite and writes it to BEHAVIOUR_SHARD_PLAN_FILE when set. It must
// use the same SuiteOptions as TestBehaviourSuite.
func TestBehaviourShardPlan(t *testing.T) {
	path := behaviourtest.ShardPlanFileFromEnv()
	plan, err := behaviourtest.WriteSuiteShardPlan(behaviourtest.SuiteOptions{
		FeaturePaths: []string{"features"},
		FixturesRoot: "e2e/behaviour",
	}, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("behaviour shard plan: %d shard(s) for %d selected scenario instance(s), %d skipped by compatibility tags, %d provisioning (max %d per shard)",
		plan.Total, plan.Selected, plan.Skipped, plan.Provisioning, plan.MaxProvisioningPerShard)
	for _, s := range plan.PerShard {
		t.Logf("  shard %d/%d: %d instance(s), %d provisioning, %d other", s.Index+1, plan.Total, s.Instances, s.Provisioning, s.Other)
	}
}
