package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerDispatchSteps(sc *godog.ScenarioContext) {
	sc.Step(`^a custom harness "([^"]+)" with:$`, func(ctx context.Context, name, doc string) (context.Context, error) {
		return ctx, givenCustomHarness(world.FromContext(ctx), name, doc)
	})
	sc.Step(`^a disabled custom harness "([^"]+)" with:$`, func(ctx context.Context, name, doc string) (context.Context, error) {
		return ctx, givenDisabledCustomHarness(world.FromContext(ctx), name, doc)
	})
	sc.Step(`^the harness "([^"]+)" workflow completes successfully$`, func(ctx context.Context, agent string) (context.Context, error) {
		return ctx, thenHarnessWorkflowCompletes(world.FromContext(ctx), agent)
	})
	sc.Step(`^the harness "([^"]+)" agent did not run$`, func(ctx context.Context, agent string) (context.Context, error) {
		return ctx, thenHarnessAgentDidNotRun(world.FromContext(ctx), agent)
	})
	sc.Step(`^a pull request is opened$`, func(ctx context.Context) (context.Context, error) {
		return ctx, whenPullRequestOpened(world.FromContext(ctx))
	})
	sc.Step(`^the pull request is labeled "([^"]+)"$`, func(ctx context.Context, label string) (context.Context, error) {
		return ctx, whenPullRequestLabeled(world.FromContext(ctx), label)
	})
	sc.Step(`^a review comment is submitted on the pull request$`, func(ctx context.Context) (context.Context, error) {
		return ctx, whenPullRequestReviewComment(world.FromContext(ctx))
	})
	sc.Step(`^the kill switch is active$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenKillSwitchActive(world.FromContext(ctx))
	})
}

// givenKillSwitchActive sets kill_switch: true in the enrolled repo's
// config.yaml, causing Dispatch to return an empty matrix for all agents.
// It also marks w.KillSwitchActivated so CleanupScenario deactivates the
// switch before the slot is reused by another scenario.
func givenKillSwitchActive(w *world.World) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before kill-switch operations")
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetKillSwitch(true)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: activate kill switch", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	w.KillSwitchActivated = true
	return nil
}

// DeactivateKillSwitch sets kill_switch: false in the enrolled repo's
// config.yaml. Exported so CleanupScenario (in package steps) can call
// it during scenario teardown.
func DeactivateKillSwitch(w *world.World) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before kill-switch operations")
	}
	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	cfg.SetKillSwitch(false)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, "behaviour: deactivate kill switch", merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

func givenDisabledCustomHarness(w *world.World, name, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before harness operations")
	}
	name = strings.TrimSpace(name)
	doc = strings.TrimSpace(doc)
	if name == "" || doc == "" {
		return fmt.Errorf("harness name and contents are required")
	}

	harnessPath := filepath.Join(".fullsend", "harness", name+".yaml")
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, harnessPath, fmt.Sprintf("behaviour: add harness %s", name), []byte(doc)); err != nil {
		return fmt.Errorf("committing harness: %w", err)
	}

	if err := commitLocalHarnessResources(context.Background(), w, name, doc); err != nil {
		return err
	}

	// Snapshot agents before modification so CleanupScenario can restore.
	if err := snapshotAgents(w); err != nil {
		return fmt.Errorf("snapshotting agents: %w", err)
	}

	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfg, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	disabled := false
	entry := config.AgentEntry{Name: name, Source: "harness/" + name + ".yaml", Enabled: &disabled}
	agents := cfg.AgentEntries()
	found := false
	for i, a := range agents {
		if strings.EqualFold(a.DerivedName(), name) {
			agents[i] = entry
			found = true
			break
		}
	}
	if !found {
		agents = append(agents, entry)
	}
	cfg.SetAgents(agents)
	merged, err := cfg.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, fmt.Sprintf("behaviour: register disabled harness %s", name), merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

func givenCustomHarness(w *world.World, name, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before harness operations")
	}
	name = strings.TrimSpace(name)
	doc = strings.TrimSpace(doc)
	if name == "" || doc == "" {
		return fmt.Errorf("harness name and contents are required")
	}
	w.DispatchAgent = name

	harnessPath := filepath.Join(".fullsend", "harness", name+".yaml")
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, harnessPath, fmt.Sprintf("behaviour: add harness %s", name), []byte(doc)); err != nil {
		return fmt.Errorf("committing harness: %w", err)
	}

	if err := commitLocalHarnessResources(context.Background(), w, name, doc); err != nil {
		return err
	}

	// Snapshot agents before modification so CleanupScenario can restore.
	if err := snapshotAgents(w); err != nil {
		return fmt.Errorf("snapshotting agents: %w", err)
	}

	cfgPath := filepath.Join(".fullsend", "config.yaml")
	cfgData, err := w.SCM.GetFileContent(context.Background(), w.Org, w.RepoName, cfgPath)
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	cfgW, err := config.ParsePerRepoConfigWriter(cfgData)
	if err != nil {
		return fmt.Errorf("parsing config: %w", err)
	}
	entry := config.AgentEntry{Name: name, Source: "harness/" + name + ".yaml"}
	agents := cfgW.AgentEntries()
	found := false
	for i, a := range agents {
		if strings.EqualFold(a.DerivedName(), name) {
			agents[i] = entry
			found = true
			break
		}
	}
	if !found {
		agents = append(agents, entry)
	}
	cfgW.SetAgents(agents)
	merged, err := cfgW.Marshal()
	if err != nil {
		return err
	}
	if err := w.SCM.CommitFile(context.Background(), w.Org, w.RepoName, cfgPath, fmt.Sprintf("behaviour: register harness %s", name), merged); err != nil {
		return fmt.Errorf("updating config: %w", err)
	}
	return nil
}

// commitLocalHarnessResources parses the harness YAML doc and commits
// any relative resource files (agent, policy) under .fullsend/ on the
// config repo. This ensures local custom harnesses can reference agent
// and policy files that exist on disk when the harness is validated.
//
// This mirrors commitRelativeResources in url_dispatch.go but commits
// to the config repo with the .fullsend/ prefix instead of to a hosting
// repo at the repo root.
func commitLocalHarnessResources(ctx context.Context, w *world.World, harnessName, doc string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before harness operations")
	}
	var h struct {
		Agent  string `yaml:"agent"`
		Policy string `yaml:"policy"`
	}
	if err := yaml.Unmarshal([]byte(doc), &h); err != nil {
		return fmt.Errorf("parsing harness YAML for resource paths: %w", err)
	}

	owner := w.Org
	repo := w.RepoName

	if h.Agent != "" && !strings.HasPrefix(h.Agent, "/") && !strings.HasPrefix(h.Agent, "https://") {
		agentPath := filepath.Join(".fullsend", h.Agent)
		if err := w.SCM.CommitFile(ctx, owner, repo, agentPath,
			fmt.Sprintf("behaviour: add agent resource for %s", harnessName),
			[]byte(minimalAgentContent)); err != nil {
			return fmt.Errorf("committing agent resource %s: %w", agentPath, err)
		}
	}

	if h.Policy != "" && !strings.HasPrefix(h.Policy, "/") && !strings.HasPrefix(h.Policy, "https://") {
		policyPath := filepath.Join(".fullsend", h.Policy)
		minimalPolicy := fmt.Sprintf("# Minimal policy for %s\n", harnessName)
		if err := w.SCM.CommitFile(ctx, owner, repo, policyPath,
			fmt.Sprintf("behaviour: add policy resource for %s", harnessName),
			[]byte(minimalPolicy)); err != nil {
			return fmt.Errorf("committing policy resource %s: %w", policyPath, err)
		}
	}

	return nil
}

func thenHarnessWorkflowCompletes(w *world.World, agent string) error {
	agent = strings.TrimSpace(agent)
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	ctx := context.Background()
	run, err := w.CI.WaitForHarnessAgent(ctx, w.Org, w.RepoName, agent, w.ScenarioStart)
	if err != nil {
		return err
	}
	w.WorkflowRun = run
	return ensureHarnessArtifacts(w, agent)
}

// defaultSettleDuration is the maximum time to wait for the dispatch
// pipeline to settle before asserting artifact absence.
const defaultSettleDuration = 90 * time.Second

// negativeSettleDuration returns how long to wait before a negative
// (did-not-run) assertion.  When the scenario already completed a
// positive harness wait (WorkflowRun is set), or enough wall time has
// elapsed since ScenarioStart, the settle is unnecessary.
func negativeSettleDuration(w *world.World, now time.Time) time.Duration {
	// A completed positive wait means the dispatch pipeline already
	// ran to completion — no additional settle needed.
	if w.WorkflowRun != nil {
		return 0
	}

	// Safety: if ScenarioStart was never recorded, use the full settle.
	if w.ScenarioStart.IsZero() {
		return defaultSettleDuration
	}

	elapsed := now.Sub(w.ScenarioStart)
	if elapsed >= defaultSettleDuration {
		return 0
	}
	return defaultSettleDuration - elapsed
}

func thenHarnessAgentDidNotRun(w *world.World, agent string) error {
	if w.ScenarioStart.IsZero() {
		return fmt.Errorf("no workflow trigger time recorded")
	}
	ctx := context.Background()

	// Allow dispatch pipeline time to settle before asserting absence.
	// Skip or shorten the wait when a positive harness wait already
	// elapsed in this scenario (piggyback pattern).
	if d := negativeSettleDuration(w, time.Now()); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}
	return w.CI.AssertNoHarnessAgentArtifact(ctx, w.Org, w.RepoName, agent, w.ScenarioStart)
}

func ensureHarnessArtifacts(w *world.World, agent string) error {
	if w.ArtifactDir != "" {
		return nil
	}
	ctx := context.Background()
	dest, err := prepareArtifactDir()
	if err != nil {
		return err
	}
	if w.WorkflowRun == nil {
		return fmt.Errorf("no workflow run recorded")
	}
	if err := w.CI.DownloadNamedArtifactFromRun(ctx, w.Org, w.RepoName, w.WorkflowRun.ID, "fullsend-"+agent, dest); err != nil {
		_ = os.RemoveAll(dest)
		return err
	}
	w.ArtifactDir = dest
	return nil
}

func whenPullRequestOpened(w *world.World) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before PR operations")
	}
	w.ScenarioStart = time.Now()
	branch := fmt.Sprintf("behaviour-pr-%d", time.Now().UnixNano())
	ctx := context.Background()
	if err := w.SCM.CreateBranch(ctx, w.RepoOwner, w.RepoName, branch); err != nil {
		return err
	}
	msg := fmt.Sprintf("behaviour pr %s", branch)
	if err := w.SCM.CommitFileToBranch(ctx, w.RepoOwner, w.RepoName, branch, "behaviour/pr.txt", msg, []byte("behaviour test\n")); err != nil {
		return err
	}
	pr, err := w.SCM.CreateChangeProposal(ctx, w.RepoOwner, w.RepoName, "Behaviour test PR", "behaviour", branch, "main")
	if err != nil {
		return err
	}
	w.PRNumber = pr.Number
	return nil
}

func whenPullRequestLabeled(w *world.World, label string) error {
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request opened")
	}
	w.ScenarioStart = time.Now()
	return w.SCM.AddIssueLabels(context.Background(), w.RepoOwner, w.RepoName, w.PRNumber, label)
}

func whenPullRequestReviewComment(w *world.World) error {
	if w.PRNumber == 0 {
		return fmt.Errorf("no pull request opened")
	}
	w.ScenarioStart = time.Now()
	// COMMENT works when the e2e bot authored the PR; APPROVE returns 422 self-review.
	return w.SCM.SubmitPullRequestReview(context.Background(), w.RepoOwner, w.RepoName, w.PRNumber, "COMMENT")
}
