package install

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

const (
	// settleMaxAttempts is how many times awaitWorkflowReady polls
	// GetWorkflow before giving up.
	settleMaxAttempts = 30

	// settlePoll is the delay between GetWorkflow polls.
	settlePoll = 5 * time.Second

	// resetMaxAttempts bounds the read-after-write retries around a
	// repo reset: GetRepo polls confirming deletion or creation, and
	// github setup re-runs after its first read 404s on a just-created
	// repo. With exponential backoff (2×) and a 1s initial delay, 5
	// attempts cover up to ~1+2+4+8 = 15s of API lag.
	resetMaxAttempts = 5
)

// resetRetryDelay is the initial delay for exponential backoff when
// polling GetRepo after delete or create. Doubled on each retry.
// Overridden in tests to avoid slow retry loops.
var resetRetryDelay = time.Second

// provisionGate serialises "inference provision" across every ensurer in
// the process. A cold pool (or one whose providers were removed) would
// otherwise send one provision per slot at once and hit GCP IAM 429s.
// Status reads do not take the gate.
var provisionGate = make(chan struct{}, 1)

// ensurer lazily creates and installs repos on demand for behaviour
// scenarios. Successful ensures are cached by org/repo key for the
// duration of a lease so duplicate EnsureRepo calls skip redundant
// work. DeleteRepo invalidates that cache so the next lease recreates
// the repo from scratch and cannot inherit leftover state.
//
// The resolved inference WIF provider is cached separately and survives
// DeleteRepo. Everything a per-repo provider depends on is keyed by the
// repo name, not its ID: the provider ID (mintcore.BuildRepoProviderID),
// its attribute condition (assertion.repository == 'org/repo') and the
// Vertex AI grant (attribute.repository/org/repo). A recreated repo
// therefore reuses the provider resolved for its name.
//
// This is an unexported interface used internally by composedDriver.
// The suite does not construct or reference it directly.
//
// Thread safety: EnsureRepo and DeleteRepo are safe for concurrent
// callers. A singleflight.Group serializes in-flight ensures per key
// so that concurrent first-calls for the same repo only perform
// create+install once; other callers wait and share the result.
type ensurer interface {
	// EnsureRepo guarantees org/repoName exists and has fullsend installed.
	// If the repo already exists it is deleted and recreated so the
	// scenario starts from a clean base (the forge's auto_init provides
	// the initial commit). Then the per-repo install flow runs
	// (inference WIF resolution + github setup).
	EnsureRepo(ctx context.Context, org, repoName string) error

	// DeleteRepo removes org/repoName (and a leftover org/repoName-fork
	// if present) and invalidates the ensure cache for that key so the
	// next EnsureRepo recreates it. Idempotent: a missing repo is not
	// an error.
	DeleteRepo(ctx context.Context, org, repoName string) error
}

// SettleFunc is called after a repo is freshly created or installed to
// wait until GitHub Actions recognises the workflow file. The default
// implementation polls GetWorkflow; tests inject a no-op.
type SettleFunc func(ctx context.Context, client forge.Client, org, repo, workflowFile string, logf func(string, ...any)) error

// InstallHooks lets a specialized driver (e.g. PlaybackDriver) run extra
// work around the standard install flow without duplicating it.
// BeforeInstall runs after repo creation but before github setup; its
// return value is threaded to AfterInstall, which runs after setup and
// post-install validation both succeed. Either func may be nil.
type InstallHooks struct {
	BeforeInstall func(ctx context.Context, client forge.Client, org, repo string) (any, error)
	AfterInstall  func(ctx context.Context, client forge.Client, org, repo string, state any) error
}

type repoEnsurer struct {
	e2eCfg    e2etest.EnvConfig
	client    forge.Client
	token     string
	binary    string
	logf      func(string, ...any)
	runCLI    CLIRunnerFunc // injectable; defaults to e2etest.TryRunCLI
	settle    SettleFunc    // injectable; defaults to awaitWorkflowReady
	setupOpts common.GitHubSetupOpts
	hooks     InstallHooks
	// actorGrants are verified once per org (membership + all-repository roles).
	actorGrants   []actorGrant
	outsiderLogin string

	mu      sync.Mutex
	ensured map[string]struct{} // keyed by org/repo; only successful results cached
	// wifProviders caches the resolved inference WIF provider by org/repo.
	// Unlike ensured, DeleteRepo does not clear it (see ensurer).
	// Lazily initialised under mu.
	wifProviders map[string]string
	verifiedOrgs map[string]struct{} // keyed by org; org-level actor access already checked
	inflight     singleflight.Group
}

// newRepoEnsurer returns an ensurer backed by the given forge client
// and CLI binary. The ensurer shares the same credentials and
// configuration as the per-repo install driver. BEHAVIOUR_CONFIG_PRESET
// is applied onto the vendored-mode defaults when set.
//
// Returns an error if TEST_ACTOR_OUTSIDER_PAT is set but its login cannot
// be resolved — outsider exclusion is a security invariant (#7777) and
// must not silently fall back to skipping the check.
func newRepoEnsurer(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	logf func(string, ...any),
) (ensurer, error) {
	opts := common.DefaultGitHubSetupOpts()
	opts.ConfigPreset = envConfigPreset()
	return newRepoEnsurerWithOpts(e2eCfg, client, token, binary, opts, logf)
}

// newRepoEnsurerWithOpts returns an ensurer like newRepoEnsurer but with
// custom GitHubSetupOpts. Used by the STAGE driver for non-vendored
// installs with a fullsend-ref, and by playback driver construction to
// install with the "dummy-playback" runtime. An optional InstallHooks
// may be passed to run extra work around the install (e.g. the playback
// tracking-issue setup); omit it for the normal no-op behaviour.
func newRepoEnsurerWithOpts(
	e2eCfg e2etest.EnvConfig,
	client forge.Client,
	token, binary string,
	opts common.GitHubSetupOpts,
	logf func(string, ...any),
	hooks ...InstallHooks,
) (ensurer, error) {
	outsiderLogin, err := outsiderLoginFromEnv(context.Background())
	if err != nil {
		return nil, fmt.Errorf("resolving outsider actor: %w", err)
	}
	var installHooks InstallHooks
	if len(hooks) > 0 {
		installHooks = hooks[0]
	}
	return &repoEnsurer{
		e2eCfg:        e2eCfg,
		client:        client,
		token:         token,
		binary:        binary,
		logf:          logf,
		runCLI:        e2etest.TryRunCLI,
		settle:        awaitWorkflowReady,
		setupOpts:     opts,
		hooks:         installHooks,
		actorGrants:   actorGrantsFromEnv(context.Background(), logf),
		outsiderLogin: outsiderLogin,
		ensured:       make(map[string]struct{}),
		verifiedOrgs:  make(map[string]struct{}),
	}, nil
}

func (e *repoEnsurer) EnsureRepo(ctx context.Context, org, repoName string) error {
	key := org + "/" + repoName

	e.mu.Lock()
	if _, ok := e.ensured[key]; ok {
		e.mu.Unlock()
		e.logf("[ensure] %s already ensured this lease, skipping", key)
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

// DeleteRepo removes the leased pool base (and any leftover fork) after
// a scenario so the next lessee cannot inherit labels, branches, PRs,
// workflow runs, or config drift. The ensure cache is invalidated
// before the delete so a failed delete still forces the next
// EnsureRepo to reset+recreate. The WIF provider cache is kept: the
// provider is keyed by repo name, so the recreated repo reuses it.
func (e *repoEnsurer) DeleteRepo(ctx context.Context, org, repoName string) error {
	key := org + "/" + repoName
	e.mu.Lock()
	delete(e.ensured, key)
	e.mu.Unlock()

	target := org + "/" + repoName
	e.logf("[ensure] deleting %s after lease", target)
	return e.resetRepo(ctx, org, repoName, target)
}

// doEnsure performs the actual create-if-missing + install work.
// It always resets and reinstalls the repo so that pool repos use the
// binary or ref from the current checkout. In vendored mode the
// current binary is pushed to the pool repo; in non-vendored mode the
// shim references the remote ref configured in setupOpts. Without
// this reset, leased repos that pass post-install validation keep a
// stale install from a prior run, silently missing dispatch fixes on
// the current branch.
func (e *repoEnsurer) doEnsure(ctx context.Context, org, repoName string) error {
	target := org + "/" + repoName

	// Step 1: delete the existing repo to reset accumulated git history.
	// Pool repos grow to GB scale from repeated test runs; the pre-review
	// shallow-clone deepening step takes 12+ minutes fetching bloated
	// history. Deleting and recreating gives a clean single-commit repo.
	if err := e.resetRepo(ctx, org, repoName, target); err != nil {
		return err
	}

	// Step 2: create repo (needed after reset, or if it never existed).
	if err := e.ensureRepoExists(ctx, org, repoName, target); err != nil {
		return err
	}

	// Verify org-level actor access before install. Direct collaborator
	// grants are not used: they vanish when resetRepo deletes the repo
	// and re-adding them creates pending invitations (#7777).
	if err := e.verifyActors(ctx, org); err != nil {
		return err
	}

	// Step 3: the repo is always freshly created (step 1 deleted any
	// prior version), so fullsend is never pre-installed. Run the full
	// install flow and settle for Actions readiness.
	e.logf("[ensure] %s needs install (fresh repo)", target)

	var hookState any
	if e.hooks.BeforeInstall != nil {
		var err error
		hookState, err = e.hooks.BeforeInstall(ctx, e.client, org, repoName)
		if err != nil {
			return fmt.Errorf("pre-install hook for %s: %w", target, err)
		}
	}

	// Step 4: run github setup to install fullsend and push the
	// current binary/ref.
	if err := e.installFullsend(ctx, org, repoName, target); err != nil {
		return err
	}

	// Select the appropriate post-install validator based on install mode
	// and the runtime the repo was installed with (defaults to "dummy").
	expectedRuntime := e.setupOpts.Runtime
	var validateErr error
	if e.setupOpts.Vendor {
		validateErr = ValidatePerRepoPostInstallWithRuntime(ctx, e.client, org, repoName, expectedRuntime)
	} else {
		validateErr = ValidatePerRepoPostInstallNonVendoredWithRuntime(ctx, e.client, org, repoName, expectedRuntime)
	}
	if validateErr != nil {
		return fmt.Errorf("post-install validation for %s: %w", target, validateErr)
	}

	if e.hooks.AfterInstall != nil {
		if err := e.hooks.AfterInstall(ctx, e.client, org, repoName, hookState); err != nil {
			return fmt.Errorf("post-install hook for %s: %w", target, err)
		}
	}

	// Step 5: wait for Actions to recognise the workflow file.
	if e.settle != nil {
		if err := e.settle(ctx, e.client, org, repoName, PerRepoTriageWorkflow, e.logf); err != nil {
			return fmt.Errorf("waiting for Actions readiness on %s: %w", target, err)
		}
	}

	return nil
}

// resetRepo deletes an existing repo to clear accumulated git history.
// Pool repos grow to gigabyte scale from repeated test runs; the
// pre-review shallow-clone deepening step takes 12+ minutes fetching
// the bloated history. A fresh repo starts with just the auto_init
// commit. No-op when the repo does not exist.
//
// Fork repos derived from the source (e.g. test-repo-01-fork) are
// deleted first. When a source repo is deleted and recreated, existing
// forks become orphaned — the fork creation step then fails because
// the fork repo exists but isn't a valid fork of the new source.
func (e *repoEnsurer) resetRepo(ctx context.Context, org, repoName, target string) error {
	// Delete fork repos before the source so they don't become orphaned.
	forkName := repoName + "-fork"
	forkTarget := org + "/" + forkName
	if _, forkErr := e.client.GetRepo(ctx, org, forkName); forkErr == nil {
		e.logf("[ensure] deleting fork %s before source reset", forkTarget)
		if err := e.client.DeleteRepo(ctx, org, forkName); err != nil {
			if !forge.IsNotFound(err) {
				return fmt.Errorf("deleting fork repo %s for reset: %w", forkTarget, err)
			}
		} else {
			if err := e.awaitDeletion(ctx, org, forkName, forkTarget); err != nil {
				return err
			}
		}
	} else if !forge.IsNotFound(forkErr) {
		return fmt.Errorf("checking fork repo %s for reset: %w", forkTarget, forkErr)
	}

	_, err := e.client.GetRepo(ctx, org, repoName)
	if err != nil {
		if forge.IsNotFound(err) {
			e.logf("[ensure] %s does not exist, nothing to delete", target)
			return nil
		}
		return fmt.Errorf("checking repo %s for reset: %w", target, err)
	}

	e.logf("[ensure] deleting %s", target)
	if err := e.client.DeleteRepo(ctx, org, repoName); err != nil {
		if forge.IsNotFound(err) {
			return nil // race: deleted between check and delete
		}
		return fmt.Errorf("deleting repo %s for history reset: %w", target, err)
	}

	// Wait for GitHub API to propagate the deletion so CreateRepo can
	// succeed on the first attempt. ensureRepoExists still retries on
	// already-exists if this poll times out with a stale GetRepo (#7839).
	return e.awaitDeletion(ctx, org, repoName, target)
}

// awaitDeletion polls GetRepo with exponential backoff until the repo
// returns 404, confirming the deletion has propagated through the
// GitHub API's eventual-consistency layer. If the repo is still
// visible after all attempts the function returns nil anyway —
// ensureRepoExists always calls CreateRepo and retries on
// already-exists rather than treating a stale GetRepo success as
// allocation-ready.
func (e *repoEnsurer) awaitDeletion(ctx context.Context, org, repoName, target string) error {
	e.logf("[ensure] waiting for %s deletion to propagate", target)
	delay := resetRetryDelay
	for attempt := 1; attempt <= resetMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled while waiting for %s deletion: %w", target, err)
		}
		_, err := e.client.GetRepo(ctx, org, repoName)
		if err != nil {
			if forge.IsNotFound(err) {
				e.logf("[ensure] %s deletion confirmed after %d attempt(s)", target, attempt)
				return nil
			}
			return fmt.Errorf("checking deletion of %s: %w", target, err)
		}
		if attempt < resetMaxAttempts {
			e.logf("[ensure] %s still visible, attempt %d/%d — backing off %v", target, attempt, resetMaxAttempts, delay)
			if ctx.Err() != nil {
				return fmt.Errorf("context cancelled while waiting for %s deletion: %w", target, ctx.Err())
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while waiting for %s deletion: %w", target, ctx.Err())
			case <-time.After(delay):
			}
			delay *= 2
		}
	}
	e.logf("[ensure] %s still visible after %d attempts; proceeding to create", target, resetMaxAttempts)
	return nil
}

// ensureRepoExists creates the repo after reset. The forge's CreateRepo
// uses auto_init, so GitHub creates an initial commit with a README —
// no explicit seeding is needed.
//
// A successful GetRepo after delete is not treated as "already ready":
// GitHub can keep serving a stale repo object for a name whose deletion
// is still propagating (#7839). Always CreateRepo; on already-exists,
// delete the repo that is actually blocking creation (resetRepo may
// have skipped its delete on a stale 404, or a retried create request
// may have succeeded server-side despite a client-side timeout) and
// retry until the name is free or attempts are exhausted.
func (e *repoEnsurer) ensureRepoExists(ctx context.Context, org, repoName, target string) error {
	e.logf("[ensure] creating %s (auto_init provides initial commit)", target)
	var lastErr error
	delay := resetRetryDelay
	for attempt := 1; attempt <= resetMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled while creating %s: %w", target, err)
		}

		_, createErr := e.client.CreateRepo(ctx, org, repoName, "Behaviour test repo", false)
		if createErr == nil {
			// Wait for the newly created repo to be visible via the API.
			// GitHub's eventual consistency means operations on a just-created
			// repo can 404 until propagation completes.
			return e.awaitCreation(ctx, org, repoName, target)
		}
		lastErr = createErr
		if !forge.IsAlreadyExists(createErr) {
			return fmt.Errorf("creating repo %s: %w", target, createErr)
		}
		if attempt < resetMaxAttempts {
			e.logf("[ensure] %s name still taken after reset, attempt %d/%d — deleting and retrying",
				target, attempt, resetMaxAttempts)
			if err := e.deleteBlockingRepo(ctx, org, repoName, target); err != nil {
				return err
			}
			// deleteBlockingRepo's awaitDeletion can return immediately when
			// GetRepo already 404s, even though CreateRepo can keep
			// rejecting the name as taken for a few seconds afterward — a
			// GetRepo 404 is not proof the name is free for CreateRepo any
			// more than a GetRepo success is proof it's still taken (#7839's
			// mirror case). Back off explicitly so this retry never fires
			// back-to-back with the previous one.
			e.logf("[ensure] %s still taken after delete, backing off %v before retrying create", target, delay)
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while creating %s: %w", target, ctx.Err())
			case <-time.After(delay):
			}
			delay *= 2
		}
	}
	return fmt.Errorf("creating repo %s: name still taken after %d attempts following reset: %w", target, resetMaxAttempts, lastErr)
}

// deleteBlockingRepo removes repoName (and a leftover org/repoName-fork,
// the same way resetRepo does) after CreateRepo reports it already
// exists. Without this, an already-exists error can never clear: the
// name stays taken forever and every retry just re-hits the same
// error, so the allocation fails slowly instead of recovering (#7839).
//
// A 404 on delete is treated as success — a concurrent deleter (or the
// original resetRepo delete finally propagating) may have already won
// the race. Waits for the deletion to propagate via awaitDeletion
// before the caller retries CreateRepo.
func (e *repoEnsurer) deleteBlockingRepo(ctx context.Context, org, repoName, target string) error {
	forkName := repoName + "-fork"
	forkTarget := org + "/" + forkName
	if _, forkErr := e.client.GetRepo(ctx, org, forkName); forkErr == nil {
		e.logf("[ensure] deleting fork %s before recreate retry", forkTarget)
		if err := e.client.DeleteRepo(ctx, org, forkName); err != nil {
			if !forge.IsNotFound(err) {
				return fmt.Errorf("deleting fork repo %s for recreate retry: %w", forkTarget, err)
			}
		} else if err := e.awaitDeletion(ctx, org, forkName, forkTarget); err != nil {
			return err
		}
	} else if !forge.IsNotFound(forkErr) {
		return fmt.Errorf("checking fork repo %s for recreate retry: %w", forkTarget, forkErr)
	}

	e.logf("[ensure] deleting %s (blocking recreate) before retry", target)
	if err := e.client.DeleteRepo(ctx, org, repoName); err != nil && !forge.IsNotFound(err) {
		return fmt.Errorf("deleting repo %s for recreate retry: %w", target, err)
	}
	return e.awaitDeletion(ctx, org, repoName, target)
}

// awaitCreation polls GetRepo with exponential backoff until the
// newly created repo is visible via the API. GitHub's eventual
// consistency means operations on a just-created repo can return 404
// until propagation completes.
//
// NOTE: This function confirms repo visibility only — not dispatch-side
// permission readiness. Do not add GetCollaboratorPermission polling
// here; it will not work. See the package doc comment in doc.go for the
// credential context separation that makes suite-side permission
// probing unreliable.
func (e *repoEnsurer) awaitCreation(ctx context.Context, org, repoName, target string) error {
	e.logf("[ensure] waiting for %s creation to propagate", target)
	delay := resetRetryDelay
	for attempt := 1; attempt <= resetMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled while waiting for %s creation: %w", target, err)
		}
		_, err := e.client.GetRepo(ctx, org, repoName)
		if err == nil {
			e.logf("[ensure] %s creation confirmed after %d attempt(s)", target, attempt)
			return nil
		}
		if !forge.IsNotFound(err) {
			return fmt.Errorf("checking creation of %s: %w", target, err)
		}
		if attempt < resetMaxAttempts {
			e.logf("[ensure] %s not yet visible, attempt %d/%d — backing off %v", target, attempt, resetMaxAttempts, delay)
			if ctx.Err() != nil {
				return fmt.Errorf("context cancelled while waiting for %s creation: %w", target, ctx.Err())
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while waiting for %s creation: %w", target, ctx.Err())
			case <-time.After(delay):
			}
			delay *= 2
		}
	}
	return fmt.Errorf("repo %s not visible after %d attempts following creation", target, resetMaxAttempts)
}

// installFullsend resolves the inference WIF provider (when a GCP
// project is configured) and runs fullsend github setup for the target
// repo.
//
// awaitCreation confirms the repo through the suite's GetRepo, but
// github setup is a separate CLI process whose first GetRepo (in
// applyPerRepoScaffold) can still 404 on GitHub's read-after-create
// lag. Retry only that specific failure, using the same bounded
// backoff as awaitCreation. Any other setup error is a single attempt.
//
// Retrying is safe only because setup makes no writes before that
// GetRepo on any path the driver uses (vendored, --fullsend-ref,
// --config, STAGE); the calls before it are reads such as the existing
// config and token scopes. If setup ever writes before that read, this
// retry must be revisited.
// The WIF resolver runs inside each attempt but hits the per-name cache
// after the first, so a retry adds no inference CLI calls.
func (e *repoEnsurer) installFullsend(ctx context.Context, _, _, target string) error {
	opts := e.setupOpts
	opts.ResolveWIFProvider = func(target, project string) (string, error) {
		return e.resolveWIFProvider(ctx, target, project)
	}

	delay := resetRetryDelay
	var lastErr error
	for attempt := 1; attempt <= resetMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled while running github setup for %s: %w", target, err)
		}

		err := common.RunGitHubSetupWithOpts(e.binary, e.token, target, e.e2eCfg.MintURL, e.e2eCfg.GCPProjectID, opts, e.runCLI, e.logf)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isGitHubSetupRepoInfo404(err) {
			return err
		}
		if attempt == resetMaxAttempts {
			break
		}
		e.logf("[ensure] github setup for %s hit the read-after-create 404, attempt %d/%d — re-running setup after %v", target, attempt, resetMaxAttempts, delay)
		if ctx.Err() != nil {
			return fmt.Errorf("context cancelled while retrying github setup for %s: %w", target, ctx.Err())
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while retrying github setup for %s: %w", target, ctx.Err())
		case <-time.After(delay):
		}
		delay *= 2
	}
	return fmt.Errorf("github setup for %s still hit the read-after-create 404 after %d attempts: %w", target, resetMaxAttempts, lastErr)
}

// isGitHubSetupRepoInfo404 reports whether err is github setup's
// read-after-create GetRepo 404 ("getting repo info: get repo …: 404 Not Found").
// Matching both substrings keeps every other setup failure as a single
// attempt. The CLI error is text from a subprocess, not forge.ErrNotFound.
func isGitHubSetupRepoInfo404(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "getting repo info: get repo ") && strings.Contains(msg, "404 Not Found")
}

// resolveWIFProvider returns the inference WIF provider for target. A
// cached value is returned without any CLI call. On a miss it reads
// "inference status"; only when that is not healthy does it run
// "inference provision" (then status again), holding provisionGate so at
// most one provision runs in the process at a time.
func (e *repoEnsurer) resolveWIFProvider(ctx context.Context, target, project string) (string, error) {
	e.mu.Lock()
	cached, ok := e.wifProviders[target]
	e.mu.Unlock()
	if ok {
		e.logf("[ensure] reusing cached WIF provider for %s: %s", target, cached)
		return cached, nil
	}
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("resolving inference WIF provider for %s: %w", target, err)
	}

	wifProvider, err := common.InferenceStatusWIFProvider(e.binary, e.token, target, project, e.runCLI, e.logf)
	if err != nil {
		e.logf("[ensure] no healthy WIF provider for %s, provisioning: %v", target, err)
		return e.provisionWIFProvider(ctx, target, project)
	}

	e.cacheWIFProvider(target, wifProvider)
	return wifProvider, nil
}

// provisionWIFProvider runs "inference provision" for target while
// holding provisionGate. The cache is re-checked once the gate is held,
// and a successful result is cached before the gate is released, so a
// caller that waited behind a provision for the same name reuses it.
func (e *repoEnsurer) provisionWIFProvider(ctx context.Context, target, project string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("waiting to provision inference for %s: %w", target, err)
	}
	select {
	case provisionGate <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("waiting to provision inference for %s: %w", target, ctx.Err())
	}
	defer func() { <-provisionGate }()

	e.mu.Lock()
	cached, ok := e.wifProviders[target]
	e.mu.Unlock()
	if ok {
		return cached, nil
	}
	wifProvider, err := common.ProvisionInference(e.binary, e.token, target, project, e.runCLI, e.logf)
	if err != nil {
		return "", err
	}
	e.cacheWIFProvider(target, wifProvider)
	return wifProvider, nil
}

func (e *repoEnsurer) cacheWIFProvider(target, wifProvider string) {
	e.mu.Lock()
	if e.wifProviders == nil {
		e.wifProviders = make(map[string]string)
	}
	e.wifProviders[target] = wifProvider
	e.mu.Unlock()
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
