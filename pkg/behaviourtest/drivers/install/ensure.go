package install

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

const (
	// settleMaxAttempts is how many times awaitWorkflowReady polls
	// GetWorkflow before giving up.
	settleMaxAttempts = 30

	// settlePoll is the delay between GetWorkflow polls.
	settlePoll = 5 * time.Second
)

// ensurer lazily creates and installs repos on demand for behaviour
// scenarios. Results are cached by org/repo key so that a second scenario
// leasing the same name within a suite run skips redundant work.
//
// This is an unexported interface used internally by composedDriver.
// The suite does not construct or reference it directly.
//
// Thread safety: EnsureRepo is safe for concurrent callers.
// A singleflight.Group serializes in-flight ensures per key so that
// concurrent first-calls for the same repo only perform create+install
// once; other callers wait and share the result.
type ensurer interface {
	// EnsureRepo guarantees org/repoName exists and has fullsend installed.
	// If the repo does not exist it is created (the forge's auto_init
	// provides the initial commit). If fullsend is not installed (per
	// post-install validation) it runs the per-repo install flow
	// (inference provision + github setup).
	EnsureRepo(ctx context.Context, org, repoName string) error
}

// SettleFunc is called after a repo is freshly created or installed to
// wait until GitHub Actions recognises the workflow file. The default
// implementation polls GetWorkflow; tests inject a no-op.
type SettleFunc func(ctx context.Context, client forge.Client, org, repo, workflowFile string, logf func(string, ...any)) error

type repoEnsurer struct {
	e2eCfg    e2etest.EnvConfig
	client    forge.Client
	token     string
	binary    string
	logf      func(string, ...any)
	runCLI    CLIRunnerFunc // injectable; defaults to e2etest.TryRunCLI
	settle    SettleFunc    // injectable; defaults to awaitWorkflowReady
	setupOpts common.GitHubSetupOpts

	mu       sync.Mutex
	ensured  map[string]struct{} // keyed by org/repo; only successful results cached
	inflight singleflight.Group
}

// newRepoEnsurer returns an ensurer backed by the given forge client
// and CLI binary. The ensurer shares the same credentials and
// configuration as the per-repo install driver.
func newRepoEnsurer(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	logf func(string, ...any),
) ensurer {
	return &repoEnsurer{
		e2eCfg:    e2eCfg,
		client:    client,
		token:     token,
		binary:    binary,
		logf:      logf,
		runCLI:    e2etest.TryRunCLI,
		settle:    awaitWorkflowReady,
		setupOpts: common.DefaultGitHubSetupOpts(),
		ensured:   make(map[string]struct{}),
	}
}

// newRepoEnsurerWithOpts returns an ensurer like newRepoEnsurer but with
// custom GitHubSetupOpts. Used by the STAGE driver for non-vendored
// installs with a fullsend-ref.
func newRepoEnsurerWithOpts(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	opts common.GitHubSetupOpts,
	logf func(string, ...any),
) ensurer {
	return &repoEnsurer{
		e2eCfg:    e2eCfg,
		client:    client,
		token:     token,
		binary:    binary,
		logf:      logf,
		runCLI:    e2etest.TryRunCLI,
		settle:    awaitWorkflowReady,
		setupOpts: opts,
		ensured:   make(map[string]struct{}),
	}
}

func (e *repoEnsurer) EnsureRepo(ctx context.Context, org, repoName string) error {
	key := org + "/" + repoName

	e.mu.Lock()
	if _, ok := e.ensured[key]; ok {
		e.mu.Unlock()
		e.logf("[ensure] %s already ensured this run, skipping", key)
		return nil
	}
	e.mu.Unlock()

	// singleflight deduplicates concurrent callers for the same key so
	// only one goroutine runs doEnsure; others wait and share the result.
	_, err, _ := e.inflight.Do(key, func() (any, error) {
		// Re-check the cache inside the flight — a prior flight may
		// have populated it before this one started.
		e.mu.Lock()
		if _, ok := e.ensured[key]; ok {
			e.mu.Unlock()
			return nil, nil
		}
		e.mu.Unlock()

		if err := e.doEnsure(ctx, org, repoName); err != nil {
			return nil, err
		}

		e.mu.Lock()
		e.ensured[key] = struct{}{}
		e.mu.Unlock()

		return nil, nil
	})

	return err
}

// doEnsure performs the actual create-if-missing + install work.
// It always re-vendors the CLI binary so that pool repos run the
// binary built from the current checkout. Without this, leased repos
// that pass post-install validation keep a stale vendored binary from
// a prior run, silently missing dispatch fixes on the current branch.
func (e *repoEnsurer) doEnsure(ctx context.Context, org, repoName string) error {
	target := org + "/" + repoName

	// Step 1: create repo if it does not exist.
	if err := e.ensureRepoExists(ctx, org, repoName, target); err != nil {
		return err
	}

	// Step 2: check whether fullsend was previously installed. We always
	// re-run setup (step 3) to keep the binary or ref current, but skip
	// the settle wait when the workflow file already exists — GitHub
	// Actions already indexed it.
	validate := ValidatePerRepoPostInstall
	if !e.setupOpts.Vendor {
		validate = ValidatePerRepoPostInstallNonVendored
	}
	alreadyInstalled := validate(ctx, e.client, org, repoName) == nil
	if alreadyInstalled {
		e.logf("[ensure] %s already installed, re-running setup to keep current", target)
	} else {
		e.logf("[ensure] %s needs install", target)
	}

	// Step 3: always run github setup to push the current binary/ref.
	// Use the mint URL from e2eCfg — the suite sets this from the install
	// driver's result before creating the ensurer.
	if err := e.installFullsend(ctx, org, repoName, target); err != nil {
		return err
	}
	if err := validate(ctx, e.client, org, repoName); err != nil {
		return fmt.Errorf("post-install validation for %s: %w", target, err)
	}

	// Step 4: wait for Actions to recognise the workflow file only on
	// fresh installs. Re-vendors update the binary and workflow files
	// but GitHub Actions already indexed the workflow on the prior install.
	if !alreadyInstalled && e.settle != nil {
		if err := e.settle(ctx, e.client, org, repoName, PerRepoTriageWorkflow, e.logf); err != nil {
			return fmt.Errorf("waiting for Actions readiness on %s: %w", target, err)
		}
	}

	return nil
}

// ensureRepoExists creates the repo if it does not already exist.
// The forge's CreateRepo uses auto_init, so GitHub creates an initial
// commit with a README — no explicit seeding is needed.
// Idempotent: a repo that already exists is left untouched.
func (e *repoEnsurer) ensureRepoExists(ctx context.Context, org, repoName, target string) error {
	_, err := e.client.GetRepo(ctx, org, repoName)
	if err == nil {
		return nil // repo exists
	}
	if !forge.IsNotFound(err) {
		return fmt.Errorf("checking repo %s: %w", target, err)
	}

	e.logf("[ensure] creating %s (auto_init provides initial commit)", target)
	if _, createErr := e.client.CreateRepo(ctx, org, repoName, "Behaviour test repo", false); createErr != nil {
		return fmt.Errorf("creating repo %s: %w", target, createErr)
	}

	return nil
}

// installFullsend runs inference provision (when a GCP project is
// configured) and fullsend github setup for the target repo.
func (e *repoEnsurer) installFullsend(_ context.Context, _, _, target string) error {
	return common.RunGitHubSetupWithOpts(e.binary, e.token, target, e.e2eCfg.MintURL, e.e2eCfg.GCPProjectID, e.setupOpts, e.runCLI, e.logf)
}

// awaitWorkflowReady polls the forge's GetWorkflow API until the given
// workflow file is visible and in "active" state, or until the attempt
// limit is exhausted. On newly created repos, GitHub Actions takes a
// variable amount of time to index committed workflow files; events
// dispatched before the workflow is indexed are silently dropped.
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
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while waiting for %s on %s: %w", workflowFile, target, ctx.Err())
			case <-time.After(settlePoll):
			}
		}
	}

	return fmt.Errorf("workflow %s not visible on %s after %d attempts", workflowFile, target, settleMaxAttempts)
}
