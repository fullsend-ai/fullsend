package steps

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerTriageSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the enrolled test repository$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenEnrolledTestRepository(ctx, world.FromContext(ctx))
	})
	sc.Step(`^an enrolled repository "([^"]+)"$`, func(ctx context.Context, fullName string) (context.Context, error) {
		return ctx, givenEnrolledRepository(world.FromContext(ctx), fullName)
	})
	sc.Step(`^an issue with title "([^"]+)" and body containing "([^"]+)"$`, func(ctx context.Context, title, bodyContains string) (context.Context, error) {
		return ctx, givenIssueWithTitleAndBody(world.FromContext(ctx), title, bodyContains)
	})
	sc.Step(`^an issue$`, func(ctx context.Context) (context.Context, error) {
		return ctx, givenIssue(world.FromContext(ctx))
	})
	sc.Step(`^the issue is labeled "([^"]+)"$`, func(ctx context.Context, label string) (context.Context, error) {
		return ctx, whenIssueLabeled(world.FromContext(ctx), label)
	})
	sc.Step(`^the triage workflow completes successfully$`, func(ctx context.Context) (context.Context, error) {
		return ctx, thenTriageWorkflowCompletes(world.FromContext(ctx))
	})
	sc.Step(`^the issue has label "([^"]+)"$`, func(ctx context.Context, label string) (context.Context, error) {
		return ctx, thenIssueHasLabel(world.FromContext(ctx), label)
	})
}

func givenEnrolledTestRepository(ctx context.Context, w *world.World) error {
	// Guard double-allocation: if AllocateRepo was already called for
	// this scenario (e.g. duplicate "Given the enrolled test repository"
	// step), skip rather than leaking a second slot.
	if w.LeasedRepoName != "" {
		return nil
	}

	if w.Driver == nil {
		return fmt.Errorf("no install driver configured; use a Factory to construct a Driver for the suite")
	}

	if ld, ok := w.Driver.(install.LeaseDriver); ok {
		lease, err := ld.AllocateLease(ctx)
		if err != nil {
			return fmt.Errorf("allocating repo: %w", err)
		}
		if err := bindLease(w, lease); err != nil {
			if deallocErr := ld.DeallocateLease(ctx, lease.Org, lease.Repo); deallocErr != nil {
				err = errors.Join(err, fmt.Errorf("releasing the lease: %w", deallocErr))
			}
			return err
		}
		w.LeasedRepoName = lease.Repo
		w.LeasedRepoOrg = lease.Org
	} else {
		repoName, err := w.Driver.AllocateRepo(ctx)
		if err != nil {
			return fmt.Errorf("allocating repo: %w", err)
		}
		w.LeasedRepoName = repoName
	}

	w.RepoOwner = w.Org
	w.RepoName = w.LeasedRepoName
	w.RepoFull = w.Org + "/" + w.LeasedRepoName

	return ValidateSlotClean(w)
}

// bindLease points the scenario World at a leased slot's org: Org,
// Token, MintURL and the mint's status group and workflow hosts come
// from the lease, and SCM and CI are rebuilt from
// the lease's org client through World.OrgDrivers, so every scenario
// operation and its cleanup use the credentials of the org that owns
// the repo. A lease without an org (a driver that leases by name only)
// leaves the World's org and drivers unchanged.
func bindLease(w *world.World, lease install.RepoLease) error {
	if lease.Org == "" {
		return nil
	}
	if w.OrgDrivers != nil {
		if lease.Client == nil {
			return fmt.Errorf("binding lease %s/%s: lease carries no forge client", lease.Org, lease.Repo)
		}
		scmDriver, ciDriver, err := w.OrgDrivers(lease.Client, lease.Token)
		if err != nil {
			return fmt.Errorf("binding lease %s/%s: building org drivers: %w", lease.Org, lease.Repo, err)
		}
		w.SCM = scmDriver
		w.CI = ciDriver
	}
	w.Org = lease.Org
	if lease.Token != "" {
		w.Token = lease.Token
	}
	w.MintURL = lease.MintURL
	w.MintStatusGroup = lease.MintStatusGroup
	w.MintWorkflowHostRepos = lease.MintWorkflowHostRepos
	return nil
}

func givenEnrolledRepository(w *world.World, fullName string) error {
	owner, repo, err := scm.ParseRepo(fullName)
	if err != nil {
		return err
	}
	if owner != w.Org {
		return fmt.Errorf("repository owner %q does not match test org %q", owner, w.Org)
	}
	w.RepoFull = fullName
	w.RepoOwner = owner
	w.RepoName = repo
	return nil
}

func givenIssueWithTitleAndBody(w *world.World, title, bodyContains string) error {
	body := fmt.Sprintf("Behaviour test issue\n\n%s\n", bodyContains)
	return createIssue(w, title, body)
}

func givenIssue(w *world.World) error {
	title := fmt.Sprintf("behaviour-issue-%d", time.Now().UnixNano())
	body := "Behaviour test issue body with steps to reproduce for triage."
	return createIssue(w, title, body)
}

func createIssue(w *world.World, title, body string) error {
	if w.RepoOwner == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before creating issues")
	}
	trigger := time.Now()
	issue, err := w.SCM.CreateIssue(context.Background(), w.RepoOwner, w.RepoName, title, body)
	if err != nil {
		return err
	}
	w.IssueNumber = issue.Number
	w.IssueTitle = title
	// fullsend.yaml triggers on issues opened and labeled. Drain the issue-open
	// run before applying ready-for-triage so the labeled dispatch is not skipped.
	if err := drainIssueOpenWorkflow(w, trigger); err != nil {
		return err
	}
	return nil
}

// issueOpenDrainSkewBuffer is subtracted from the trigger timestamp on
// retry to compensate for clock drift between the test runner and GitHub.
// 30 s covers typical NTP drift without matching stale runs from prior
// scenarios (pool repos are org-isolated).
const issueOpenDrainSkewBuffer = 30 * time.Second

// drainIssueOpenWorkflow waits for the fullsend.yaml workflow to process
// the issues.opened event. If the first attempt fails (typically due to
// GitHub Actions webhook delivery lag or clock skew between the runner
// and GitHub), it retries once with a relaxed trigger timestamp.
func drainIssueOpenWorkflow(w *world.World, trigger time.Time) error {
	ctx := context.Background()
	repo := w.RepoName
	file := install.PerRepoTriageWorkflow

	_, err := w.CI.WaitForWorkflow(ctx, w.Org, repo, file, trigger, issueOpenEvent)
	if err == nil {
		return nil
	}

	// Retry with a clock-skew buffer. When the test runner's clock is
	// slightly ahead of GitHub's, the workflow run's CreatedAt falls
	// before our trigger timestamp and the first poll window misses it.
	worldLogf(w, "issue-open drain: retrying with skew buffer: %v", err)
	buffered := trigger.Add(-issueOpenDrainSkewBuffer)
	if _, retryErr := w.CI.WaitForWorkflow(ctx, w.Org, repo, file, buffered, issueOpenEvent); retryErr == nil {
		return nil
	}

	return fmt.Errorf("waiting for issue-open workflow: %w", err)
}

func whenIssueLabeled(w *world.World, label string) error {
	if w.IssueNumber == 0 {
		return fmt.Errorf("no issue created")
	}
	// Truncate to second precision: forge-reported workflow/pipeline
	// CreatedAt timestamps have second precision, but time.Now() carries
	// a fractional component. A run created in the same wall-clock second
	// as this trigger can therefore have a CreatedAt that parses earlier
	// than an untruncated boundary and be rejected by every creation-time
	// filter derived from it (CountHarnessDispatches, WaitForHarnessAgentRound)
	// on every retry, not just the first (#7957 review).
	w.ScenarioStart = time.Now().Truncate(time.Second)
	w.TriageTriggerEvent = issueOpenEvent
	return w.SCM.AddIssueLabels(context.Background(), w.RepoOwner, w.RepoName, w.IssueNumber, label)
}

func thenTriageWorkflowCompletes(w *world.World) error {
	return ensureTriageWorkflowComplete(w)
}

func thenIssueHasLabel(w *world.World, label string) error {
	return thenTargetLabel(w, targetIssue, label, true)
}
