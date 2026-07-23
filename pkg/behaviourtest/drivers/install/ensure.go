package install

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/e2etest"
)

// RepoEnsurer lazily creates and installs repos on demand for behaviour
// scenarios. Results are cached by repo name so that a second scenario
// leasing the same name within a suite run skips redundant work.
//
// Thread safety: EnsureRepo is safe for concurrent callers. The cache
// is guarded by a mutex; the underlying create+install operations are
// idempotent, so concurrent first-calls for the same repo may both run
// the install but both succeed.
type RepoEnsurer interface {
	// EnsureRepo guarantees org/repoName exists and has fullsend installed.
	// If the repo does not exist it is created and seeded with an initial
	// commit. If fullsend is not installed (per post-install validation)
	// it runs the per-repo install flow (inference provision + github setup).
	EnsureRepo(ctx context.Context, org, repoName string) (State, error)
}

type repoEnsurer struct {
	e2eCfg e2etest.EnvConfig
	client forge.Client
	token  string
	binary string
	logf   func(string, ...any)

	mu      sync.Mutex
	ensured map[string]State // keyed by repo name; only successful results cached
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
		ensured: make(map[string]State),
	}
}

func (e *repoEnsurer) EnsureRepo(ctx context.Context, org, repoName string) (State, error) {
	e.mu.Lock()
	if st, ok := e.ensured[repoName]; ok {
		e.mu.Unlock()
		e.logf("[ensure] %s/%s already ensured this run, skipping", org, repoName)
		return st, nil
	}
	e.mu.Unlock()

	st, err := e.doEnsure(ctx, org, repoName)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	// Another goroutine may have raced and cached a result; prefer the
	// first successful result but either is correct.
	if existing, ok := e.ensured[repoName]; ok {
		e.mu.Unlock()
		return existing, nil
	}
	e.ensured[repoName] = st
	e.mu.Unlock()

	return st, nil
}

// doEnsure performs the actual create-if-missing + install-if-needed work.
func (e *repoEnsurer) doEnsure(ctx context.Context, org, repoName string) (State, error) {
	target := org + "/" + repoName

	// Step 1: create repo if it does not exist.
	if err := e.ensureRepoExists(ctx, org, repoName, target); err != nil {
		return nil, err
	}

	// Step 2: install fullsend if post-install validation fails.
	if installErr := validatePerRepoPostInstall(ctx, e.client, org, repoName); installErr != nil {
		e.logf("[ensure] %s needs install (validation: %v)", target, installErr)
		if err := e.installFullsend(ctx, org, repoName, target); err != nil {
			return nil, err
		}
		if err := validatePerRepoPostInstall(ctx, e.client, org, repoName); err != nil {
			return nil, fmt.Errorf("post-install validation for %s: %w", target, err)
		}
	} else {
		e.logf("[ensure] %s already installed, skipping", target)
	}

	return &perRepoState{org: org, repo: repoName}, nil
}

// ensureRepoExists creates the repo and seeds an initial commit if it
// does not already exist. Idempotent: a repo that already exists is
// left untouched.
func (e *repoEnsurer) ensureRepoExists(ctx context.Context, org, repoName, target string) error {
	_, err := e.client.GetRepo(ctx, org, repoName)
	if err == nil {
		return nil // repo exists
	}
	if !forge.IsNotFound(err) {
		return fmt.Errorf("checking repo %s: %w", target, err)
	}

	e.logf("[ensure] creating %s", target)
	if _, createErr := e.client.CreateRepo(ctx, org, repoName, "Behaviour test repo", false); createErr != nil {
		return fmt.Errorf("creating repo %s: %w", target, createErr)
	}

	e.logf("[ensure] seeding %s with initial commit", target)
	readme := fmt.Appendf(nil, "# %s\n\nBehaviour test repository.\n", repoName)
	if seedErr := e.client.CreateFile(ctx, org, repoName, "README.md",
		"chore: initialize repo for behaviour testing", readme); seedErr != nil {
		return fmt.Errorf("seeding repo %s: %w", target, seedErr)
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
	if _, err := e2etest.TryRunCLI(e.binary, e.token, args...); err != nil {
		return fmt.Errorf("github setup %s: %w", target, err)
	}
	return nil
}

// provisionInference creates repo-scoped inference WIF for target and
// returns the provider resource name. Mirrors
// perRepoDriver.provisionPerRepoInference.
func (e *repoEnsurer) provisionInference(target, project string) (string, error) {
	provisionArgs := []string{"inference", "provision", target, "--project", project}
	e.logf("[ensure] running fullsend %s", strings.Join(provisionArgs, " "))
	if _, err := e2etest.TryRunCLI(e.binary, e.token, provisionArgs...); err != nil {
		return "", fmt.Errorf("inference provision %s: %w", target, err)
	}

	statusArgs := []string{"inference", "status", target, "--project", project, "--format", "json"}
	e.logf("[ensure] running fullsend %s", strings.Join(statusArgs, " "))
	out, err := e2etest.TryRunCLI(e.binary, e.token, statusArgs...)
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
