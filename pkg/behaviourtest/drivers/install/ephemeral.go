// ephemeral.go implements the ephemeral-repo behaviour-test driver
// (ADR 0114) coordinated by the lock pool (ADR 0115).
//
// Unlike the slot-pool driver (composed.go), which leases reused
// test-repo-NN slots across many orgs, this driver:
//
//   - acquires one lock-pool slot per run, which fixes the run's org and
//     bounds cross-run concurrency (ADR 0115);
//   - creates a uniquely named ephemeral repo bt-{run-id}-{uuid8} per
//     scenario in that org, installs fullsend via `repos install
//     --fullsend-ref` with a shared org-scoped WIF provider, and never
//     reuses a repo (ADR 0114);
//   - retains repos after each scenario and batch-prunes the oldest whole
//     runs at Finalize, skipping any run that still holds a lock.
//
// It implements the same LeaseDriver contract the suite already uses, so a
// lease is simply a freshly created ephemeral repo rather than a claimed
// slot; the surrounding World/suite/steps are unchanged.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// repoKeepThreshold is the GitHub bt-* repo count above which the oldest
// whole runs are pruned (ADR 0114). Retaining repos raises the org's
// rate-limit cap, so pruning stops as soon as the count would drop below it.
// A var so tests can lower it.
var repoKeepThreshold = 200

// KeepRepos reports whether retained repos should be preserved (no pruning),
// for debugging. Set E2E_KEEP_REPOS=true.
func KeepRepos() bool { return os.Getenv("E2E_KEEP_REPOS") == "true" }

// ephemeralEnsurer creates ephemeral repos with fullsend installed. Each
// call generates a unique name, creates the repo, waits for git readiness,
// installs fullsend (no vendoring; shared org-scoped WIF), validates, and
// waits for Actions to index the workflow.
type ephemeralEnsurer struct {
	client        forge.Client
	token         string
	binary        string
	fullsendRef   string
	appSet        string
	configPreset  string
	mintURL       string
	gcpProjectID  string
	wifProvider   string
	runID         RunID
	logf          func(string, ...any)
	runCLI        CLIRunnerFunc
	settle        SettleFunc
	eventDelivery EventDeliveryFunc
}

const (
	// ephemeralGitReadyMaxAttempts bounds the GetRef polls that confirm the
	// git layer is initialized after repo creation.
	ephemeralGitReadyMaxAttempts = 15
	// ephemeralEventMaxAttempts bounds the event-delivery and settle polls.
	ephemeralEventMaxAttempts = 30
)

// ephemeral polling delays. Vars so tests can shorten them.
var (
	ephemeralGitReadyPoll = 2 * time.Second
	ephemeralEventPoll    = 5 * time.Second
)

// EventDeliveryFunc verifies GitHub's event-delivery pipeline is live for a
// freshly created repo, using a pre-created warmup issue.
type EventDeliveryFunc func(ctx context.Context, client forge.Client, org, repo string, issueNumber int, workflowFile string, logf func(string, ...any)) error

const (
	// ephemeralAllowInfix marks a repo the run's mint trusts as a workflow
	// host: bt-{run-id}-allow-{uuid8}. The mint's WORKFLOW_HOST_REPOS is
	// scoped to bt-{run-id}-allow-*, so every repo the run creates for its
	// own work is trusted.
	ephemeralAllowInfix = "allow"
	// ephemeralDenyInfix marks a repo deliberately kept off the allowlist:
	// bt-{run-id}-deny-{uuid8}. It carries the run-id (so prune and cleanup
	// still find it) but does not match bt-{run-id}-allow-*, so the mint
	// rejects it — used by the mint-probe unapproved-host scenarios.
	ephemeralDenyInfix = "deny"
)

// ephemeralRepoName derives a unique repo name from the run id: the shared
// bt-{run-id}- prefix, the "allow" trust marker, and a per-repo 8-hex suffix.
func ephemeralRepoName(runID RunID) string {
	return fmt.Sprintf("bt-%s-%s-%s", runID, ephemeralAllowInfix, uuid8())
}

// uuid8 returns 8 hex chars of a fresh random UUID, the per-repo suffix.
func uuid8() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")[:8]
}

// ephemeralWorkflowHostPrefix is the run-scoped WORKFLOW_HOST_REPOS value
// for the ephemeral mint: a prefix glob matching exactly this run's trusted
// repos (org/bt-{run-id}-allow-*). Every repo a run creates for its own work
// carries the "allow" marker, so all are trusted workflow hosts, while repos
// from other runs, outside the prefix, or marked "deny" are not — tighter
// than a bare "*".
func ephemeralWorkflowHostPrefix(org string, runID RunID) string {
	return fmt.Sprintf("%s/bt-%s-%s-*", org, runID, ephemeralAllowInfix)
}

// UnapprovedProbeHost returns a workflow-host repo name the active mint will
// reject, derived from the leased repo. For an ephemeral leased repo
// (bt-{run-id}-allow-{uuid8}) it swaps the "allow" marker for "deny", so the
// host keeps the run-id — prune and scenario cleanup still find it — while
// falling outside the mint's bt-{run-id}-allow-* allowlist. For a slot-pool
// repo (test-repo-NN, no marker) it appends "-mint-unapproved", which is on
// no list. Used by the mint-probe unapproved-host scenarios.
func UnapprovedProbeHost(leasedRepo string) string {
	marker := "-" + ephemeralAllowInfix + "-"
	if strings.HasPrefix(leasedRepo, "bt-") {
		if i := strings.Index(leasedRepo, marker); i >= 0 {
			return leasedRepo[:i] + "-" + ephemeralDenyInfix + "-" + leasedRepo[i+len(marker):]
		}
	}
	return leasedRepo + "-mint-unapproved"
}

// createRepo creates and provisions one ephemeral repo, returning its name.
// The name is returned even on later-stage failures so the caller can track
// it for cleanup/prune.
func (e *ephemeralEnsurer) createRepo(ctx context.Context, org, hint string) (string, error) {
	repoName := ephemeralRepoName(e.runID)
	target := org + "/" + repoName

	description := "fullsend behaviour test"
	if hint != "" {
		description = "Behaviour test: " + hint
	}
	e.logf("[ensure] creating %s (auto_init provides initial commit)", target)
	if _, err := e.client.CreateRepo(ctx, org, repoName, description, false); err != nil {
		return "", fmt.Errorf("creating repo %s: %w", target, err)
	}

	if err := e.awaitGitReady(ctx, org, repoName, target); err != nil {
		return repoName, err
	}

	// Warmup issue created before install: no workflow exists yet, so the
	// event is ignored. Used after install to verify event delivery.
	var warmupIssue int
	if e.eventDelivery != nil {
		issue, err := e.client.CreateIssue(ctx, org, repoName, "warmup", "event delivery probe")
		if err != nil {
			return repoName, fmt.Errorf("creating warmup issue on %s: %w", target, err)
		}
		warmupIssue = issue.Number
	}

	if err := e.installFullsend(org, repoName); err != nil {
		return repoName, err
	}

	if err := ValidatePerRepoPostInstall(ctx, e.client, org, repoName); err != nil {
		return repoName, fmt.Errorf("post-install validation for %s: %w", target, err)
	}

	if e.settle != nil {
		if err := e.settle(ctx, e.client, org, repoName, PerRepoTriageWorkflow, e.logf); err != nil {
			return repoName, fmt.Errorf("waiting for Actions readiness on %s: %w", target, err)
		}
	}

	if e.eventDelivery != nil {
		if err := e.eventDelivery(ctx, e.client, org, repoName, warmupIssue, PerRepoTriageWorkflow, e.logf); err != nil {
			return repoName, fmt.Errorf("verifying event delivery on %s: %w", target, err)
		}
	}

	return repoName, nil
}

func (e *ephemeralEnsurer) installFullsend(org, repoName string) error {
	return runReposInstallFn(common.ReposInstallOpts{
		Binary:       e.binary,
		Token:        e.token,
		Target:       org + "/" + repoName,
		MintURL:      e.mintURL,
		FullsendRef:  e.fullsendRef,
		AppSet:       e.appSet,
		ConfigPreset: e.configPreset,
		GCPProjectID: e.gcpProjectID,
		WIFProvider:  e.wifProvider,
	}, e.runCLI, e.logf)
}

// awaitGitReady polls GetRef("heads/main") until the git layer is ready,
// avoiding 422/409 errors when the API reports the repo before its git
// layer has initialized.
func (e *ephemeralEnsurer) awaitGitReady(ctx context.Context, org, repoName, target string) error {
	e.logf("[ensure] waiting for git readiness on %s", target)
	for attempt := 1; attempt <= ephemeralGitReadyMaxAttempts; attempt++ {
		if _, err := e.client.GetRef(ctx, org, repoName, "heads/main"); err == nil {
			e.logf("[ensure] %s git ready after %d attempt(s)", target, attempt)
			return nil
		}
		if attempt < ephemeralGitReadyMaxAttempts {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled waiting for git readiness on %s: %w", target, ctx.Err())
			case <-time.After(ephemeralGitReadyPoll):
			}
		}
	}
	return fmt.Errorf("git layer on %s not ready after %d attempts", target, ephemeralGitReadyMaxAttempts)
}

// awaitEventDelivery posts a comment on the warmup issue and polls for a
// completed workflow run, proving event delivery is live. The comment body
// does not start with /fs-, so the shim's dispatch job is skipped.
func awaitEventDelivery(ctx context.Context, client forge.Client, org, repo string, issueNumber int, workflowFile string, logf func(string, ...any)) error {
	target := org + "/" + repo
	logf("[ensure] verifying event delivery on %s", target)
	probeTime := time.Now()
	if _, err := client.CreateIssueComment(ctx, org, repo, issueNumber, "fullsend event delivery probe"); err != nil {
		return fmt.Errorf("posting warmup comment on %s: %w", target, err)
	}
	for attempt := 1; attempt <= ephemeralEventMaxAttempts; attempt++ {
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
		if attempt < ephemeralEventMaxAttempts {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while verifying event delivery on %s: %w", target, ctx.Err())
			case <-time.After(ephemeralEventPoll):
			}
		}
	}
	return fmt.Errorf("event delivery not confirmed on %s after %d attempts", target, ephemeralEventMaxAttempts)
}

// runReposInstallFn installs fullsend into an ephemeral repo. Override in
// tests to stub the `repos install` CLI without shelling out.
var runReposInstallFn = common.RunReposInstall

// ephemeralRepoPrefixLen is the fixed length of the "bt-{run-id}-" prefix
// (3 for "bt-" + RunIDLen + 1 for the trailing "-").
const ephemeralRepoPrefixLen = 3 + RunIDLen + 1

// ephemeralDriver is the LeaseDriver for the ephemeral-repo model. It holds
// one lock-pool slot (fixing the run's org) for the suite's lifetime and
// creates a fresh ephemeral repo per lease.
type ephemeralDriver struct {
	org               string
	mint              mintDriver
	ens               *ephemeralEnsurer
	client            forge.Client
	token             string
	mintURL           string
	statusGroup       string
	workflowHostRepos []string

	lockPool *LockPool
	held     *HeldLock
	runID    RunID
	logf     func(string, ...any)

	mu      sync.Mutex
	created map[string]bool
}

var _ LeaseDriver = (*ephemeralDriver)(nil)
var _ LeaseVerifier = (*ephemeralDriver)(nil)

// AllocateLease creates a fresh ephemeral repo in the run's locked org and
// returns it as a lease.
func (d *ephemeralDriver) AllocateLease(ctx context.Context) (RepoLease, error) {
	name, err := d.ens.createRepo(ctx, d.org, "")
	if name != "" {
		d.mu.Lock()
		d.created[name] = true
		d.mu.Unlock()
	}
	if err != nil {
		return RepoLease{}, fmt.Errorf("allocating ephemeral repo in %s: %w", d.org, err)
	}
	d.logf("[driver] created %s/%s", d.org, name)
	return RepoLease{
		Org:                   d.org,
		Repo:                  name,
		Token:                 d.token,
		Client:                d.client,
		MintURL:               d.mintURL,
		MintStatusGroup:       d.statusGroup,
		MintWorkflowHostRepos: d.workflowHostRepos,
	}, nil
}

// DeallocateLease retains the repo (GitHub policy, ADR 0114): it retires the
// repo so it cannot invoke the shared org-scoped WIF after its scenario ends,
// then stops tracking it. Retained repos are batch-pruned at Finalize. A
// retirement failure is returned rather than swallowed, so the suite does not
// treat a still-authorized repo as cleanly deallocated.
func (d *ephemeralDriver) DeallocateLease(ctx context.Context, _, repo string) error {
	err := d.sanitizeRetainedRepo(ctx, repo)
	d.mu.Lock()
	delete(d.created, repo)
	d.mu.Unlock()
	if err != nil {
		return fmt.Errorf("retiring retained repo %s/%s: %w", d.org, repo, err)
	}
	return nil
}

// sanitizeRetainedRepo retires a repo before it is retained. The mint URL and
// WIF provider are configuration, not secrets, so deleting the repo's
// variables alone is not an authorization boundary: a workflow could carry
// those values directly and the repo still matches the run's trusted host
// prefix until the mint is torn down. So the primary control is disabling
// Actions, which stops any further workflow run from starting there; stripping
// the mint/WIF variables is secondary cleanup. Errors are collected and
// returned (not just logged) so DeallocateLease can surface a repo that could
// not be retired. In-flight runs are not cancelled here; the mint teardown at
// Finalize closes that residual window.
func (d *ephemeralDriver) sanitizeRetainedRepo(ctx context.Context, repo string) error {
	var errs []error

	// Primary boundary: no new workflow runs can start on the retired repo.
	if disabler, ok := d.client.(forge.ActionsDisabler); ok {
		if err := disabler.DisableActions(ctx, d.org, repo); err != nil && !forge.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("disabling Actions: %w", err))
		}
	} else {
		d.logf("[driver] sanitize: client cannot disable Actions on %s/%s (relying on mint teardown)", d.org, repo)
	}

	// Secondary cleanup: strip leftover mint/WIF variables.
	vars, err := d.client.ListRepoVariables(ctx, d.org, repo)
	if err != nil {
		errs = append(errs, fmt.Errorf("listing variables: %w", err))
	} else {
		for name := range vars {
			if err := d.client.DeleteRepoVariable(ctx, d.org, repo, name); err != nil && !forge.IsNotFound(err) {
				errs = append(errs, fmt.Errorf("deleting variable %s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// StillOwnsLease is always true for ephemeral repos: names are unique to
// this run and prune never deletes a locked run's repos, so a lease's repo
// is never taken over by another holder.
func (d *ephemeralDriver) StillOwnsLease(context.Context, string, string) (bool, error) {
	return true, nil
}

// AllocateRepo is unsupported: callers use AllocateLease.
func (d *ephemeralDriver) AllocateRepo(context.Context) (string, error) {
	return "", errLeaseRequired
}

// DeallocateRepo is unsupported: callers use DeallocateLease.
func (d *ephemeralDriver) DeallocateRepo(context.Context, string) error {
	return errLeaseRequired
}

// Capacity is not slot-bound in the ephemeral model; concurrency is limited
// by GODOG_CONCURRENCY and the org's rate-limit budget, not a fixed pool. A
// high value avoids a spurious over-capacity warning.
func (d *ephemeralDriver) Capacity() int { return 1000 }

// Finalize prunes old runs (unless E2E_KEEP_REPOS), releases the lock slot,
// and tears down the mint.
func (d *ephemeralDriver) Finalize(ctx context.Context) error {
	logAPIConsumption(d.client, d.org, "at finalize", d.logf)
	if !KeepRepos() {
		d.pruneOldRuns(ctx)
	}

	var relErr error
	if d.held != nil {
		if err := d.held.Release(ctx); err != nil {
			relErr = fmt.Errorf("Finalize: releasing lock: %w", err)
		}
	}

	var mintErr error
	if d.mint != nil {
		if err := d.mint.Teardown(ctx); err != nil {
			mintErr = fmt.Errorf("Finalize: mint teardown: %w", err)
		}
	}

	return errors.Join(relErr, mintErr)
}

// logStartRateLimit records a run-start rate-limit baseline so the run's total
// GitHub API consumption can be recovered from the start and finalize lines as
// (remaining_start - remaining_end) + (limit_end - limit_start). The limit term
// is needed because an installation token's cap climbs as the org gains repos,
// so a bare remaining delta would undercount. It reads /rate_limit (which does
// not consume quota and so is a clean pre-work baseline, before any repo is
// created) and seeds the client's observation. Best-effort: a failure is logged
// and leaves the finalize line's per-window counters as the fallback measure.
func logStartRateLimit(ctx context.Context, client forge.Client, org string, logf func(string, ...any)) {
	f, ok := client.(forge.RateLimitFetcher)
	if !ok {
		return
	}
	rl, err := f.FetchRateLimit(ctx)
	if err != nil {
		logf("[driver] rate-limit at start (org %s): baseline read failed: %v", org, err)
		return
	}
	logf("[driver] rate-limit at start (org %s): %s", org, rl.String())
}

// logAPIConsumption reports the run's GitHub API usage against the org's
// hourly rate-limit budget, so a growing suite's per-run cost (and how close
// it is to the 12,500/hr cap) is visible. The quota counter's remaining/limit
// reflects the installation token's server-side consumption — including the
// `repos install` CLI subprocess, which shares the token but not this
// client — while APICalls() is the suite-side per-caller breakdown only.
// Paired with the logStartRateLimit baseline, the run total is
// (remaining_start - remaining_end) + (limit_end - limit_start).
func logAPIConsumption(client forge.Client, org, when string, logf func(string, ...any)) {
	if r, ok := client.(forge.APIQuotaReporter); ok {
		for _, c := range r.APIQuotaCounters() {
			logf("[driver] rate-limit quota %s (org %s): %s", when, org, formatAPIQuotaCounter(c))
		}
	}
	if r, ok := client.(forge.RateLimitReporter); ok {
		if rl, seen := r.RateLimit(); seen {
			logf("[driver] rate-limit %s (org %s): %s", when, org, rl.String())
		}
	}
	if r, ok := client.(forge.APICallReporter); ok {
		if calls := r.APICalls(); len(calls) > 0 {
			logf("[driver] suite-side API calls %s (org %s): %s", when, org, formatAPICalls(calls))
		}
	}
}

// pruneOldRuns retains the newest bt-* runs and deletes the oldest whole
// runs once the org's bt-* repo count exceeds repoKeepThreshold, skipping
// any run that still holds a lock (ADR 0114 × 0115).
func (d *ephemeralDriver) pruneOldRuns(ctx context.Context) {
	lister, ok := d.client.(forge.AllRepoLister)
	if !ok {
		d.logf("[driver] prune: client does not support AllRepoLister, skipping")
		return
	}
	repos, err := lister.ListAllOrgRepos(ctx, d.org)
	if err != nil {
		// Listing is the single source for both the repos to prune and the
		// locks that protect them; if it fails we prune nothing, so a
		// degraded forge can never make an active run's repos look
		// unprotected (ADR 0114 × 0115).
		d.logf("[driver] prune: failed to list repos: %v", err)
		return
	}

	// Derive the protected run-ids from the same listing: every lock repo in
	// the org (not just this run's configured slots) whose description names a
	// current owner. No separate per-slot reads, so there is no path where a
	// transient lock read fails open and a running suite's repos get deleted.
	locked := lockedRunIDs(repos)

	groups := make(map[string][]string)
	for _, r := range repos {
		if !strings.HasPrefix(r.Name, "bt-") || len(r.Name) < ephemeralRepoPrefixLen {
			continue
		}
		runID := r.Name[3 : 3+RunIDLen]
		groups[runID] = append(groups[runID], r.Name)
	}

	total := 0
	for _, names := range groups {
		total += len(names)
	}
	if total <= repoKeepThreshold {
		d.logf("[driver] prune: %d bt-* repos, at or under threshold (%d), skipping", total, repoKeepThreshold)
		return
	}

	runIDs := make([]string, 0, len(groups))
	for id := range groups {
		runIDs = append(runIDs, id)
	}
	sort.Strings(runIDs) // time-sortable: oldest first

	for _, id := range runIDs {
		if total-len(groups[id]) < repoKeepThreshold {
			break
		}
		if locked[id] {
			d.logf("[driver] prune: skipping run %s (still holds a lock)", id)
			continue
		}
		names := groups[id]
		d.logf("[driver] prune: deleting run %s (%d repos)", id, len(names))
		for _, name := range names {
			if err := d.client.DeleteRepo(ctx, d.org, name); err != nil && !forge.IsNotFound(err) {
				d.logf("[driver] prune: failed to delete %s/%s: %v", d.org, name, err)
			}
		}
		total -= len(names)
	}
	d.logf("[driver] prune: %d bt-* repos remaining", total)
}

// lockedRunIDs returns the set of run ids that currently own a lock slot,
// read from an org repo listing: every lock repo (lock-<N>, any index) whose
// description carries a valid owner token. Deriving this from the same listing
// prune already fetched means prune never deletes a running suite's repos out
// from under it — there are no separate per-slot reads that could fail open,
// and a run holding a slot outside the current config's range is still seen.
func lockedRunIDs(repos []forge.Repository) map[string]bool {
	locked := make(map[string]bool)
	for _, r := range repos {
		if !isLockRepoName(r.Name) {
			continue
		}
		if token, ok := parseOwnerToken(r.Description); ok {
			locked[token.String()] = true
		}
	}
	return locked
}
