package steps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// cleanupMaxAttempts is the maximum number of attempts for a cleanup
// operation before giving up. Overridable in tests to avoid real waits.
var cleanupMaxAttempts = 3

// cleanupBaseDelay is the base delay between cleanup retry attempts.
// Actual delay doubles with each attempt (exponential backoff).
// Overridable in tests to avoid real waits.
var cleanupBaseDelay = 500 * time.Millisecond

// cleanupRetry runs fn up to cleanupMaxAttempts times, retrying only on
// transient errors (as determined by forge.IsTransient). Non-transient
// errors and nil are returned immediately. When all retries are
// exhausted, the last error is returned so the caller can log it.
func cleanupRetry(logf func(string, ...any), desc string, fn func() error) error {
	var lastErr error
	for attempt := range cleanupMaxAttempts {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !forge.IsTransient(lastErr) {
			return lastErr
		}
		if attempt < cleanupMaxAttempts-1 {
			delay := cleanupBaseDelay * time.Duration(1<<uint(attempt))
			if logf != nil {
				logf("behaviour cleanup: %s: transient error (attempt %d/%d, retrying in %v): %v",
					desc, attempt+1, cleanupMaxAttempts, delay, lastErr)
			}
			time.Sleep(delay)
		}
	}
	return lastErr
}

func CleanupScenario(w *world.World) {
	ctx := context.Background()

	// --- Issue / PR cleanup ---
	if w.IssueNumber > 0 {
		desc := fmt.Sprintf("close issue #%d", w.IssueNumber)
		if err := cleanupRetry(w.Logf, desc, func() error {
			return w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, w.IssueNumber)
		}); err != nil {
			worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
		}
	}
	if w.ForkPRNumber > 0 {
		// Fork PRs are opened against the base repo, so close on base repo.
		desc := fmt.Sprintf("close fork PR #%d", w.ForkPRNumber)
		if err := cleanupRetry(w.Logf, desc, func() error {
			return w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, w.ForkPRNumber)
		}); err != nil {
			worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
		}
	}

	// --- Branch-scenario cleanup ---
	// Sweep applier-created PRs by namespace: a code run for this
	// scenario's issue pushes to agent/<issue>-*, but the PR is only
	// registered in CreatedPRNumbers when the head-match assertion ran
	// and succeeded. Issue numbers are unique, so anything left open in
	// the namespace would otherwise be permanent pool-repo debris. Gated
	// on IssueNumber alone (not on CreatedBranches) so it still runs for
	// a code-stage scenario that never seeded a decoy/seed branch.
	if w.IssueNumber > 0 {
		namespacePrefix := fmt.Sprintf("agent/%d-", w.IssueNumber)
		seenPR := make(map[int]bool, len(w.CreatedPRNumbers))
		for _, n := range w.CreatedPRNumbers {
			seenPR[n] = true
		}
		var prs []forge.ChangeProposal
		desc := "list open PRs for namespace sweep"
		if err := cleanupRetry(w.Logf, desc, func() error {
			var listErr error
			prs, listErr = w.SCM.ListOpenChangeProposals(ctx, w.RepoOwner, w.RepoName)
			return listErr
		}); err != nil {
			worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
		} else {
			for _, pr := range prs {
				if !strings.HasPrefix(pr.Head, namespacePrefix) || seenPR[pr.Number] {
					continue
				}
				seenPR[pr.Number] = true
				w.CreatedPRNumbers = append(w.CreatedPRNumbers, pr.Number)
				w.CreatedBranches = append(w.CreatedBranches, pr.Head)
			}
		}
	}

	// Close PRs before deleting their head branches so GitHub does not
	// auto-close them with a confusing "branch deleted" event first.
	closedPR := make(map[int]bool, len(w.CreatedPRNumbers))
	for _, number := range w.CreatedPRNumbers {
		if closedPR[number] {
			continue
		}
		closedPR[number] = true
		desc := fmt.Sprintf("close PR #%d", number)
		if err := cleanupRetry(w.Logf, desc, func() error {
			return w.SCM.CloseIssue(ctx, w.RepoOwner, w.RepoName, number)
		}); err != nil {
			worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
		}
	}
	deletedBranch := make(map[string]bool, len(w.CreatedBranches))
	for _, branch := range w.CreatedBranches {
		if deletedBranch[branch] {
			continue
		}
		deletedBranch[branch] = true
		desc := fmt.Sprintf("delete branch %s", branch)
		if err := cleanupRetry(w.Logf, desc, func() error {
			return w.SCM.DeleteBranch(ctx, w.RepoOwner, w.RepoName, branch)
		}); err != nil {
			if !forge.IsNotFound(err) {
				worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
			}
		}
	}

	// --- Fork repo cleanup ---
	// Fork repos are ephemeral: created per-scenario and deleted here.
	// Branch and PR cleanup above already ran against the base repo;
	// deleting the fork repo removes the branch implicitly, but we
	// still attempt branch deletion first so partial failures leave
	// less debris.
	if w.ForkPRBranch != "" && w.ForkOwner != "" && w.ForkRepo != "" {
		desc := fmt.Sprintf("delete fork branch %s", w.ForkPRBranch)
		if err := cleanupRetry(w.Logf, desc, func() error {
			return w.SCM.DeleteBranch(ctx, w.ForkOwner, w.ForkRepo, w.ForkPRBranch)
		}); err != nil {
			if !forge.IsNotFound(err) {
				worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
			}
		}
	}
	if w.ForkOwner != "" && w.ForkRepo != "" && w.ForkRepo != w.RepoName {
		desc := fmt.Sprintf("delete fork repo %s/%s", w.ForkOwner, w.ForkRepo)
		if err := cleanupRetry(w.Logf, desc, func() error {
			return w.SCM.DeleteRepo(ctx, w.ForkOwner, w.ForkRepo)
		}); err != nil {
			if !forge.IsNotFound(err) {
				worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
			}
		}
	}

	// --- URL harness hosting repo cleanup ---
	// Hosting repos are ephemeral: created per-scenario and deleted here
	// (same lifecycle as fork repos). Guard against deleting the enrolled
	// test repo itself.
	if w.URLHarnessRepoOwner != "" && w.URLHarnessRepoName != "" && w.URLHarnessRepoName != w.RepoName {
		desc := fmt.Sprintf("delete harness-hosting repo %s/%s", w.URLHarnessRepoOwner, w.URLHarnessRepoName)
		if err := cleanupRetry(w.Logf, desc, func() error {
			return w.SCM.DeleteRepo(ctx, w.URLHarnessRepoOwner, w.URLHarnessRepoName)
		}); err != nil {
			if !forge.IsNotFound(err) {
				worldLogf(w, "behaviour cleanup: %s: %v", desc, err)
			}
		}
	}

	// --- Jira mock cleanup ---
	if w.JiraMockServer != nil {
		w.JiraMockServer.Close()
	}
	if w.JiraConfigDir != "" {
		if err := os.RemoveAll(w.JiraConfigDir); err != nil {
			worldLogf(w, "behaviour cleanup: remove jira config dir: %v", err)
		}
	}

	// --- Artifact cleanup ---
	if w.ArtifactDir != "" && shouldRemoveArtifactDir(w.ArtifactDir, os.Getenv("BEHAVIOUR_ARTIFACT_DIR")) {
		if err := os.RemoveAll(w.ArtifactDir); err != nil {
			worldLogf(w, "behaviour cleanup: remove artifact dir: %v", err)
		}
	}

	// --- Kill switch cleanup ---
	// Deactivate the kill switch so the next scenario on this slot is
	// not blocked by sticky state. Runs before dummy-script cleanup
	// because the kill switch is a repo-level config that affects all
	// harnesses.
	if w.KillSwitchActivated {
		if err := cleanupRetry(w.Logf, "deactivate kill switch", func() error {
			return DeactivateKillSwitch(w)
		}); err != nil {
			worldLogf(w, "behaviour cleanup: deactivate kill switch: %v", err)
		}
	}

	// --- Runtime override cleanup ---
	// Restore the install-time runtime so a later scenario on this slot
	// does not silently run under pi (or whatever this one selected).
	if w.RuntimeOverridden {
		if err := cleanupRetry(w.Logf, "restore runtime", func() error {
			return RestoreRuntime(w)
		}); err != nil {
			worldLogf(w, "behaviour cleanup: restore runtime: %v", err)
		}
	}

	// --- Allowed remote resources cleanup ---
	// Restore the pre-scenario allowed_remote_resources so a later
	// scenario on this slot does not inherit a leftover URL prefix.
	// Without this, an allowlist-negative scenario that reuses a slot
	// after a positive URL scenario sees the stale prefix and the
	// config validates when it should fail.
	if w.AllowedResourcesOverridden {
		if err := cleanupRetry(w.Logf, "restore allowed_remote_resources", func() error {
			return RestoreAllowedResources(w)
		}); err != nil {
			worldLogf(w, "behaviour cleanup: restore allowed_remote_resources: %v", err)
		}
	}

	// --- Agents cleanup ---
	// Restore the pre-scenario agents list so a later scenario on this
	// slot does not inherit custom agent entries (local or URL-sourced)
	// from the previous lessee. Without this, harness registrations
	// accumulate on the config overlay for the rest of the run.
	if w.AgentsOverridden {
		if err := cleanupRetry(w.Logf, "restore agents", func() error {
			return RestoreAgents(w)
		}); err != nil {
			worldLogf(w, "behaviour cleanup: restore agents: %v", err)
		}
	}

	// --- Reaction notification cleanup ---
	// Disable reaction notifications so the next scenario on this slot
	// is not affected by sticky config state.
	if reactionsEnabledInConfig(w) {
		if err := DisableReactionNotifications(w); err != nil {
			worldLogf(w, "behaviour cleanup: disable reaction notifications: %v", err)
		}
	}

	// --- Dummy script cleanup ---
	if len(w.DummyOps) > 0 {
		if w.Org == "" || w.RepoName == "" {
			worldLogf(w, "behaviour cleanup: clear dummy script: no repo configured; call 'Given the enrolled test repository' first")
		} else {
			if err := cleanupRetry(w.Logf, "clear dummy script", func() error {
				empty := []byte("ops: []\n")
				return w.SCM.CommitFile(ctx, w.Org, w.RepoName, w.BehaviourScriptPath(), "behaviour: clear dummy agent script", empty)
			}); err != nil {
				worldLogf(w, "behaviour cleanup: clear dummy script: %v", err)
			}
		}
	}
}

// shouldRemoveArtifactDir reports whether cleanup may delete artifactDir.
// Dirs under BEHAVIOUR_ARTIFACT_DIR are preserved for CI upload-artifact.
func shouldRemoveArtifactDir(artifactDir, ciArtifactDir string) bool {
	ciArtifactDir = strings.TrimSpace(ciArtifactDir)
	if ciArtifactDir == "" {
		return true
	}
	return !artifactDirUnderCIRoot(artifactDir, ciArtifactDir)
}

func artifactDirUnderCIRoot(dir, ciRoot string) bool {
	cleanDir := filepath.Clean(dir)
	cleanRoot := filepath.Clean(ciRoot)
	if cleanDir == cleanRoot {
		return true
	}
	return strings.HasPrefix(cleanDir, cleanRoot+string(os.PathSeparator))
}

func worldLogf(w *world.World, format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}
