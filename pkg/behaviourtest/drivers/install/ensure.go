package install

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

const (
	// settleMaxAttempts is how many times awaitWorkflowReady polls
	// GetWorkflow before giving up.
	settleMaxAttempts = 30

	// gitReadyMaxAttempts is the number of GetRef polls to confirm
	// the git layer is ready after repo creation.
	gitReadyMaxAttempts = 15
)

// settlePoll is the delay between GetWorkflow polls. Overridden in tests.
var settlePoll = 5 * time.Second

// gitReadyPoll is the delay between GetRef polls. Overridden in tests.
var gitReadyPoll = 2 * time.Second

// ensurer creates ephemeral repos with fullsend installed. Each call
// generates a unique repo name, creates the repo, waits for git
// readiness, installs fullsend via repos install, validates, and waits
// for Actions to index the workflow.
//
// This is an unexported interface used internally by composedDriver.
type ensurer interface {
	CreateRepo(ctx context.Context, org, hint string) (repoName string, err error)
}

// SettleFunc is called after a repo is freshly created or installed to
// wait until GitHub Actions recognises the workflow file.
type SettleFunc func(ctx context.Context, client forge.Client, org, repo, workflowFile string, logf func(string, ...any)) error

// EventDeliveryFunc is called after a repo's workflow is indexed to
// verify that GitHub's event delivery pipeline is live end-to-end.
// issueNumber is a pre-created warmup issue on the repo.
type EventDeliveryFunc func(ctx context.Context, client forge.Client, org, repo string, issueNumber int, workflowFile string, logf func(string, ...any)) error

type repoEnsurer struct {
	e2eCfg        e2etest.EnvConfig
	client        forge.Client
	token         string
	binary        string
	vendorBinary  string
	fullsendRef   string
	logf          func(string, ...any)
	runCLI        CLIRunnerFunc
	settle        SettleFunc
	eventDelivery EventDeliveryFunc
	runTimestamp  string
}

func newRepoEnsurer(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary, vendorBinary, fullsendRef string,
	logf func(string, ...any),
) ensurer {
	return &repoEnsurer{
		e2eCfg:        e2eCfg,
		client:        client,
		token:         token,
		binary:        binary,
		vendorBinary:  vendorBinary,
		fullsendRef:   fullsendRef,
		logf:          logf,
		runCLI:        e2etest.TryRunCLI,
		settle:        awaitWorkflowReady,
		eventDelivery: awaitEventDelivery,
		runTimestamp:  time.Now().UTC().Format("20060102T150405"),
	}
}

func generateRepoName(runTimestamp string) string {
	return fmt.Sprintf("bt-%s-%s", runTimestamp, uuid.New().String()[:8])
}

func (e *repoEnsurer) CreateRepo(ctx context.Context, org, hint string) (string, error) {
	repoName := generateRepoName(e.runTimestamp)
	target := org + "/" + repoName

	description := fmt.Sprintf("Behaviour test: %s", hint)
	e.logf("[ensure] creating %s (auto_init provides initial commit)", target)
	if _, err := e.client.CreateRepo(ctx, org, repoName, description, false); err != nil {
		return "", fmt.Errorf("creating repo %s: %w", target, err)
	}

	if err := e.awaitGitReady(ctx, org, repoName, target); err != nil {
		return repoName, err
	}

	// Create a warmup issue BEFORE installing fullsend. No workflow
	// exists yet, so the issues.opened event is silently ignored —
	// no wasted triage run. The issue is used after install to verify
	// event delivery via a comment that triggers the shim but skips
	// the dispatch job (body does not start with /fs-).
	var warmupIssueNumber int
	if e.eventDelivery != nil {
		issue, err := e.client.CreateIssue(ctx, org, repoName, "warmup", "event delivery probe")
		if err != nil {
			return repoName, fmt.Errorf("creating warmup issue on %s: %w", target, err)
		}
		warmupIssueNumber = issue.Number
	}

	if err := e.installFullsend(org, repoName); err != nil {
		return repoName, err
	}

	if err := ValidatePostInstall(ctx, e.client, org, repoName); err != nil {
		return repoName, fmt.Errorf("post-install validation for %s: %w", target, err)
	}

	if e.settle != nil {
		if err := e.settle(ctx, e.client, org, repoName, TriageWorkflow, e.logf); err != nil {
			return repoName, fmt.Errorf("waiting for Actions readiness on %s: %w", target, err)
		}
	}

	if e.eventDelivery != nil {
		if err := e.eventDelivery(ctx, e.client, org, repoName, warmupIssueNumber, TriageWorkflow, e.logf); err != nil {
			return repoName, fmt.Errorf("verifying event delivery on %s: %w", target, err)
		}
	}

	return repoName, nil
}

// awaitGitReady polls GetRef("heads/main") until the git layer is ready.
// This fixes 422/409 errors that occur when the API reports the repo
// exists but the git layer hasn't initialized yet.
func (e *repoEnsurer) awaitGitReady(ctx context.Context, org, repoName, target string) error {
	e.logf("[ensure] waiting for git readiness on %s", target)
	for attempt := 1; attempt <= gitReadyMaxAttempts; attempt++ {
		_, err := e.client.GetRef(ctx, org, repoName, "heads/main")
		if err == nil {
			e.logf("[ensure] %s git ready after %d attempt(s)", target, attempt)
			return nil
		}

		if attempt < gitReadyMaxAttempts {
			if ctx.Err() != nil {
				return fmt.Errorf("context cancelled waiting for git readiness on %s: %w", target, ctx.Err())
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled waiting for git readiness on %s: %w", target, ctx.Err())
			case <-time.After(gitReadyPoll):
			}
		}
	}
	return fmt.Errorf("git layer on %s not ready after %d attempts", target, gitReadyMaxAttempts)
}

func (e *repoEnsurer) installFullsend(org, repoName string) error {
	fullTarget := org + "/" + repoName
	return common.RunReposInstall(
		e.binary, e.token, fullTarget,
		e.e2eCfg.MintURL, e.fullsendRef, e.e2eCfg.GCPProjectID, e.e2eCfg.WIFProvider, e.vendorBinary,
		e.runCLI, e.logf,
	)
}

// awaitWorkflowReady polls the forge's GetWorkflow API until the given
// workflow file is visible, or until the attempt limit is exhausted.
func awaitWorkflowReady(ctx context.Context, client forge.Client, org, repo, workflowFile string, logf func(string, ...any)) error {
	target := org + "/" + repo
	logf("[ensure] waiting for Actions to recognise %s on %s", workflowFile, target)

	for attempt := 1; attempt <= settleMaxAttempts; attempt++ {
		wf, err := client.GetWorkflow(ctx, org, repo, workflowFile)
		if err == nil && wf != nil {
			logf("[ensure] %s visible on %s after %d attempt(s) (state=%s)", workflowFile, target, attempt, wf.State)
			return nil
		}

		if attempt < settleMaxAttempts {
			if ctx.Err() != nil {
				return fmt.Errorf("context cancelled while waiting for %s on %s: %w", workflowFile, target, ctx.Err())
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while waiting for %s on %s: %w", workflowFile, target, ctx.Err())
			case <-time.After(settlePoll):
			}
		}
	}

	return fmt.Errorf("workflow %s not visible on %s after %d attempts", workflowFile, target, settleMaxAttempts)
}

// awaitEventDelivery posts a comment on a pre-created warmup issue and
// polls ListWorkflowRuns until a completed run appears, proving that
// GitHub's event delivery pipeline is live for this repo. The comment
// body does not start with /fs-, so the shim's dispatch job is skipped
// by its if: condition — no triage or agent work runs.
//
// On pre-existing repos (the old pool flow) the pipeline is already
// warm and the run appears in seconds. On freshly created ephemeral
// repos there can be a gap between workflow indexing and event delivery
// readiness; this function absorbs that gap.
func awaitEventDelivery(ctx context.Context, client forge.Client, org, repo string, issueNumber int, workflowFile string, logf func(string, ...any)) error {
	target := org + "/" + repo
	logf("[ensure] verifying event delivery on %s", target)

	probeTime := time.Now()

	if _, err := client.CreateIssueComment(ctx, org, repo, issueNumber, "fullsend event delivery probe"); err != nil {
		return fmt.Errorf("posting warmup comment on %s: %w", target, err)
	}

	for attempt := 1; attempt <= settleMaxAttempts; attempt++ {
		runs, err := client.ListWorkflowRuns(ctx, org, repo, workflowFile)
		if err == nil {
			for _, run := range runs {
				t, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
				if parseErr != nil {
					continue
				}
				if !t.Before(probeTime.Add(-30*time.Second)) && run.Status == "completed" {
					logf("[ensure] event delivery confirmed on %s after %d attempt(s) (run %d)", target, attempt, run.ID)
					return nil
				}
			}
		}

		if attempt < settleMaxAttempts {
			if ctx.Err() != nil {
				return fmt.Errorf("context cancelled while verifying event delivery on %s: %w", target, ctx.Err())
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while verifying event delivery on %s: %w", target, ctx.Err())
			case <-time.After(settlePoll):
			}
		}
	}

	return fmt.Errorf("event delivery not confirmed on %s after %d attempts", target, settleMaxAttempts)
}
