package behaviourtest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock advances by step on every reading.
type fakeClock struct {
	t    time.Time
	step time.Duration
}

func (c *fakeClock) now() time.Time {
	c.t = c.t.Add(c.step)
	return c.t
}

func TestSuiteMetrics_RecordsPhases(t *testing.T) {
	clock := &fakeClock{t: time.Unix(0, 0), step: time.Second}
	m := newSuiteMetrics(clock.now) // t=1
	m.setShard(shardSpec{Index: 1, Total: 3}, plannedShard{Instances: make([]scenarioInstance, 5), Provisioning: 3, Other: 2})
	m.setBuild(90 * time.Second)
	m.setOrgSetup(30 * time.Second)
	m.setConcurrency(12)
	m.addOrg("halfsend-01", 20, "remaining=5000/5000")
	m.markInstallDone() // before the body: ignored
	m.markBodyStart()   // t=2
	m.markInstallDone() // t=3 -> 1s
	m.markInstallDone() // later completions do not move it
	m.recordScenarioError(errors.New("API rate limit exceeded"))
	m.recordScenarioError(errors.New("boom"))
	m.recordScenarioError(nil)
	m.markBodyEnd()      // t=4 -> 2s
	m.markCleanupStart() // t=5
	m.setEndQuota("halfsend-01", "remaining=4000/5000")
	m.setEndQuota("halfsend-99", "ignored")
	m.markCleanupEnd() // t=6 -> 1s

	r := m.report() // t=7 -> total 6s
	assert.Equal(t, 2, r.Shard)
	assert.Equal(t, 3, r.Total)
	assert.Equal(t, 5, r.PlannedInstances)
	assert.Equal(t, 3, r.PlannedProvisioning)
	assert.Equal(t, 2, r.PlannedOther)
	assert.Equal(t, 12, r.Concurrency)
	assert.InDelta(t, 90, r.BuildSeconds, 0.001)
	assert.InDelta(t, 30, r.OrgSetupSeconds, 0.001)
	require.NotNil(t, r.FirstInstallSeconds)
	assert.InDelta(t, 1, *r.FirstInstallSeconds, 0.001)
	assert.InDelta(t, 2, r.BodySeconds, 0.001)
	assert.InDelta(t, 1, r.CleanupSeconds, 0.001)
	assert.InDelta(t, 6, r.TotalSeconds, 0.001)
	assert.Equal(t, 1, r.RateLimitFailures)
	assert.Equal(t, []orgMetrics{{Org: "halfsend-01", Capacity: 20, StartQuota: "remaining=5000/5000", EndQuota: "remaining=4000/5000"}}, r.Orgs)
}

func TestSuiteMetrics_UnmarkedPhasesStayZero(t *testing.T) {
	m := newSuiteMetrics(nil)
	m.markBodyEnd()
	m.markCleanupEnd()
	r := m.report()
	assert.Zero(t, r.BodySeconds)
	assert.Zero(t, r.CleanupSeconds)
	assert.Nil(t, r.FirstInstallSeconds)
}

func TestSuiteMetrics_NilIsSafe(t *testing.T) {
	var m *suiteMetrics
	m.setShard(shardSpec{}, plannedShard{})
	m.setBuild(time.Second)
	m.setOrgSetup(time.Second)
	m.setConcurrency(1)
	m.addOrg("o", 1, "q")
	m.setEndQuota("o", "q")
	m.markBodyStart()
	m.markBodyEnd()
	m.markInstallDone()
	m.markCleanupStart()
	m.markCleanupEnd()
	m.recordScenarioError(errors.New("rate limit"))
	require.NoError(t, m.write(t.TempDir(), t.Logf))
}

func TestSuiteMetrics_Write(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, format)
	}

	t.Run("unsharded", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "artifacts")
		m := newSuiteMetrics(nil)
		require.NoError(t, m.write(dir, logf))
		data, err := os.ReadFile(filepath.Join(dir, "shard-metrics.json"))
		require.NoError(t, err)
		var r shardMetricsReport
		require.NoError(t, json.Unmarshal(data, &r))
		assert.Zero(t, r.Total)
	})
	t.Run("sharded", func(t *testing.T) {
		dir := t.TempDir()
		m := newSuiteMetrics(nil)
		m.setShard(shardSpec{Index: 2, Total: 3}, plannedShard{})
		require.NoError(t, m.write(dir, logf))
		data, err := os.ReadFile(filepath.Join(dir, "shard-metrics-3.json"))
		require.NoError(t, err)
		assert.Contains(t, string(data), `"shard":3`)
	})
	t.Run("log only", func(t *testing.T) {
		require.NoError(t, newSuiteMetrics(nil).write("", logf))
	})
	t.Run("unwritable", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		require.NoError(t, os.WriteFile(file, nil, 0o644))
		require.ErrorContains(t, newSuiteMetrics(nil).write(file, logf), "creating artifact dir")

		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "shard-metrics.json"), 0o755))
		require.ErrorContains(t, newSuiteMetrics(nil).write(dir, logf), "writing shard metrics")
	})
	for _, l := range lines {
		assert.True(t, strings.HasPrefix(l, "[shard-metrics]"), l)
	}
}
