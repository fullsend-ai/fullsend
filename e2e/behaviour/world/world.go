package world

import (
	"path/filepath"
	"time"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/ci"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/env"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/install"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/runtime"
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

	ScenarioStart time.Time

	DummyOps           []runtime.BehaviourOperation
	ArtifactDir        string
	TriageTriggerEvent string // GitHub event for triage dispatch (issues for label path)

	IssueNumber    int
	IssueTitle     string
	WorkflowRun    *forge.WorkflowRun
	TriageWorkflow string
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
