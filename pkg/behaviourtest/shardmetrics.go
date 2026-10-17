package behaviourtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// suiteMetrics records where a behaviour run (one CI shard) spends its
// time, so separate-runner shards can be compared with a single-runner
// run. It holds counts, durations, org names and rate-limit budgets
// only: never credentials, environment values or command arguments.
// All methods are safe for concurrent use and on a nil receiver.
type suiteMetrics struct {
	mu    sync.Mutex
	now   func() time.Time
	start time.Time

	bodyStart    time.Time
	cleanupStart time.Time

	data shardMetricsReport
}

// shardMetricsReport is the JSON written to BEHAVIOUR_ARTIFACT_DIR.
type shardMetricsReport struct {
	// Shard is 1-based; 0 with Total 0 means the run was not sharded.
	Shard int `json:"shard"`
	Total int `json:"total"`

	PlannedInstances    int `json:"planned_instances"`
	PlannedProvisioning int `json:"planned_provisioning"`
	PlannedOther        int `json:"planned_other"`
	Concurrency         int `json:"concurrency"`

	BuildSeconds    float64 `json:"build_seconds"`
	OrgSetupSeconds float64 `json:"org_setup_seconds"`
	// FirstInstallSeconds is from the start of the scenario body to the
	// first provisioning step that completed; nil when none completed.
	FirstInstallSeconds *float64 `json:"first_install_seconds"`
	BodySeconds         float64  `json:"body_seconds"`
	CleanupSeconds      float64  `json:"cleanup_seconds"`
	TotalSeconds        float64  `json:"total_seconds"`

	Orgs []orgMetrics `json:"orgs"`
	// RateLimitFailures counts scenarios that failed with a GitHub
	// rate-limit error.
	RateLimitFailures int `json:"rate_limit_failures"`
}

type orgMetrics struct {
	Org        string `json:"org"`
	Capacity   int    `json:"capacity"`
	StartQuota string `json:"start_quota"`
	EndQuota   string `json:"end_quota,omitempty"`
}

func newSuiteMetrics(now func() time.Time) *suiteMetrics {
	if now == nil {
		now = time.Now
	}
	return &suiteMetrics{now: now, start: now()}
}

func seconds(d time.Duration) float64 {
	return d.Round(time.Millisecond).Seconds()
}

func (m *suiteMetrics) setShard(spec shardSpec, planned plannedShard) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Shard = spec.Index + 1
	m.data.Total = spec.Total
	m.data.PlannedInstances = len(planned.Instances)
	m.data.PlannedProvisioning = planned.Provisioning
	m.data.PlannedOther = planned.Other
}

func (m *suiteMetrics) setBuild(d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.BuildSeconds = seconds(d)
}

func (m *suiteMetrics) setOrgSetup(d time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.OrgSetupSeconds = seconds(d)
}

func (m *suiteMetrics) setConcurrency(n int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Concurrency = n
}

func (m *suiteMetrics) addOrg(org string, capacity int, quota string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.Orgs = append(m.data.Orgs, orgMetrics{Org: org, Capacity: capacity, StartQuota: quota})
}

func (m *suiteMetrics) setEndQuota(org, quota string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.data.Orgs {
		if m.data.Orgs[i].Org == org {
			m.data.Orgs[i].EndQuota = quota
		}
	}
}

func (m *suiteMetrics) markBodyStart() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodyStart = m.now()
}

func (m *suiteMetrics) markBodyEnd() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.bodyStart.IsZero() {
		m.data.BodySeconds = seconds(m.now().Sub(m.bodyStart))
	}
}

// markInstallDone records the first completed provisioning step.
func (m *suiteMetrics) markInstallDone() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data.FirstInstallSeconds != nil || m.bodyStart.IsZero() {
		return
	}
	s := seconds(m.now().Sub(m.bodyStart))
	m.data.FirstInstallSeconds = &s
}

func (m *suiteMetrics) markCleanupStart() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupStart = m.now()
}

func (m *suiteMetrics) markCleanupEnd() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cleanupStart.IsZero() {
		m.data.CleanupSeconds = seconds(m.now().Sub(m.cleanupStart))
	}
}

// recordScenarioError counts a scenario failure caused by a GitHub
// rate limit. Only the error's classification is kept, not its text.
func (m *suiteMetrics) recordScenarioError(err error) {
	if m == nil || err == nil || !isRateLimitFailure(err) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.RateLimitFailures++
}

// isRateLimitFailure reports whether err reads as a GitHub primary or
// secondary rate-limit failure. Scenario errors often wrap CLI output,
// so this matches the message rather than a typed API error.
func isRateLimitFailure(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "429 too many requests")
}

// report finalizes TotalSeconds and returns a copy of the report.
func (m *suiteMetrics) report() shardMetricsReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data.TotalSeconds = seconds(m.now().Sub(m.start))
	out := m.data
	out.Orgs = append([]orgMetrics(nil), m.data.Orgs...)
	return out
}

// write logs the report as one line and, when artifactDir is set,
// writes it to shard-metrics.json there (shard-metrics-N.json for
// shard N) so each shard's upload carries it.
func (m *suiteMetrics) write(artifactDir string, logf func(string, ...any)) error {
	if m == nil {
		return nil
	}
	data, err := json.Marshal(m.report())
	if err != nil {
		return fmt.Errorf("encoding shard metrics: %w", err)
	}
	logf("[shard-metrics] %s", data)
	if artifactDir == "" {
		return nil
	}
	name := "shard-metrics.json"
	m.mu.Lock()
	if m.data.Total > 0 {
		name = fmt.Sprintf("shard-metrics-%d.json", m.data.Shard)
	}
	m.mu.Unlock()
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return fmt.Errorf("creating artifact dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, name), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing shard metrics: %w", err)
	}
	return nil
}
