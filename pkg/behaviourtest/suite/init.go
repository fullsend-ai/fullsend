package suite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/failclass"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/steps"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// InitScenario registers tag-based skips, Before/After hooks, and shared steps.
// Each scenario receives its own World cloned from template. The unified
// install.Driver on template.Driver handles repo allocation/deallocation;
// scenarios that need a repo call AllocateRepo in a step (e.g. "Given the
// enrolled test repository"), and the After hook deallocates on cleanup.
func InitScenario(sc *godog.ScenarioContext, template *world.World) {
	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		return beforeScenario(ctx, tagNames(scenario.Tags), template, scenario.Name)
	})
	sc.After(func(ctx context.Context, scenario *godog.Scenario, err error) (context.Context, error) {
		return afterScenario(ctx, template.Driver, err)
	})
	steps.Register(sc)
}

// beforeScenario clones the template World, resets scenario fields, and
// records the scenario name. Repo allocation is handled by the step (via
// Driver.AllocateRepo), not by the Before hook.
//
// name is the godog scenario name. In playback mode it is forwarded to
// the PlaybackDriver via SetRepoHint so the driver can correlate its
// per-repo bookkeeping back to the scenario that is running; for the
// standard pool-based Driver, SetRepoHint is a no-op.
func beforeScenario(ctx context.Context, tags []string, template *world.World, name string) (context.Context, error) {
	tagErr := SkipErrorForTagNames(tags, template)
	if errors.Is(tagErr, godog.ErrSkip) {
		return ctx, tagErr
	}
	// A non-skip tag error (a malformed tag) fails the scenario. The World is
	// still attached below so the After hook can write a failure summary for
	// it, as it does for any other failed scenario.
	w := template.Clone()
	resetScenarioWorld(w)
	w.ScenarioName = name

	if pd, ok := w.Driver.(*install.PlaybackDriver); ok {
		pd.SetRepoHint(name)
	}

	ctx = world.WithWorld(ctx, w)
	return ctx, tagErr
}

// afterScenario runs scenario cleanup and deallocates the repo if one was
// allocated. Deallocation errors are surfaced as test failures rather than
// panicking the godog runner.
//
// Every non-skip failure the hook sees — the scenario's own error, or a
// deallocation error that fails an otherwise passing scenario — is first
// recorded on the run's shared failure recorder with its classification
// and a redacted message, and logs one labelled line, via
// recordScenarioFailure.
//
// Order: for a failed scenario, CollectFailureLogs first saves the logs
// of the repository's workflow runs that in-scenario collection did not
// already save (a wait that timed out, a failure before any run was
// resolved) plus a failure summary; then CleanupScenario
// (issues/PRs/forks/hosting repos) runs, and the deferred
// DeallocateLease (DeallocateRepo for a driver that leases by name)
// deletes the leased slot repo and frees the slot. All debug
// collection (workflow logs via saveWorkflowRunLogs and
// CollectFailureLogs, agent artifacts via ensureArtifacts) has finished
// before the repo is deleted, so CI still has those files under
// BEHAVIOUR_ARTIFACT_DIR after the leased repo is gone.
//
// A deallocation failure on an otherwise passing scenario also writes the
// failure summary (the repo's workflow runs are collected best-effort, as
// teardown has already been attempted).
//
// CleanupScenario mutates by name, so before it runs the After hook
// hands it a lease-ownership check (World.LeaseOwned, from the driver's
// install.LeaseVerifier). When another holder has replaced the slot, or
// ownership cannot be verified, cleanup skips its mutations; the lease
// is still released below, and the driver's release checks ownership
// itself before deleting the slot repo.
//
// driver.DeallocateRepo is deferred so the lease is returned even if
// steps.CleanupScenario panics. A deallocation failure is always
// recorded: with no scenario error it becomes the scenario's error,
// and beside a scenario error it is recorded separately, so a
// scenario-classified teardown failure keeps the run non-retryable
// (#8036's no-masking contract).
func afterScenario(ctx context.Context, driver install.Driver, scenarioErr error) (_ context.Context, retErr error) {
	retErr = scenarioErr
	w := world.FromContext(ctx)
	if w == nil {
		return ctx, retErr
	}
	if driver != nil && w.LeasedRepoName != "" {
		name, org := w.LeasedRepoName, w.LeasedRepoOrg
		w.LeaseOwned = leaseOwnership(driver, org, name)
		defer func() {
			var deallocErr error
			if ld, ok := driver.(install.LeaseDriver); ok && org != "" {
				deallocErr = ld.DeallocateLease(ctx, org, name)
			} else {
				deallocErr = driver.DeallocateRepo(ctx, name)
			}
			if deallocErr != nil {
				if w.Logf != nil {
					w.Logf("deallocating repo: %v", deallocErr)
				}
				deallocFailure := fmt.Errorf("deallocating repo: %w", deallocErr)
				if retErr == nil {
					retErr = deallocFailure
					// The scenario passed, so no failure summary was written
					// above; the deallocation failure turns it into a failed
					// scenario, which must leave the same artifact.
					recordScenarioFailure(w, retErr)
					steps.CollectFailureLogs(ctx, w, retErr)
				} else {
					// The scenario's own failure was recorded above. Record
					// the teardown failure too: when it classifies as
					// scenario it must keep the whole run non-retryable, so
					// an infrastructure retry cannot mask it.
					recordScenarioFailure(w, deallocFailure)
				}
			}
		}()
	}
	if scenarioErr != nil && !errors.Is(scenarioErr, godog.ErrSkip) {
		recordScenarioFailure(w, scenarioErr)
		steps.CollectFailureLogs(ctx, w, scenarioErr)
	}
	steps.CleanupScenario(w)
	return ctx, retErr
}

// recordScenarioFailure records one classified failure for a failed
// scenario on the run's shared recorder (World.Failures) and logs a
// single labelled [failure-class] line, so the suite's console output
// and the failure-class report agree on the same per-failure record.
//
// The World's credential literals are registered before the message is
// redacted, so the very first failure a process records — before any
// debug-log collection has registered anything — still masks an opaque
// token that no pattern recognizes.
//
// Every call site labels the source "scenario": a deallocation failure
// fails the scenario it tore down, and its recorded message
// ("deallocating repo: ...") names the origin, so no third source label
// is needed beside "scenario" and "bootstrap". The recorder is
// nil-receiver-safe, so Worlds built without one record nothing; a nil
// Logf suppresses only the console line. The scenario name is redacted
// in the log line because it is forge-supplied text.
func recordScenarioFailure(w *world.World, err error) {
	steps.RegisterRedactionLiterals(w)
	class := failclass.Classify(err)
	w.Failures.RecordFailure("scenario", w.ScenarioName, class, steps.RedactText(err.Error()))
	if w.Logf != nil {
		w.Logf("[failure-class] scenario %q: %s", steps.RedactText(w.ScenarioName), class)
	}
}

// leaseOwnership returns the check scenario cleanup uses to confirm the
// slot (org, name) is still this scenario's lease, or nil when the
// driver leases by repo name alone (no shared slot to lose). A driver
// that leases across orgs but cannot verify yields a check that always
// fails, so cleanup fails closed rather than mutating by name.
func leaseOwnership(driver install.Driver, org, name string) func(context.Context) (bool, error) {
	if org == "" {
		return nil
	}
	if lv, ok := driver.(install.LeaseVerifier); ok {
		return func(ctx context.Context) (bool, error) {
			return lv.StillOwnsLease(ctx, org, name)
		}
	}
	if _, ok := driver.(install.LeaseDriver); ok {
		return func(context.Context) (bool, error) {
			return false, fmt.Errorf("%T cannot verify lease ownership of %s/%s", driver, org, name)
		}
	}
	return nil
}

func resetScenarioWorld(w *world.World) {
	w.ScenarioStart = time.Now()
	w.ScenarioBegin = w.ScenarioStart
	w.ScenarioName = ""
	w.SavedLogRunIDs = nil
	w.DummyOps = nil
	w.IssueNumber = 0
	w.IssueTitle = ""
	w.PRNumber = 0
	w.DispatchAgent = ""
	w.TriageWorkflow = ""
	w.TriageTriggerEvent = ""
	w.WorkflowRun = nil
	w.ArtifactDir = ""
	w.ForkOwner = ""
	w.ForkRepo = ""
	w.ForkPRNumber = 0
	w.ForkPRBranch = ""
	w.ForkSCM = nil
	w.URLHarnessRepoOwner = ""
	w.URLHarnessRepoName = ""
	w.URLBaseHarnesses = nil
	w.RecordedBranchSHAs = nil
	w.CreatedBranches = nil
	w.CreatedPRNumbers = nil
	w.CreatedIssueNumbers = nil
	w.LeasedRepoName = ""
	w.LeasedRepoOrg = ""
	w.LeaseOwned = nil
	w.MintURL = ""
	w.MintStatusGroup = ""
	w.MintWorkflowHostRepos = nil
	w.KillSwitchActivated = false
	w.RuntimeOverridden = false
	w.RuntimeOriginal = ""
	w.AllowedResourcesOverridden = false
	w.AllowedResourcesOriginal = nil
	w.AgentsOverridden = false
	w.AgentsOriginal = nil
	w.GatewayConfigOverridden = false
	w.GatewayConfigOriginal = nil
	w.GatewayAPIKeySecretSet = false
	w.OwnersAuthActivated = false
	w.JiraMockServer = nil
	w.JiraMockState = nil
	w.JiraConfigDir = ""
	w.PlaybackEntries = nil
	w.PlaybackCommitted = false
	w.ConsumedHarnessRunIDs = nil
	w.HarnessRunArtifactDirs = nil
	w.StagePublishedSHA = nil
	w.MintProbe = nil
	w.MintStatus = nil
	w.BotRoleProbe = nil
}

func tagNames(tags []*messages.PickleTag) []string {
	names := make([]string, 0, len(tags))
	for _, tag := range tags {
		names = append(names, tag.Name)
	}
	return names
}

// SkipErrorForTagNames returns godog.ErrSkip when compatibility tags exclude the scenario.
func SkipErrorForTagNames(tags []string, w *world.World) error {
	for _, tag := range tags {
		name := strings.TrimPrefix(tag, "@")
		switch {
		case name == "skip:per-repo" && w.Config.InstallMode == "per-repo":
			return godog.ErrSkip
		case name == "skip:gitlab" && w.Config.SCM == "gitlab":
			return godog.ErrSkip
		case name == "playback" && !w.IsPlaybackMode():
			// @playback scenarios replay canned results via the
			// dummy-playback runtime and only make sense under the
			// playback suite (pkg/behaviourtest.RunPlaybackSuite),
			// whose template World wraps install.Driver in a
			// *install.PlaybackDriver. Skip them under the standard
			// behaviour suite regardless of GODOG_TAGS so a shared
			// "features" directory can hold both without the normal
			// suite trying (and failing) to run playback-only steps.
			return godog.ErrSkip
		case strings.HasPrefix(name, "requires:capability:"):
			// Skip unless the runner declares the capability via
			// BEHAVIOUR_CAPABILITIES. Gates scenarios that assert
			// behavior only present past a dependency version, so CI
			// stays green until the dependency ships and the runner
			// opts in.
			capability := strings.TrimPrefix(name, "requires:capability:")
			if capability == "" {
				return fmt.Errorf("malformed tag %q: requires:capability: needs a name", tag)
			}
			if !w.Config.HasCapability(capability) {
				return godog.ErrSkip
			}
		}
	}
	return nil
}
