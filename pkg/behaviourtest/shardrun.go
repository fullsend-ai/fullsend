package behaviourtest

import (
	"context"
	"errors"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
)

// runShard is the part of the selected scenarios one RunSuite executes.
type runShard struct {
	// Sharded is false when the run executes every selected scenario.
	Sharded bool
	// Matrix is true when the run is one job of a CI shard matrix, even
	// when it executes every selected scenario (STAGE's single shard);
	// Spec then names that job for the metrics file.
	Matrix bool
	Spec   shardSpec
	// Planned is the scenario instances this run executes: its shard's
	// share when Sharded, every selected instance otherwise.
	Planned plannedShard
	// Gate admits only this shard's scenarios; nil when not sharded.
	Gate *shardGate
	// Demand counts the instances this run executes, which size its
	// org acquisition.
	Demand suiteDemand
}

// resolveRunShard decides which selected instances this run executes,
// from BEHAVIOUR_SHARD_INDEX/BEHAVIOUR_SHARD_TOTAL. Unsharded runs (no
// shard env, or STAGE with a single shard) execute everything. STAGE
// refuses more than one shard: its pool is one org, so its behaviour is
// unchanged by CI sharding.
func resolveRunShard(cfg env.RunnerConfig, instances []scenarioInstance, skip func(tags []string) error) (runShard, error) {
	spec, ok, err := shardSpecFromEnv()
	if err != nil {
		return runShard{}, err
	}
	matrix := ok
	if ok && cfg.Environment == "stage" {
		if spec.Total > 1 {
			return runShard{}, fmt.Errorf("STAGE runs in its single org and cannot be split across shards (%s=%d)", envShardTotal, spec.Total)
		}
		ok = false
	}
	if !ok {
		demand := summarizeDemand(instances, skip)
		r := runShard{
			Matrix: matrix,
			Planned: plannedShard{
				Instances:    instances,
				Provisioning: demand.Provisioning,
				Other:        demand.Selected - demand.Provisioning,
			},
			Demand: demand,
		}
		if matrix {
			r.Spec = spec
		}
		return r, nil
	}
	planned, gate, err := selectShard(spec, instances, skip)
	if err != nil {
		return runShard{}, err
	}
	return runShard{
		Sharded: true,
		Matrix:  true,
		Spec:    spec,
		Planned: planned,
		Gate:    gate,
		Demand:  summarizeDemand(planned.Instances, skip),
	}, nil
}

// scenarioGate returns the suite gate, nil when the run is unsharded.
func (r runShard) scenarioGate() func(*godog.Scenario) error {
	if r.Gate == nil {
		return nil
	}
	return r.Gate.Gate
}

// verify checks after the suite body that a sharded run started exactly
// its planned scenarios.
func (r runShard) verify() error {
	if r.Gate == nil {
		return nil
	}
	return r.Gate.Verify()
}

// shardConcurrency resolves godog's worker count. An explicit
// GODOG_CONCURRENCY (raw) wins; otherwise a CI shard runs at most
// defaultShardConcurrency scenarios at once, and an unsharded run up to
// its orgs' combined driver capacity.
func shardConcurrency(raw string, capacity int, sharded bool) (n int, overCapacity bool, err error) {
	n, overCapacity, err = resolveConcurrency(raw, capacity)
	if err != nil || raw != "" || !sharded {
		return n, overCapacity, err
	}
	return min(n, defaultShardConcurrency), false, nil
}

// registerMetricsHooks records, for metrics, when the first provisioning
// step completes and which scenarios failed on a rate limit.
func registerMetricsHooks(sc *godog.ScenarioContext, metrics *suiteMetrics) {
	sc.StepContext().After(func(ctx context.Context, st *godog.Step, status godog.StepResultStatus, err error) (context.Context, error) {
		if err == nil && status == godog.StepPassed && st != nil && provisioningStepRe.MatchString(st.Text) {
			metrics.markInstallDone()
		}
		return ctx, err
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		if err != nil && !errors.Is(err, godog.ErrSkip) {
			metrics.recordScenarioError(err)
		}
		return ctx, err
	})
}
