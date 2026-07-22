package world

import (
	"path/filepath"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/ci"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
)

// World holds scenario state and injected drivers.
type World struct {
	Config  env.RunnerConfig
	SCM     scm.Driver
	CI      ci.Driver
	Install install.State

	Org       string
	RepoFull  string
	RepoOwner string
	RepoName  string
	Token     string
	Logf      func(string, ...any)

	// FixturesRoot is module-relative (e.g. "e2e/behaviour" or "behaviour").
	FixturesRoot string

	ScenarioStart time.Time

	DummyOps           []runtime.BehaviourOperation
	ArtifactDir        string
	TriageTriggerEvent string // GitHub event for triage dispatch (issues for label path)

	IssueNumber    int
	IssueTitle     string
	WorkflowRun    *forge.WorkflowRun
	TriageWorkflow string

	DispatchAgent string
	PRNumber      int

	// Fork context — set by fork step definitions.
	ForkOwner    string
	ForkRepo     string
	ForkPRNumber int
	ForkPRBranch string

	// LeasedRepoName is the logical test-repo name acquired from a RepoPool
	// for this scenario's duration. Empty when no pool is configured.
	LeasedRepoName string
}

// Clone creates a shallow copy of w that shares driver references (Config,
// SCM, CI, Install) but has independent scenario-level fields (zeroed).
// Use Clone in the Before hook to give each scenario its own World.
func (w *World) Clone() *World {
	return &World{
		Config:       w.Config,
		SCM:          w.SCM,
		CI:           w.CI,
		Install:      w.Install,
		Org:          w.Org,
		RepoFull:     w.RepoFull,
		RepoOwner:    w.RepoOwner,
		RepoName:     w.RepoName,
		Token:        w.Token,
		Logf:         w.Logf,
		FixturesRoot: w.FixturesRoot,
	}
}

const BehaviourScriptRepoPath = "behaviour/current-scenario.yaml"

// BehaviourScriptPath returns the repo-relative path for the dummy agent script.
func (w *World) BehaviourScriptPath() string {
	if w.Install == nil {
		return BehaviourScriptRepoPath
	}
	if prefix := w.Install.ConfigPathPrefix(); prefix != "" {
		return filepath.Join(prefix, BehaviourScriptRepoPath)
	}
	return BehaviourScriptRepoPath
}
