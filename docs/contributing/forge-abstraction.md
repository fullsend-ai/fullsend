# Forge Abstraction

All git forge operations (GitHub API calls, PR comments, issue creation, workflow dispatch, etc.) **must** go through the `forge.Client` interface defined in `internal/forge/forge.go`. This is a fundamental architectural rule — the codebase supports multiple forges (GitHub, GitLab, Forgejo) and direct coupling to any single forge breaks the abstraction.

**Prohibited outside `internal/forge/github/`:**

- `exec.Command("gh", ...)` — shelling out to the GitHub CLI
- Direct GitHub REST or GraphQL API calls (e.g., raw `net/http` to `api.github.com`)
- Any other forge-specific operation that bypasses `forge.Client`

**Where forge-specific code belongs:** Only the `internal/forge/github/` package (the GitHub implementation of `forge.Client`) should contain GitHub-specific logic. All other packages must use the `forge.Client` interface, which is injected as a dependency.

**When writing code:** If you need a forge operation that `forge.Client` does not yet support, add a new method to the interface and implement it in the GitHub client — do not work around the interface.

**When reviewing PRs:** Flag any direct `exec.Command("gh", ...)`, raw GitHub API calls, or other forge-specific operations outside `internal/forge/github/` as a medium-severity or higher finding. This is an architectural violation, not a style preference.

**Composite action (`action.yml`):** The forge abstraction extends to `action.yml` bash scripts. New GitHub API operations in action steps should be implemented as `fullsend` CLI subcommands (under `internal/cli/`) that use `forge.Client`, not as inline `gh api` calls. Existing `gh api` calls in `action.yml` that predate this rule are grandfathered but should be migrated when touched.

## Security considerations for destructive operations

Forge methods that modify or destroy resources — closing PRs/MRs, deleting branches/refs, merging, force-pushing, and writing or committing new content to a branch or resource identified by a predictable name — require ownership or authorization verification at the **call site**, not inside the forge method itself. The forge method is a thin transport layer; it should not encode policy about who is allowed to act. The caller has the context to decide whether the operation is safe. The write itself is the consequential action even when no delete occurs: committing onto a well-known branch overwrites whoever currently occupies that ref.

**1. Verify ownership before acting.** Before invoking a destructive forge operation, the caller must verify that the authenticated user has a legitimate relationship to the target resource. For example, before closing a PR, confirm the PR was authored by the authenticated user (or by a known bot identity the caller controls). Do not assume that the ability to call a forge method implies authorization to use it on any resource.

When one ownership check must gate multiple related operations on the same resource (for example both a delete-and-recreate and a subsequent commit), the implementer must verify the check's result actually short-circuits every one of those operations, not just the most obviously destructive step. Computing the check and then proceeding to `CommitFilesToBranch` regardless is the same as having no check.

**2. Prefer fail-closed defaults.** When ownership or authorization data is missing or ambiguous, skip the operation rather than proceeding. If an API response omits the author field, treat it as "not ours" and do not close the PR. A missed cleanup is recoverable; silently closing someone else's PR is not.

When a fail-closed check aborts an operation that the rest of the flow depends on (for example committing onto a branch whose ownership could not be verified), the caller must surface that as an error, not a silent success. "Ownership unverifiable, nothing done" is a caller-visible failure. Returning success (for example `(delivered=false, err=nil)`) from a delivery path whose callers treat a nil error as "delivered" lets install/upgrade exit 0 while nothing was written. Best-effort cleanup helpers that skip a close or delete they cannot authorize may still return without error — the skip is the fail-closed outcome, and the rest of the flow does not depend on that cleanup having happened.

**3. Guard against predictable-name attacks.** Well-known branch names (e.g., `fullsend/onboard`, `fullsend/scaffold-install`) are predictable. An external contributor could open a PR on one of these branches before the automation runs. Without an ownership check, the automation would close a legitimate external PR or commit onto it. Always filter by author or other ownership signal before acting on resources identified by predictable names.

When the resource can be reached from more than one repo (for example fork-based contribution flows), branch name plus author is insufficient. The check must also compare repo identity (`owner/repo`) so a same-named branch in an unrelated repo is not mistaken for the target's own. Match `ChangeProposal.HeadRepo` against the repo being mutated; `Head` is only a bare ref name.

**Positive examples:** [`closeStaleScaffoldPRs`](../../internal/layers/commit.go) demonstrates ownership-before-act, fail-closed empty-author skip, and predictable-name awareness. It closes stale scaffold PRs only when the PR author matches the authenticated user (`strings.EqualFold(pr.Author, authenticatedUser)`), skips PRs with an empty author field, and operates on well-known scaffold branch names with full awareness that external actors could create PRs on those paths.

[`recreateStaleScaffoldBranch`](../../internal/layers/commit.go) in the same file (PR #7420) is the complementary write-side example. It fail-closes when `authenticatedUser` is empty or listing PRs fails (`proceedToCommit` is false); occupancy matching requires both branch name and repo identity (`pr.HeadRepo` versus `targetOwner/targetRepo`); and `commitBranchAndPR` returns an error when `proceedToCommit` is false so the subsequent `CommitFilesToBranch` is not reached and the caller does not see a silent success.

**When reviewing PRs:** Flag any new destructive forge operation (close, delete, merge, force-push, or a write/commit onto a predictable-named branch via `forge.Client`) that lacks ownership or authorization verification at the call site. Also flag these as medium-severity or higher — the same class as a forge abstraction violation:

- an ownership check whose result does not actually gate every subsequent related operation on that resource (check computed but not enforced)
- a predictable-name occupancy check that matches by branch name and author without comparing repo identity (`owner/repo`)
- a fail-closed skip that returns success (nil error) to a caller that treats nil as "delivered"
