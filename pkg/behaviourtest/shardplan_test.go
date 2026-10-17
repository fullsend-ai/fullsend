package behaviourtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func noSkip([]string) error { return nil }

// syntheticInstances returns prov provisioning and other
// non-provisioning instances with distinct keys.
func syntheticInstances(prov, other int) []scenarioInstance {
	var out []scenarioInstance
	for i := range prov {
		out = append(out, scenarioInstance{URI: "p.feature", Name: fmt.Sprintf("prov %d", i), Provisions: true, Key: fmt.Sprintf("p%d", i)})
	}
	for i := range other {
		out = append(out, scenarioInstance{URI: "o.feature", Name: fmt.Sprintf("other %d", i), Key: fmt.Sprintf("o%d", i)})
	}
	return out
}

// assertExactlyOnce checks every instance is assigned to exactly one
// shard and no shard exceeds the provisioning cap.
func assertExactlyOnce(t *testing.T, instances []scenarioInstance, shards []plannedShard, perShardCap int) {
	t.Helper()
	want := map[string]int{}
	for _, inst := range instances {
		want[inst.Key]++
	}
	got := map[string]int{}
	for i, s := range shards {
		assert.Equal(t, i, s.Index, "shard indexes are 0..n-1 in order")
		assert.LessOrEqual(t, s.Provisioning, perShardCap, "shard %d provisioning", i)
		assert.Equal(t, len(s.Instances), s.Provisioning+s.Other, "shard %d counts", i)
		for _, inst := range s.Instances {
			got[inst.Key]++
		}
	}
	assert.Equal(t, want, got, "every selected instance assigned exactly once")
}

func TestPlanShards_ShardCountGrowsWithProvisioning(t *testing.T) {
	for _, tc := range []struct {
		prov, other int
		wantShards  int
	}{
		{0, 0, 1},  // nothing selected: still one shard, so the matrix is never empty
		{0, 5, 1},  // non-provisioning only: run exactly once
		{1, 0, 1},  // single provisioning instance
		{20, 3, 1}, // exactly at the cap
		{21, 3, 2},
		{40, 0, 2},
		{41, 0, 3},
		{49, 17, 3}, // issue example
		{60, 0, 3},
		{61, 9, 4}, // issue example
	} {
		t.Run(fmt.Sprintf("%dp_%do", tc.prov, tc.other), func(t *testing.T) {
			instances := syntheticInstances(tc.prov, tc.other)
			shards, err := planShards(instances, noSkip, maxProvisioningPerShard)
			require.NoError(t, err)
			assert.Len(t, shards, tc.wantShards)
			assertExactlyOnce(t, instances, shards, maxProvisioningPerShard)

			// Provisioning work is balanced: shards differ by at most one.
			lo, hi := shards[0].Provisioning, shards[0].Provisioning
			for _, s := range shards {
				lo, hi = min(lo, s.Provisioning), max(hi, s.Provisioning)
			}
			assert.LessOrEqual(t, hi-lo, 1)
		})
	}
}

func TestPlanShards_SkippedProvisioningCountsAsOther(t *testing.T) {
	instances := syntheticInstances(25, 0)
	for i := range instances[:10] {
		instances[i].Tags = []string{"@skip:gitlab"}
	}
	skip := func(tags []string) error {
		if len(tags) > 0 {
			return godog.ErrSkip
		}
		return nil
	}
	shards, err := planShards(instances, skip, maxProvisioningPerShard)
	require.NoError(t, err)
	require.Len(t, shards, 1, "15 runnable provisioning instances fit one shard")
	assert.Equal(t, 15, shards[0].Provisioning)
	assert.Equal(t, 10, shards[0].Other)
}

func TestPlanShards_SameKeyInstancesShareAShard(t *testing.T) {
	instances := syntheticInstances(5, 2)
	dup := scenarioInstance{URI: "d.feature", Name: "dup", Provisions: true, Key: "dup"}
	instances = append(instances, dup, dup, dup)
	shards, err := planShards(instances, noSkip, 3)
	require.NoError(t, err)
	assertExactlyOnce(t, instances, shards, 3)
	owners := map[int]bool{}
	for _, s := range shards {
		for _, inst := range s.Instances {
			if inst.Key == "dup" {
				owners[s.Index] = true
			}
		}
	}
	assert.Len(t, owners, 1, "instances with the same source key share a shard")
}

func TestPlanShards_Errors(t *testing.T) {
	_, err := planShards(nil, noSkip, 0)
	require.Error(t, err)

	dup := scenarioInstance{URI: "d.feature", Name: "dup", Provisions: true, Key: "dup"}
	_, err = planShards([]scenarioInstance{dup, dup, dup}, noSkip, 2)
	require.ErrorContains(t, err, "exceed the per-shard cap")
}

func TestPlanShards_Deterministic(t *testing.T) {
	instances := syntheticInstances(49, 17)
	a, err := planShards(instances, noSkip, maxProvisioningPerShard)
	require.NoError(t, err)
	b, err := planShards(instances, noSkip, maxProvisioningPerShard)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestPickleKey(t *testing.T) {
	base := func() *messages.Pickle {
		return &messages.Pickle{
			Uri:        "features/a.feature",
			Name:       "scenario",
			AstNodeIds: []string{"10", "11"},
			Tags:       []*messages.PickleTag{{Name: "@smoke"}},
			Steps: []*messages.PickleStep{
				{Text: "the enrolled test repository"},
				{Text: "a doc", Argument: &messages.PickleStepArgument{DocString: &messages.PickleDocString{MediaType: "json", Content: "{}"}}},
				{Text: "a table", Argument: &messages.PickleStepArgument{DataTable: &messages.PickleTable{Rows: []*messages.PickleTableRow{{Cells: []*messages.PickleTableCell{{Value: "a"}, {Value: "b"}}}}}}},
				nil,
			},
		}
	}
	key := pickleKey(base())
	assert.Equal(t, key, pickleKey(base()), "stable")
	assert.Len(t, key, 64)

	for name, mutate := range map[string]func(p *messages.Pickle){
		"uri":       func(p *messages.Pickle) { p.Uri = "features/b.feature" },
		"name":      func(p *messages.Pickle) { p.Name = "other" },
		"source":    func(p *messages.Pickle) { p.AstNodeIds[1] = "12" },
		"tag":       func(p *messages.Pickle) { p.Tags[0].Name = "@wip" },
		"step":      func(p *messages.Pickle) { p.Steps[0].Text = "x" },
		"docstring": func(p *messages.Pickle) { p.Steps[1].Argument.DocString.Content = "[]" },
		"cell":      func(p *messages.Pickle) { p.Steps[2].Argument.DataTable.Rows[0].Cells[1].Value = "c" },
		"boundary": func(p *messages.Pickle) {
			// Length-prefixing keeps "ab"+"c" distinct from "a"+"bc".
			p.Steps[2].Argument.DataTable.Rows[0].Cells[0].Value = "ab"
			p.Steps[2].Argument.DataTable.Rows[0].Cells[1].Value = ""
		},
	} {
		p := base()
		mutate(p)
		assert.NotEqual(t, key, pickleKey(p), name)
	}
}

func TestShardSpecFromEnv(t *testing.T) {
	for _, tc := range []struct {
		index, total string
		want         shardSpec
		ok           bool
		wantErr      string
	}{
		{"", "", shardSpec{}, false, ""},
		{"0", "1", shardSpec{Index: 0, Total: 1}, true, ""},
		{" 2 ", "3", shardSpec{Index: 2, Total: 3}, true, ""},
		{"0", "", shardSpec{}, false, "must be set together"},
		{"", "2", shardSpec{}, false, "must be set together"},
		{"0", "0", shardSpec{}, false, envShardTotal},
		{"0", "x", shardSpec{}, false, envShardTotal},
		{"3", "3", shardSpec{}, false, envShardIndex},
		{"-1", "3", shardSpec{}, false, envShardIndex},
	} {
		t.Run(tc.index+"_"+tc.total, func(t *testing.T) {
			t.Setenv(envShardIndex, tc.index)
			t.Setenv(envShardTotal, tc.total)
			got, ok, err := shardSpecFromEnv()
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
	assert.Equal(t, "2/3", shardSpec{Index: 1, Total: 3}.String())
}

// shardFeatures has outline rows, a background, a rule, a scenario
// compatibility tags skip, a malformed tag, two identical outline rows
// and a scenario the tag filter drops.
func shardFeatures() map[string]string {
	return map[string]string{
		"a/outline.feature": `Feature: outline
  Background:
    Given the enrolled test repository

  Scenario Outline: row <n>
    Then a plain step

    Examples:
      | n |
      | 1 |
      | 2 |
      | 3 |
      | 4 |
      | 5 |
      | 5 |

  @skip:gitlab
  Scenario: skipped on gitlab
    Then a plain step

  @wip
  Scenario: filtered out
    Then a plain step
`,
		"b/plain.feature": `Feature: plain
  Scenario: org only
    Given a plain step

  @requires:capability:
  Scenario: malformed tag
    Given a plain step

  Rule: grouped
    Scenario: provisions inside a rule
      Given the enrolled test repository
`,
	}
}

// runGodogShard runs paths through godog with the shard gate in front of
// the compatibility check (as InitGatedRoutedScenario does) and returns
// the keys of the scenarios that passed the gate.
func runGodogShard(t *testing.T, paths []string, tagFilter string, gate func(*godog.Scenario) error, base *world.World) []string {
	t.Helper()
	var mu sync.Mutex
	var ran []string
	st := godog.TestSuite{
		Name: "shard",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
				if err := gate(scenario); err != nil {
					return ctx, err
				}
				mu.Lock()
				ran = append(ran, pickleKey(scenario))
				mu.Unlock()
				return ctx, suite.SkipErrorForTagNames(tagNamesOf(scenario), base)
			})
			sc.Step(`^the enrolled test repository$`, func() error { return nil })
			sc.Step(`^a plain step$`, func() error { return nil })
		},
		Options: &godog.Options{
			Format:      "progress",
			Output:      io.Discard,
			Paths:       paths,
			Tags:        tagFilter,
			Concurrency: 3,
		},
	}
	st.Run()
	return ran
}

// TestShardGate_GodogRunsEachSelectedInstanceOnceAcrossShards plans a
// real feature tree, then runs godog once per shard with the gate and
// checks the union of the shards is every selected instance, exactly
// once, with each shard verifying its own share.
func TestShardGate_GodogRunsEachSelectedInstanceOnceAcrossShards(t *testing.T) {
	dir := writeFeatures(t, shardFeatures())
	const tagFilter = "~@wip"
	base := &world.World{Config: env.RunnerConfig{SCM: "gitlab", InstallMode: "per-repo"}}
	skip := func(tags []string) error { return suite.SkipErrorForTagNames(tags, base) }

	instances, err := loadScenarioInstances([]string{dir}, tagFilter)
	require.NoError(t, err)
	require.Len(t, instances, 10)

	const perShardCap = 2
	shards, err := planShards(instances, skip, perShardCap)
	require.NoError(t, err)
	require.Len(t, shards, 4, "7 provisioning instances (one pair identical) at 2 per shard")
	assertExactlyOnce(t, instances, shards, perShardCap)

	want := map[string]int{}
	for _, inst := range instances {
		want[inst.Key]++
	}
	got := map[string]int{}
	for _, s := range shards {
		gate := newShardGate(shardSpec{Index: s.Index, Total: len(shards)}, shards)
		ran := runGodogShard(t, []string{dir}, tagFilter, gate.Gate, base)
		require.NoError(t, gate.Verify(), "shard %d", s.Index)
		assert.Len(t, ran, len(s.Instances), "shard %d ran its planned instances", s.Index)
		for _, key := range ran {
			got[key]++
		}
	}
	assert.Equal(t, want, got, "every selected instance ran on exactly one shard")
}

func TestShardGate_IdenticalOutlineRowsSplitAcrossShards(t *testing.T) {
	feature := "Feature: duplicate rows\n  Scenario Outline: identical\n    Given the enrolled test repository\n    Then a plain step\n    Examples:\n      | value |\n" + strings.Repeat("      | same |\n", 21)
	dir := writeFeatures(t, map[string]string{
		"a/duplicates.feature": feature,
		"b/other.feature":      "Feature: other\n  Scenario: other\n    Then a plain step\n",
	})
	// Godog parses duplicate paths even though it only executes the first.
	// The planner must consume IDs in the same order for later features.
	paths := []string{filepath.Join(dir, "a/duplicates.feature"), filepath.Join(dir, "a/duplicates.feature"), filepath.Join(dir, "b/other.feature")}
	instances, err := loadScenarioInstances(paths, "")
	require.NoError(t, err)
	require.Len(t, instances, 22)
	unique := make(map[string]bool, len(instances))
	for _, inst := range instances {
		assert.False(t, unique[inst.Key], "each Outline row has a distinct source ID")
		unique[inst.Key] = true
	}

	shards, err := planShards(instances, noSkip, maxProvisioningPerShard)
	require.NoError(t, err)
	require.Len(t, shards, 2)
	assertExactlyOnce(t, instances, shards, maxProvisioningPerShard)

	base := &world.World{Config: env.RunnerConfig{SCM: "github", InstallMode: "per-repo"}}
	want := make(map[string]int, len(instances))
	for _, inst := range instances {
		want[inst.Key]++
	}
	got := make(map[string]int, len(instances))
	for _, shard := range shards {
		gate := newShardGate(shardSpec{Index: shard.Index, Total: len(shards)}, shards)
		ran := runGodogShard(t, paths, "", gate.Gate, base)
		require.NoError(t, gate.Verify(), "shard %d", shard.Index)
		for _, key := range ran {
			got[key]++
		}
	}
	assert.Equal(t, want, got, "every identical row runs exactly once across shards")
}

func TestShardGate_VerifyReportsMissingAndUnknown(t *testing.T) {
	instances := syntheticInstances(2, 1)
	shards, err := planShards(instances, noSkip, 1)
	require.NoError(t, err)
	require.Len(t, shards, 2)

	gate := newShardGate(shardSpec{Index: 0, Total: 2}, shards)
	err = gate.Verify()
	require.ErrorContains(t, err, "started 0 time(s), planned 1")
	assert.ErrorContains(t, err, "shard 1/2")

	unknown := &godog.Scenario{Uri: "x.feature", Name: "not planned"}
	err = gate.Gate(unknown)
	require.Error(t, err)
	assert.NotErrorIs(t, err, godog.ErrSkip, "an unplanned scenario fails rather than being silently skipped")
	assert.ErrorContains(t, gate.Verify(), `"x.feature: not planned" is not in the plan`)
}

func TestSelectShard_DetectsPlanDrift(t *testing.T) {
	instances := syntheticInstances(49, 0)
	_, _, err := selectShard(shardSpec{Index: 0, Total: 2}, instances, noSkip)
	require.ErrorContains(t, err, "shard plan drift")

	planned, gate, err := selectShard(shardSpec{Index: 2, Total: 3}, instances, noSkip)
	require.NoError(t, err)
	assert.Equal(t, 2, planned.Index)
	assert.NotNil(t, gate)

	dup := scenarioInstance{Key: "dup", Provisions: true}
	_, _, err = selectShard(shardSpec{Index: 0, Total: 1}, []scenarioInstance{dup}, noSkip)
	require.NoError(t, err)
	many := make([]scenarioInstance, maxProvisioningPerShard+1)
	for i := range many {
		many[i] = dup
	}
	_, _, err = selectShard(shardSpec{Index: 0, Total: 1}, many, noSkip)
	require.ErrorContains(t, err, "exceed the per-shard cap")
}

func TestResolveRunShard(t *testing.T) {
	instances := syntheticInstances(25, 3)
	dev := env.RunnerConfig{Environment: "dev"}
	stage := env.RunnerConfig{Environment: "stage"}

	t.Run("unsharded runs everything", func(t *testing.T) {
		t.Setenv(envShardIndex, "")
		t.Setenv(envShardTotal, "")
		r, err := resolveRunShard(dev, instances, noSkip)
		require.NoError(t, err)
		assert.False(t, r.Sharded)
		assert.Nil(t, r.scenarioGate())
		require.NoError(t, r.verify())
		assert.Equal(t, 28, r.Demand.Eligible)
		assert.Equal(t, 25, r.Demand.Provisioning)
	})
	t.Run("dev shard runs its share", func(t *testing.T) {
		t.Setenv(envShardIndex, "1")
		t.Setenv(envShardTotal, "2")
		r, err := resolveRunShard(dev, instances, noSkip)
		require.NoError(t, err)
		assert.True(t, r.Sharded)
		assert.NotNil(t, r.scenarioGate())
		assert.Equal(t, len(r.Planned.Instances), r.Demand.Selected)
		assert.LessOrEqual(t, r.Demand.Provisioning, maxProvisioningPerShard)
		require.ErrorContains(t, r.verify(), "did not run its planned scenarios")
	})
	t.Run("stage single shard is unsharded", func(t *testing.T) {
		t.Setenv(envShardIndex, "0")
		t.Setenv(envShardTotal, "1")
		r, err := resolveRunShard(stage, instances, noSkip)
		require.NoError(t, err)
		assert.False(t, r.Sharded)
		assert.Equal(t, 25, r.Demand.Provisioning, "STAGE keeps every scenario in its single org")
	})
	t.Run("stage refuses several shards", func(t *testing.T) {
		t.Setenv(envShardIndex, "0")
		t.Setenv(envShardTotal, "2")
		_, err := resolveRunShard(stage, instances, noSkip)
		require.ErrorContains(t, err, "STAGE")
	})
	t.Run("bad env", func(t *testing.T) {
		t.Setenv(envShardIndex, "0")
		t.Setenv(envShardTotal, "")
		_, err := resolveRunShard(dev, instances, noSkip)
		require.Error(t, err)
	})
	t.Run("drift", func(t *testing.T) {
		t.Setenv(envShardIndex, "0")
		t.Setenv(envShardTotal, "5")
		_, err := resolveRunShard(dev, instances, noSkip)
		require.ErrorContains(t, err, "drift")
	})
}

func TestShardConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name     string
		raw      string
		capacity int
		sharded  bool
		want     int
		over     bool
		wantErr  bool
	}{
		{"unsharded defaults to combined capacity", "", 24, false, 24, false, false},
		{"shard defaults to at most 12", "", 24, true, defaultShardConcurrency, false, false},
		{"shard below the default keeps capacity", "", 6, true, 6, false, false},
		{"explicit value wins on a shard", "30", 24, true, 30, true, false},
		{"explicit value within capacity", "4", 24, true, 4, false, false},
		{"invalid", "x", 12, true, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, over, err := shardConcurrency(tc.raw, tc.capacity, tc.sharded)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, n)
			assert.Equal(t, tc.over, over)
		})
	}
}

func setRunnerEnv(t *testing.T, environment string) {
	t.Helper()
	t.Setenv("BEHAVIOUR_SCM", "gitlab")
	t.Setenv("BEHAVIOUR_CI", "githubactions")
	t.Setenv("BEHAVIOUR_INSTALL_MODE", "per-repo")
	t.Setenv("ENVIRONMENT", environment)
	t.Setenv("BEHAVIOUR_CAPABILITIES", "")
	t.Setenv("GODOG_TAGS", "~@wip")
}

func TestWriteSuiteShardPlan(t *testing.T) {
	dir := writeFeatures(t, matrixFeatures(45, true))
	opts := SuiteOptions{FeaturePaths: []string{dir}, FixturesRoot: "e2e/behaviour"}

	t.Run("dev", func(t *testing.T) {
		setRunnerEnv(t, "dev")
		path := filepath.Join(t.TempDir(), "plan.json")
		plan, err := WriteSuiteShardPlan(opts, path)
		require.NoError(t, err)
		assert.Equal(t, 3, plan.Total)
		assert.Equal(t, []int{0, 1, 2}, plan.Shards)
		assert.Equal(t, 47, plan.Selected, "45 rows, org-only, compat-skipped")
		assert.Equal(t, 1, plan.Skipped)
		assert.Equal(t, 45, plan.Provisioning)
		assert.Equal(t, maxProvisioningPerShard, plan.MaxProvisioningPerShard)
		sum := 0
		for _, s := range plan.PerShard {
			assert.LessOrEqual(t, s.Provisioning, maxProvisioningPerShard)
			sum += s.Instances
		}
		assert.Equal(t, plan.Selected, sum)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		var decoded SuiteShardPlan
		require.NoError(t, json.Unmarshal(data, &decoded))
		assert.Equal(t, plan, decoded)
	})
	t.Run("stage is one shard", func(t *testing.T) {
		setRunnerEnv(t, "stage")
		plan, err := WriteSuiteShardPlan(opts, "")
		require.NoError(t, err)
		assert.Equal(t, 1, plan.Total)
		assert.Equal(t, 45, plan.PerShard[0].Provisioning)
	})
	t.Run("errors", func(t *testing.T) {
		setRunnerEnv(t, "dev")
		_, err := WriteSuiteShardPlan(SuiteOptions{FixturesRoot: "x"}, "")
		require.ErrorContains(t, err, "invalid suite options")

		_, err = WriteSuiteShardPlan(SuiteOptions{FeaturePaths: []string{filepath.Join(dir, "missing")}, FixturesRoot: "x"}, "")
		require.ErrorContains(t, err, "reading feature files")

		_, err = WriteSuiteShardPlan(opts, filepath.Join(dir, "no-such-dir", "plan.json"))
		require.ErrorContains(t, err, "writing shard plan")

		t.Setenv("ENVIRONMENT", "prod")
		_, err = WriteSuiteShardPlan(opts, "")
		require.ErrorContains(t, err, "invalid behaviour runner config")
	})
	t.Run("plan file env", func(t *testing.T) {
		t.Setenv(envPlanFile, " /tmp/plan.json ")
		assert.Equal(t, "/tmp/plan.json", ShardPlanFileFromEnv())
	})
}

// TestShardedRun_EachShardRunsOnlyItsScenarios runs the full sharded
// path (plan, gate, org acquisition, routed godog suite) once per shard
// with fake orgs, and checks the shards together ran every eligible
// scenario exactly once, each acquiring orgs only for its own share.
func TestShardedRun_EachShardRunsOnlyItsScenarios(t *testing.T) {
	dir := writeFeatures(t, matrixFeatures(49, true))
	const tagFilter = "~@wip"
	base := &world.World{Config: env.RunnerConfig{SCM: "gitlab", InstallMode: "per-repo", Environment: "dev"}}
	skip := func(tags []string) error { return suite.SkipErrorForTagNames(tags, base) }
	instances, err := loadScenarioInstances([]string{dir}, tagFilter)
	require.NoError(t, err)
	all := summarizeDemand(instances, skip)

	plan, err := planShards(instances, skip, maxProvisioningPerShard)
	require.NoError(t, err)
	require.Len(t, plan, 3)

	ran := map[string]int{}
	var mu sync.Mutex
	for _, ps := range plan {
		t.Setenv(envShardIndex, fmt.Sprint(ps.Index))
		t.Setenv(envShardTotal, fmt.Sprint(len(plan)))
		shard, err := resolveRunShard(base.Config, instances, skip)
		require.NoError(t, err)
		require.True(t, shard.Sharded)

		h := newShardHarness(t, 6)
		metrics := newSuiteMetrics(nil)
		s := h.suite(t, defaultPlan(6))
		s.Metrics = metrics
		err = s.Run(context.Background(), shard.Demand, func(a *shardAssigner) error {
			router := a.Router()
			gs := godog.TestSuite{
				Name: "shard",
				ScenarioInitializer: func(sc *godog.ScenarioContext) {
					suite.InitGatedRoutedScenario(sc, base, shard.scenarioGate(), router)
					sc.Step(`^a plain step$`, func(ctx context.Context) error {
						w := world.FromContext(ctx)
						mu.Lock()
						ran[w.ScenarioName]++
						mu.Unlock()
						return nil
					})
					registerMetricsHooks(sc, metrics)
				},
				Options: &godog.Options{Format: "progress", Output: io.Discard, Paths: []string{dir}, Tags: tagFilter, Concurrency: 6},
			}
			if st := gs.Run(); st != 0 {
				return fmt.Errorf("godog status %d", st)
			}
			return shard.verify()
		})
		require.NoError(t, err, "shard %d", ps.Index)
		assert.Len(t, h.pool.acquired, 1, "shard %d needs one org for at most %d provisioning instances", ps.Index, maxProvisioningPerShard)
		h.assertTornDown(t)

		r := metrics.report()
		assert.Len(t, r.Orgs, 1)
		assert.NotEmpty(t, r.Orgs[0].EndQuota)
		assert.NotNil(t, r.FirstInstallSeconds, "provisioning steps completed")
	}
	// Every eligible scenario ran exactly once: outline rows have
	// distinct names ("provision 1".."provision 49"), each with one plain
	// step, as does the org-only scenario.
	assert.Len(t, ran, all.Eligible)
	for name, n := range ran {
		assert.Equal(t, 1, n, "scenario %q", name)
	}
	assert.Equal(t, 1, ran["org only"])
	assert.Equal(t, 1, ran["provision 49"])
}

func TestRegisterMetricsHooks_CountsRateLimitFailures(t *testing.T) {
	metrics := newSuiteMetrics(nil)
	metrics.markBodyStart()
	feature := `Feature: hooks
  Scenario: provisions
    Given the enrolled test repository

  Scenario: rate limited
    Given a rate-limited step

  Scenario: other failure
    Given a failing step
`
	st := godog.TestSuite{
		Name: "hooks",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Step(`^the enrolled test repository$`, func() error { return nil })
			sc.Step(`^a rate-limited step$`, func() error {
				return errors.New("github setup: API rate limit exceeded for installation")
			})
			sc.Step(`^a failing step$`, func() error { return errors.New("boom") })
			registerMetricsHooks(sc, metrics)
		},
		Options: &godog.Options{
			Format:          "progress",
			Output:          io.Discard,
			FeatureContents: []godog.Feature{{Name: "hooks.feature", Contents: []byte(feature)}},
		},
	}
	assert.NotZero(t, st.Run())
	r := metrics.report()
	assert.Equal(t, 1, r.RateLimitFailures)
	assert.NotNil(t, r.FirstInstallSeconds)
}

func TestIsRateLimitFailure(t *testing.T) {
	assert.True(t, isRateLimitFailure(errors.New("You have exceeded a secondary rate limit")))
	assert.True(t, isRateLimitFailure(errors.New("HTTP 429 Too Many Requests")))
	assert.False(t, isRateLimitFailure(errors.New("404 Not Found")))
}
