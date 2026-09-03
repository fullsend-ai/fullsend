package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type repoEnsurer struct {
	e2eCfg      e2etest.EnvConfig
	client      forge.Client
	token       string
	binary      string
	fullsendRef string
	logf        func(string, ...any)
	runCLI      CLIRunnerFunc
	settle      SettleFunc
}

func newRepoEnsurer(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary, fullsendRef string,
	logf func(string, ...any),
) ensurer {
	return &repoEnsurer{
		e2eCfg:      e2eCfg,
		client:      client,
		token:       token,
		binary:      binary,
		fullsendRef: fullsendRef,
		logf:        logf,
		runCLI:      e2etest.TryRunCLI,
		settle:      awaitWorkflowReady,
	}
}

func generateRepoName(hint string) string {
	h := sha256.Sum256([]byte(hint))
	return fmt.Sprintf("bt-%s-%s", uuid.New().String()[:8], hex.EncodeToString(h[:4]))
}

func scenarioHash(hint string) string {
	h := sha256.Sum256([]byte(hint))
	return hex.EncodeToString(h[:4])
}

func (e *repoEnsurer) CreateRepo(ctx context.Context, org, hint string) (string, error) {
	repoName := generateRepoName(hint)
	target := org + "/" + repoName

	description := fmt.Sprintf("Behaviour test: %s", hint)
	e.logf("[ensure] creating %s (auto_init provides initial commit)", target)
	if _, err := e.client.CreateRepo(ctx, org, repoName, description, false); err != nil {
		return "", fmt.Errorf("creating repo %s: %w", target, err)
	}

	if err := e.awaitGitReady(ctx, org, repoName, target); err != nil {
		return "", err
	}

	if err := e.installFullsend(org, repoName, target); err != nil {
		return "", err
	}

	if err := ValidatePostInstall(ctx, e.client, org, repoName); err != nil {
		return "", fmt.Errorf("post-install validation for %s: %w", target, err)
	}

	if e.settle != nil {
		if err := e.settle(ctx, e.client, org, repoName, TriageWorkflow, e.logf); err != nil {
			return "", fmt.Errorf("waiting for Actions readiness on %s: %w", target, err)
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

func (e *repoEnsurer) installFullsend(org, repoName, target string) error {
	fullTarget := org + "/" + repoName
	return common.RunReposInstall(
		e.binary, e.token, fullTarget,
		e.e2eCfg.MintURL, e.fullsendRef, e.e2eCfg.GCPProjectID, e.e2eCfg.WIFProvider,
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
