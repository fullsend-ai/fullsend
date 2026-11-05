package install

import (
	"context"
	"errors"
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
	// freshly claimed slot repo: GetRepo polls confirming its creation
	// (or a leftover fork's deletion), and github setup re-runs after
	// its first read 404s on the just-created repo. With exponential
	// backoff (2×) and a 1s initial delay, 5 attempts cover up to
	// ~1+2+4+8 = 15s of API lag.
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

// ensurer installs fullsend into leased slot repos on demand for
// behaviour scenarios. The slot allocator (slotpool.go) creates a slot's
// repository when it claims the slot and deletes it when the lease is
// released; the ensurer never creates, deletes or resets a slot repo.
// An existing repository under a slot name belongs to whoever claimed
// it, so treating it as resettable would destroy another holder's lease.
// Successful ensures are cached by org/repo key for the duration of a
// lease so duplicate EnsureRepo calls skip redundant work. ReleaseRepo
// invalidates that cache so the next lease installs from scratch.
//
// The resolved inference WIF provider is cached separately and survives
// ReleaseRepo. Everything a per-repo provider depends on is keyed by the
// repo name, not its ID: the provider ID (mintcore.BuildRepoProviderID),
// its attribute condition (assertion.repository == 'org/repo') and the
// Vertex AI grant (attribute.repository/org/repo). A recreated repo
// therefore reuses the provider resolved for its name.
//
// This is an unexported interface used internally by composedDriver.
// The suite does not construct or reference it directly.
//
// Thread safety: EnsureRepo and ReleaseRepo are safe for concurrent
// callers. A singleflight.Group serializes in-flight ensures per key
// so that concurrent first-calls for the same repo only perform the
// install once; other callers wait and share the result.
type ensurer interface {
	// EnsureRepo installs fullsend into org/repoName, the slot repo the
	// allocator created for this lease (the forge's auto_init provides
	// the initial commit). It waits for the repo to become visible,
	// removes a fork an earlier holder of the slot left behind, and
	// runs the per-repo install flow (inference WIF resolution + github
	// setup). A slot repo that never becomes visible is an error; it is
	// never created here.
	//
	// The optional stillOwns (at most one is used; none means the caller
	// needs no verification) reports whether the repository under the
	// slot's name is still this lease's. EnsureRepo fails closed with
	// errLeaseLost, before any further write, when it reports false or
	// cannot verify: before deleting a leftover fork, before the install
	// hook and before every github setup attempt, including the ones that
	// follow a read-after-create retry wait. Without that, a slot repo
	// replaced by another holder while provisioning was delayed would be
	// installed into (or have its fork deleted).
	EnsureRepo(ctx context.Context, org, repoName string, stillOwns ...OwnerCheck) error

	// ReleaseRepo invalidates the ensure cache for that key and deletes a
	// leftover org/repoName-fork, if present, as long as stillOwns reports
	// that the repository under the slot name is still this lease's.
	// stillOwns is consulted after the fork has been looked up, just before
	// the first DELETE, and again before every resend of it. When it
	// reports false or fails (the base repo was replaced, is gone, or could
	// not be verified) the fork may belong to another holder, so it is left
	// for the next EnsureRepo of the slot to clean. It does not delete the
	// slot repo itself: the allocator's Release does that after checking
	// the repository is still this lease's. Idempotent: a missing fork is
	// not an error.
	ReleaseRepo(ctx context.Context, org, repoName string, stillOwns OwnerCheck) error
}

// OwnerCheck reports whether the repository under a slot's name is still
// the one its lease claimed. It changes no state. A nil OwnerCheck means
// the caller holds the slot and needs no verification.
type OwnerCheck func(ctx context.Context) (bool, error)

// errForkReplaced is returned by the fork delete's retry guard when the
// fork under the slot's fork name is gone or is a different repository
// than the one being deleted, or when the slot's base repo is no longer
// this lease's.
var errForkReplaced = errors.New("fork repo gone or replaced")

// errLeaseLost is returned by EnsureRepo when the repository under the
// slot's name is no longer this lease's (replaced by another holder or
// gone) or its ownership could not be verified, so provisioning stops
// before writing to the slot or deleting its fork.
var errLeaseLost = errors.New("slot repo is no longer this lease's")

// requireOwnership fails closed with errLeaseLost unless stillOwns (when
// non-nil) reports that the slot repo is still this lease's. step names
// the write about to be skipped.
func (e *repoEnsurer) requireOwnership(ctx context.Context, stillOwns OwnerCheck, target, step string) error {
	return requireOwnership(ctx, stillOwns, target, step)
}

// requireOwnership is the repoEnsurer-independent form of
// (*repoEnsurer).requireOwnership, for callers (the composed driver) that
// write to the slot repo after EnsureRepo returns.
func requireOwnership(ctx context.Context, stillOwns OwnerCheck, target, step string) error {
	if stillOwns == nil {
		return nil
	}
	owns, err := stillOwns(ctx)
	if err != nil {
		return fmt.Errorf("verifying %s is still this lease's before %s: %w: %w", target, step, errLeaseLost, err)
	}
	if !owns {
		return fmt.Errorf("%s before %s: %w", target, step, errLeaseLost)
	}
	return nil
}

// ownershipGuardedCtx returns a context under which the forge client
// does not replay a request after an ambiguous failure and re-verifies
// stillOwns after every retry wait and before the mutating request of a
// multi-step commit, so a write by slot name never lands on a replacement
// holder's repo. stillOwns runs on ctx, not the returned context, so the
// check itself is not guarded. With a nil stillOwns it returns ctx.
func ownershipGuardedCtx(ctx context.Context, stillOwns OwnerCheck, target, step string) context.Context {
	if stillOwns == nil {
		return ctx
	}
	return forge.WithRetryGuard(forge.WithoutAmbiguousRetry(ctx), func(context.Context) error {
		return requireOwnership(ctx, stillOwns, target, step)
	})
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
	// Unlike ensured, ReleaseRepo does not clear it (see ensurer).
	// Lazily initialised under mu.
	wifProviders map[string]string
	// provisioning holds one channel per org/repo whose inference
	// provision and post-provision poll is in flight; it is closed when
	// that attempt ends. Lazily initialised under mu.
	provisioning map[string]chan struct{}
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

func (e *repoEnsurer) EnsureRepo(ctx context.Context, org, repoName string, owner ...OwnerCheck) error {
	key := org + "/" + repoName
	var stillOwns OwnerCheck
	if len(owner) > 0 {
		stillOwns = owner[0]
	}

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

		if err := e.doEnsure(ctx, org, repoName, stillOwns); err != nil {
			return nil, err
		}

		e.mu.Lock()
		e.ensured[key] = struct{}{}
		e.mu.Unlock()

		return nil, nil
	})

	return err
}

// ReleaseRepo runs the ensurer's part of releasing a lease: it
// invalidates the ensure cache and deletes a leftover fork, so the next
// lessee of the slot name cannot inherit it. The slot repo itself is
// deleted by the allocator's Release, which first checks that the
// repository under the name is still this lease's. The cache is
// invalidated before the fork delete so a failed delete still forces
// the next EnsureRepo to install from scratch. The WIF provider cache
// is kept: the provider is keyed by repo name, so the next repo created
// under the name reuses it. The fork is deleted only while stillOwns
// reports that the slot repo is still this lease's; otherwise it is left
// alone, because a replacement holder of the slot may own it.
func (e *repoEnsurer) ReleaseRepo(ctx context.Context, org, repoName string, stillOwns OwnerCheck) error {
	key := org + "/" + repoName
	e.mu.Lock()
	delete(e.ensured, key)
	e.mu.Unlock()

	return e.deleteLeftoverFork(ctx, org, repoName, stillOwns)
}

// doEnsure installs fullsend into the slot repo the allocator created
// for this lease. The repo is always fresh (a claim creates it, a
// release deletes it), so it starts from the single auto_init commit and
// fullsend is never pre-installed: the install always pushes the binary
// or ref from the current checkout.
//
// The slot repo is never created, deleted or reset here. An existing
// repository under a slot name is another holder's lease unless this
// lease's claim created it, and deleting or resetting it would break
// that holder's scenario.
func (e *repoEnsurer) doEnsure(ctx context.Context, org, repoName string, stillOwns OwnerCheck) error {
	target := org + "/" + repoName

	// Step 1: the claim created the repo; wait for it to be visible
	// through the API before operating on it.
	if err := e.awaitCreation(ctx, org, repoName, target); err != nil {
		return err
	}

	// Step 2: a fork named after the slot belongs to the slot's holder,
	// which is now this lease. One left by an earlier holder (a crashed
	// runner, or a failed cleanup) is an orphan of a deleted repo and
	// would make the fork steps fail, so remove it.
	if err := e.deleteLeftoverForkOwned(ctx, org, repoName, stillOwns, true); err != nil {
		return err
	}

	// Verify org-level actor access before install. Direct collaborator
	// grants are not used: they vanish when the slot repo is deleted at
	// release, and re-adding them creates pending invitations (#7777).
	if err := e.verifyActors(ctx, org); err != nil {
		return err
	}

	// Step 3: run the full install flow and settle for Actions
	// readiness.
	e.logf("[ensure] %s needs install (fresh slot repo)", target)

	var hookState any
	if err := e.requireOwnership(ctx, stillOwns, target, "the pre-install hook"); err != nil {
		return err
	}
	if e.hooks.BeforeInstall != nil {
		var err error
		hookState, err = e.hooks.BeforeInstall(ctx, e.client, org, repoName)
		if err != nil {
			return fmt.Errorf("pre-install hook for %s: %w", target, err)
		}
	}

	// Step 4: run github setup to install fullsend and push the
	// current binary/ref.
	if err := e.installFullsend(ctx, target, stillOwns); err != nil {
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
		// Install and validation can outlast the lease, and the hook
		// writes by repo name, so recheck ownership first and guard its
		// forge writes against a later replacement.
		if err := e.requireOwnership(ctx, stillOwns, target, "the post-install hook"); err != nil {
			return err
		}
		hookCtx := ownershipGuardedCtx(ctx, stillOwns, target, "the post-install hook")
		if err := e.hooks.AfterInstall(hookCtx, e.client, org, repoName, hookState); err != nil {
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

// deleteLeftoverFork deletes org/repoName-fork, the same-org fork the
// fork steps create for the slot (see steps.resolveForkName), when it
// exists. Only the holder of the slot creates that name, and the caller
// holds the slot, so a fork found here was left by an earlier holder:
// it is a fork of a since-deleted slot repo, and the fork creation step
// would fail on it because it is not a fork of the new repo. The slot
// repo itself is never touched. No-op when the fork does not exist. A
// repo under the name that is not a fork is deleted only when it carries
// a slot-claim description (a fork promoted to a standalone repo when its
// parent was deleted); any other non-fork is refused.
//
// stillOwns, when non-nil, verifies that the repo under the slot name is
// still the caller's lease. It is checked after the fork's identity has
// been captured and before the first DELETE and every resend, so a
// replacement holder's fork is never deleted, whether the base was
// replaced before the lookup or while the lookup or a retry was waiting.
// A fork left because ownership is lost or unverifiable is cleaned by the
// slot's next EnsureRepo.
func (e *repoEnsurer) deleteLeftoverFork(ctx context.Context, org, repoName string, stillOwns OwnerCheck) error {
	return e.deleteLeftoverForkOwned(ctx, org, repoName, stillOwns, false)
}

// deleteLeftoverForkOwned is deleteLeftoverFork with a choice of what
// lost or unverifiable ownership means. With failClosed false (release)
// the fork is left alone and nil is returned: the caller is tearing down
// and the slot's next EnsureRepo cleans it. With failClosed true
// (provisioning) errLeaseLost is returned, so the caller stops before
// installing into a slot it no longer holds.
func (e *repoEnsurer) deleteLeftoverForkOwned(ctx context.Context, org, repoName string, stillOwns OwnerCheck, failClosed bool) error {
	forkName := repoName + "-fork"
	forkTarget := org + "/" + forkName
	fork, forkErr := e.client.GetRepo(ctx, org, forkName)
	if forge.IsNotFound(forkErr) {
		return nil
	}
	if forkErr != nil {
		return fmt.Errorf("checking for leftover fork repo %s: %w", forkTarget, forkErr)
	}
	if fork == nil {
		return fmt.Errorf("repo %s exists but could not be read; refusing to delete it", forkTarget)
	}
	if !fork.Fork && !isSlotOwnershipMarker(fork.Description) {
		// The fork steps only ever create a real fork under this name.
		// A fork stops reporting as one when its parent is deleted and
		// it is promoted to the network root (a crashed runner's fork
		// whose slot repo the collector then reclaimed), but it keeps
		// the slot-claim description it inherited from the parent, which
		// is what marks it as test-owned and safe to reclaim. Anything
		// else under this name is not ours.
		return fmt.Errorf("repo %s exists but is not a fork; refusing to delete it", forkTarget)
	}
	// The fork may be the replacement holder's if the base was replaced
	// before or during the lookup above, so verify ownership now that the
	// fork's identity is captured.
	if stillOwns != nil {
		owns, err := stillOwns(ctx)
		if err != nil {
			e.logf("[ensure] verifying %s/%s is still this lease's (leaving fork %s alone): %v", org, repoName, forkTarget, err)
			if failClosed {
				return fmt.Errorf("verifying %s/%s is still this lease's before deleting fork %s: %w: %w", org, repoName, forkTarget, errLeaseLost, err)
			}
			return nil
		}
		if !owns {
			e.logf("[ensure] %s/%s is no longer this lease's; leaving fork %s for the slot's next holder", org, repoName, forkTarget)
			if failClosed {
				return fmt.Errorf("%s/%s before deleting fork %s: %w", org, repoName, forkTarget, errLeaseLost)
			}
			return nil
		}
	}
	e.logf("[ensure] deleting leftover fork %s", forkTarget)
	// The name may be recreated by another holder while a delete is
	// retried (an ambiguous failure whose first attempt landed, or a
	// rate-limit wait), so never replay the DELETE after an ambiguous
	// failure and re-verify the fork's identity and the base's ownership
	// before every resend.
	delCtx := forge.WithRetryGuard(forge.WithoutAmbiguousRetry(ctx), func(context.Context) error {
		// Look up on the caller's ctx so the lookup is not itself guarded.
		cur, err := e.client.GetRepo(ctx, org, forkName)
		if err != nil {
			if forge.IsNotFound(err) {
				return errForkReplaced
			}
			return fmt.Errorf("re-verifying fork %s after retry wait: %w", forkTarget, err)
		}
		if fork.ID != 0 && cur.ID != fork.ID {
			return errForkReplaced
		}
		if stillOwns != nil {
			owns, err := stillOwns(ctx)
			if err != nil {
				if failClosed {
					return fmt.Errorf("re-verifying %s/%s after retry wait: %w: %w", org, repoName, errLeaseLost, err)
				}
				return fmt.Errorf("re-verifying %s/%s after retry wait: %w", org, repoName, err)
			}
			if !owns {
				if failClosed {
					return fmt.Errorf("%s/%s after retry wait: %w", org, repoName, errLeaseLost)
				}
				return errForkReplaced
			}
		}
		return nil
	})
	if err := e.client.DeleteRepo(delCtx, org, forkName); err != nil {
		if failClosed && errors.Is(err, errLeaseLost) {
			return err // ownership lost during a retry wait: nothing was resent
		}
		if forge.IsNotFound(err) || errors.Is(err, errForkReplaced) {
			return nil // race: deleted, or replaced by another holder's fork
		}
		// The delete may have landed with the response lost; do not
		// resend it, re-check the fork's identity instead.
		cur, getErr := e.client.GetRepo(ctx, org, forkName)
		if forge.IsNotFound(getErr) || (getErr == nil && fork.ID != 0 && cur.ID != fork.ID) {
			return nil
		}
		return fmt.Errorf("deleting leftover fork repo %s: %w", forkTarget, err)
	}
	return e.awaitDeletion(ctx, org, forkName, forkTarget)
}

// awaitDeletion polls GetRepo with exponential backoff until the repo
// returns 404, confirming the deletion has propagated through the
// GitHub API's eventual-consistency layer. If the repo is still
// visible after all attempts the function returns nil anyway: the fork
// steps retry CreateFork on a name GitHub still reports as taken
// (steps.createFork), so a stale GetRepo success is not fatal here.
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
	e.logf("[ensure] %s still visible after %d attempts; proceeding", target, resetMaxAttempts)
	return nil
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
//
// stillOwns, when non-nil, is checked before every setup attempt, so the
// attempt that follows a retry wait never installs into a slot repo that
// another holder replaced during the wait.
func (e *repoEnsurer) installFullsend(ctx context.Context, target string, stillOwns OwnerCheck) error {
	opts := e.setupOpts
	opts.ResolveWIFProvider = func(target, project string) (string, error) {
		wifProvider, err := e.resolveWIFProvider(ctx, target, project)
		if err != nil {
			return "", err
		}
		// Resolution can block on provisionGate and inference
		// provisioning, so recheck ownership here: the setup subprocess
		// starts immediately after this resolver returns.
		if err := e.requireOwnership(ctx, stillOwns, target, "github setup (after WIF resolution)"); err != nil {
			return "", err
		}
		return wifProvider, nil
	}

	delay := resetRetryDelay
	var lastErr error
	for attempt := 1; attempt <= resetMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled while running github setup for %s: %w", target, err)
		}

		if err := e.requireOwnership(ctx, stillOwns, target, "github setup"); err != nil {
			return err
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
// "inference provision", holding provisionGate only for the provision so
// at most one provision runs in the process at a time, then re-polls
// status after releasing the gate.
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
// holding provisionGate, then releases the gate and polls "inference
// status" until the provider is healthy. A concurrent caller for the same
// name waits for the in-flight provision and poll and reuses its cached
// result (or runs its own if that one failed), and the cache is re-checked
// once the gate is held, so a caller that waited behind a finished
// provision for the same name reuses it.
func (e *repoEnsurer) provisionWIFProvider(ctx context.Context, target, project string) (string, error) {
	for {
		if err := ctx.Err(); err != nil {
			return "", fmt.Errorf("waiting to provision inference for %s: %w", target, err)
		}
		e.mu.Lock()
		if cached, ok := e.wifProviders[target]; ok {
			e.mu.Unlock()
			return cached, nil
		}
		running, busy := e.provisioning[target]
		if !busy {
			if e.provisioning == nil {
				e.provisioning = make(map[string]chan struct{})
			}
			done := make(chan struct{})
			e.provisioning[target] = done
			e.mu.Unlock()
			defer func() {
				e.mu.Lock()
				delete(e.provisioning, target)
				e.mu.Unlock()
				close(done)
			}()
			return e.provisionAndWait(ctx, target, project)
		}
		e.mu.Unlock()
		select {
		case <-running:
		case <-ctx.Done():
			return "", fmt.Errorf("waiting to provision inference for %s: %w", target, ctx.Err())
		}
	}
}

// provisionAndWait is provisionWIFProvider's body for one in-flight
// provision of target.
func (e *repoEnsurer) provisionAndWait(ctx context.Context, target, project string) (string, error) {
	cached, ok, err := e.provisionUnderGate(ctx, target, project)
	if err != nil {
		return "", err
	}
	if ok {
		return cached, nil
	}
	wifProvider, err := common.WaitInferenceHealthy(ctx, e.binary, e.token, target, project, e.runCLI, e.logf)
	if err != nil {
		return "", err
	}
	e.cacheWIFProvider(target, wifProvider)
	return wifProvider, nil
}

// provisionUnderGate holds provisionGate while it re-checks the cache and,
// on a miss, runs "inference provision". It returns the cached provider
// with ok=true on a hit. The gate is released on every path, including a
// panic, before the caller polls status: the gate guards IAM writes, and a
// slow-to-propagate provider must not hold up other slots' provisions for
// the whole backoff.
func (e *repoEnsurer) provisionUnderGate(ctx context.Context, target, project string) (string, bool, error) {
	select {
	case provisionGate <- struct{}{}:
	case <-ctx.Done():
		return "", false, fmt.Errorf("waiting to provision inference for %s: %w", target, ctx.Err())
	}
	defer func() { <-provisionGate }()

	e.mu.Lock()
	cached, ok := e.wifProviders[target]
	e.mu.Unlock()
	if ok {
		return cached, true, nil
	}
	if err := common.RunInferenceProvision(e.binary, e.token, target, project, e.runCLI, e.logf); err != nil {
		return "", false, err
	}
	return "", false, nil
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
