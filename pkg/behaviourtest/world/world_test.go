package world

import (
	"sync"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci/githubactions"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	scmgithub "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/stretchr/testify/assert"
)

func TestClone_CopiesAllFields(t *testing.T) {
	original := &World{
		Org:            "test-org",
		RepoFull:       "test-org/test-repo",
		RepoOwner:      "test-org",
		RepoName:       "test-repo",
		Token:          "tok",
		FixturesRoot:   "e2e/behaviour",
		IssueNumber:    42,
		PRNumber:       7,
		DispatchAgent:  "triage",
		ArtifactDir:    "/tmp/art",
		ForkOwner:      "fork-org",
		ForkRepo:       "fork-repo",
		ForkPRNumber:   99,
		ForkPRBranch:   "pr-branch",
		LeasedRepoName: "test-repo-03",
	}
	clone := original.Clone()

	// Template / driver fields are copied.
	assert.Equal(t, original.Org, clone.Org)
	assert.Equal(t, original.RepoFull, clone.RepoFull)
	assert.Equal(t, original.RepoOwner, clone.RepoOwner)
	assert.Equal(t, original.RepoName, clone.RepoName)
	assert.Equal(t, original.Token, clone.Token)
	assert.Equal(t, original.FixturesRoot, clone.FixturesRoot)

	// Scenario fields are also copied (value copy). The caller is
	// responsible for zeroing them via resetScenarioWorld.
	assert.Equal(t, 42, clone.IssueNumber)
	assert.Equal(t, 7, clone.PRNumber)
	assert.Equal(t, "triage", clone.DispatchAgent)
	assert.Equal(t, "test-repo-03", clone.LeasedRepoName)
}

func TestClone_IndependentMutation(t *testing.T) {
	original := &World{Org: "test-org", RepoName: "test-repo"}
	clone := original.Clone()

	clone.IssueNumber = 123
	clone.RepoName = "test-repo-01"
	assert.Equal(t, 0, original.IssueNumber)
	assert.Equal(t, "test-repo", original.RepoName)
}

func TestClone_SharesDriversByReference(t *testing.T) {
	fc := forge.NewFakeClient()
	scmDriver := scmgithub.New(fc)
	ciDriver := githubactions.New(fc, "tok")
	original := &World{SCM: scmDriver, CI: ciDriver}
	clone := original.Clone()

	// Drivers are shared by reference — the production implementations
	// are immutable wrappers around forge.Client and are safe for
	// concurrent use.
	assert.Same(t, original.SCM, clone.SCM)
	assert.Same(t, original.CI, clone.CI)
}

// TestClone_SharesFailuresRecorder verifies that the failure recorder
// is shared by reference through Clone: a failure recorded through one
// clone is observed through another, because every clone of a template
// World points at the same run-scoped Recorder (see World.Failures).
func TestClone_SharesFailuresRecorder(t *testing.T) {
	template := &World{Failures: &behaviourfail.Recorder{}}
	first := template.Clone()
	second := template.Clone()

	// The shallow copy carries the recorder pointer, not a copy.
	assert.Same(t, template.Failures, first.Failures)
	assert.Same(t, template.Failures, second.Failures)

	first.Failures.RecordFailure(
		"scenario", "dispatch hits the rate limit",
		behaviourfail.Infrastructure, "429 too many requests")

	// The other clone observes the recorded failure.
	records := second.Failures.Snapshot()
	if assert.Len(t, records, 1) {
		assert.Equal(t, "scenario", records[0].Source)
		assert.Equal(t, "dispatch hits the rate limit", records[0].Scenario)
		assert.Equal(t, behaviourfail.Infrastructure, records[0].Class)
	}
	infra, scenario := second.Failures.Counts()
	assert.Equal(t, 1, infra)
	assert.Equal(t, 0, scenario)
}

// TestClone_ConcurrentFieldIndependence verifies that scenario-specific
// value fields on cloned Worlds can be mutated independently from
// concurrent goroutines without racing.
func TestClone_ConcurrentFieldIndependence(t *testing.T) {
	t.Parallel()

	template := &World{
		Config:       env.RunnerConfig{SCM: "github", CI: "githubactions", InstallMode: "per-repo"},
		Org:          "test-org",
		RepoFull:     "test-org/test-repo",
		RepoOwner:    "test-org",
		RepoName:     "test-repo",
		Token:        "tok",
		FixturesRoot: "e2e/behaviour",
	}

	const numClones = 12
	clones := make([]*World, numClones)
	for i := range numClones {
		clones[i] = template.Clone()
		clones[i].IssueNumber = 0
		clones[i].PRNumber = 0
		clones[i].ScenarioStart = time.Now()
	}

	// Each goroutine mutates only its own clone's scenario fields.
	var wg sync.WaitGroup
	for i, w := range clones {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Read shared template fields (value copies).
			_ = w.Config.InstallMode
			_ = w.FixturesRoot

			// Mutate scenario-specific fields independently.
			w.IssueNumber = i + 1
			w.PRNumber = i + 100
			w.DispatchAgent = "triage"
			w.ArtifactDir = "/tmp/art"
			w.ForkOwner = "fork-org"
		}()
	}
	wg.Wait()

	for i, w := range clones {
		assert.Equal(t, i+1, w.IssueNumber, "clone %d IssueNumber", i)
		assert.Equal(t, i+100, w.PRNumber, "clone %d PRNumber", i)
	}
}

func TestBehaviourScriptPath(t *testing.T) {
	// BT is per-repo only — BehaviourScriptPath always prefixes with
	// .fullsend regardless of state.
	w := &World{}
	got := w.BehaviourScriptPath()
	assert.Equal(t, ".fullsend/behaviour/current-scenario.yaml", got)
}
