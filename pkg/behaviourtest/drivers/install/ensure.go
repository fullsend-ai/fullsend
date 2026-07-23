package install

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

const (
	// settleMaxAttempts is how many times awaitWorkflowReady polls
	// GetWorkflow before giving up.
	settleMaxAttempts = 30

	// settlePoll is the delay between GetWorkflow polls.
	settlePoll = 5 * time.Second
)

// RepoEnsurer lazily creates and installs repos on demand for behaviour
// scenarios. Results are cached by org/repo key so that a second scenario
// leasing the same name within a suite run skips redundant work.
//
// Thread safety: EnsureRepo is safe for concurrent callers.
// A singleflight.Group serializes in-flight ensures per key so that
// concurrent first-calls for the same repo only perform create+install
// once; other callers wait and share the result.
type RepoEnsurer interface {
	// EnsureRepo guarantees org/repoName exists and has fullsend installed.
	// If the repo does not exist it is created (the forge's auto_init
	// provides the initial commit). If fullsend is not installed (per
	// post-install validation) it runs the per-repo install flow
	// (inference provision + github setup).
	EnsureRepo(ctx context.Context, org, repoName string) (State, error)
}

// CLIRunnerFunc is the signature for running a fullsend CLI command.
// The default implementation is e2etest.TryRunCLI. Inject a custom
// function in tests to avoid shelling out.
type CLIRunnerFunc func(binary, token string, args ...string) (string, error)

// SettleFunc is called after a repo is freshly created or installed to
// wait until GitHub Actions recognises the workflow file. The default
// implementation polls GetWorkflow; tests inject a no-op.
type SettleFunc func(ctx context.Context, client forge.Client, org, repo, workflowFile string, logf func(string, ...any)) error

type repoEnsurer struct {
	e2eCfg e2etest.EnvConfig
	client forge.Client
	token  string
	binary string
	logf   func(string, ...any)
	runCLI CLIRunnerFunc // injectable; defaults to e2etest.TryRunCLI
	settle SettleFunc    // injectable; defaults to awaitWorkflowReady

	mu       sync.Mutex
	ensured  map[string]State // keyed by org/repo; only successful results cached
	inflight singleflight.Group
}

// NewRepoEnsurer returns a RepoEnsurer backed by the given forge client
// and CLI binary. The ensurer shares the same credentials and
// configuration as the per-repo install driver.
func NewRepoEnsurer(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	logf func(string, ...any),
) RepoEnsurer {
	return &repoEnsurer{
		e2eCfg:  e2eCfg,
		client:  client,
		token:   token,
		binary:  binary,
		logf:    logf,
		runCLI:  e2etest.TryRunCLI,
		settle:  awaitWorkflowReady,
		ensured: make(map[string]State),
	}
}

func (e *repoEnsurer) EnsureRepo(ctx context.Context, org, repoName string) (State, error) {
	key := org + "/" + repoName

	e.mu.Lock()
	if st, ok := e.ensured[key]; ok {
		e.mu.Unlock()
		e.logf("[ensure] %s already ensured this run, skipping", key)
		return st, nil
	}
	e.mu.Unlock()

	// singleflight deduplicates concurrent callers for the same key so
	// only one goroutine runs doEnsure; others wait and share the result.
	v, err, _ := e.inflight.Do(key, func() (any, error) {
		// Re-check the cache inside the flight — a prior flight may
		// have populated it before this one started.
		e.mu.Lock()
		if st, ok := e.ensured[key]; ok {
			e.mu.Unlock()
			return st, nil
		}
		e.mu.Unlock()

		st, err := e.doEnsure(ctx, org, repoName)
		if err != nil {
			return nil, err
		}

		e.mu.Lock()
		e.ensured[key] = st
		e.mu.Unlock()

		return st, nil
	})
	if err != nil {
		return nil, err
	}

	return v.(State), nil
}

// doEnsure performs the actual create-if-missing + install-if-needed work.
func (e *repoEnsurer) doEnsure(ctx context.Context, org, repoName string) (State, error) {
	target := org + "/" + repoName

	// Step 1: create repo if it does not exist.
	if err := e.ensureRepoExists(ctx, org, repoName, target); err != nil {
		return nil, err
	}

	// Step 2: install fullsend if post-install validation fails.
	needsSettle := false
	if installErr := validatePerRepoPostInstall(ctx, e.client, org, repoName); installErr != nil {
		e.logf("[ensure] %s needs install (validation: %v)", target, installErr)
		if err := e.installFullsend(ctx, org, repoName, target); err != nil {
			return nil, err
		}
		if err := validatePerRepoPostInstall(ctx, e.client, org, repoName); err != nil {
			return nil, fmt.Errorf("post-install validation for %s: %w", target, err)
		}
		needsSettle = true
	} else {
		e.logf("[ensure] %s already installed, skipping", target)
	}

	// Step 3: wait for Actions to recognise the workflow file.
	// On freshly created/installed repos, GitHub Actions needs time to
	// index the workflow before it can dispatch events (e.g. issues).
	// For already-installed repos the first poll succeeds immediately.
	if needsSettle && e.settle != nil {
		if err := e.settle(ctx, e.client, org, repoName, perRepoTriageWorkflow, e.logf); err != nil {
			return nil, fmt.Errorf("waiting for Actions readiness on %s: %w", target, err)
		}
	}

	return &perRepoState{org: org, repo: repoName}, nil
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
// configured) and fullsend github setup for the target repo. Same
// semantics as perRepoDriver.Install.
func (e *repoEnsurer) installFullsend(_ context.Context, _, _, target string) error {
	args := []string{
		"github", "setup", target,
		"--vendor", "--direct",
		"--skip-app-setup",
		"--mint-url", e.e2eCfg.MintURL,
		"--runtime", "dummy",
	}

	if project := strings.TrimSpace(e.e2eCfg.GCPProjectID); project != "" {
		wifProvider, err := e.provisionInference(target, project)
		if err != nil {
			return err
		}
		args = append(args, "--inference-project", project, "--inference-wif-provider", wifProvider)
	}

	e.logf("[ensure] running fullsend %s", strings.Join(args, " "))
	if _, err := e.runCLI(e.binary, e.token, args...); err != nil {
		return fmt.Errorf("github setup %s: %w", target, err)
	}
	return nil
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

// provisionInference creates repo-scoped inference WIF for target and
// returns the provider resource name. Mirrors
// perRepoDriver.provisionPerRepoInference.
func (e *repoEnsurer) provisionInference(target, project string) (string, error) {
	provisionArgs := []string{"inference", "provision", target, "--project", project}
	e.logf("[ensure] running fullsend %s", strings.Join(provisionArgs, " "))
	if _, err := e.runCLI(e.binary, e.token, provisionArgs...); err != nil {
		return "", fmt.Errorf("inference provision %s: %w", target, err)
	}

	statusArgs := []string{"inference", "status", target, "--project", project, "--format", "json"}
	e.logf("[ensure] running fullsend %s", strings.Join(statusArgs, " "))
	out, err := e.runCLI(e.binary, e.token, statusArgs...)
	if err != nil {
		return "", fmt.Errorf("inference status %s: %w", target, err)
	}

	wifProvider, err := parseInferenceStatusWIFProvider(out)
	if err != nil {
		return "", fmt.Errorf("inference status %s: %w", target, err)
	}
	e.logf("[ensure] repo-scoped inference WIF provider: %s", wifProvider)
	return wifProvider, nil
}
