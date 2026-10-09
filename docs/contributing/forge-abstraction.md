# Forge Abstraction

All git forge operations (GitHub API calls, PR comments, issue creation, workflow dispatch, etc.) **must** go through the `forge.Client` interface defined in `internal/forge/forge.go`. This is a fundamental architectural rule — the codebase supports multiple forges (GitHub, GitLab, Forgejo) and direct coupling to any single forge breaks the abstraction.

**Prohibited outside the matching `forge.Client` implementation** (`internal/forge/github/`, `internal/forge/gitlab/`, and any future Forgejo client):

- `exec.Command("gh", ...)` — shelling out to the GitHub CLI (a GitHub-specific illustration of the general rule)
- Direct forge REST or GraphQL API calls (e.g., raw `net/http` to `api.github.com`)
- Any other forge-specific operation that bypasses `forge.Client`

**Where forge-specific code belongs:** Only the matching `forge.Client` implementation (e.g. `internal/forge/github/`, `internal/forge/gitlab/`) should contain that forge's specific logic. All other packages must use the `forge.Client` interface, which is injected as a dependency.

**When writing code:** If you need a forge operation that `forge.Client` does not yet support, add a new method to the interface and implement it in each live forge client (`internal/forge/github/`, `internal/forge/gitlab/`, and future Forgejo) — do not work around the interface.

**When reviewing PRs:** Flag any direct `exec.Command("gh", ...)`, raw forge API calls, or other forge-specific operations outside the matching `forge.Client` implementation as a medium-severity or higher finding. This is an architectural violation, not a style preference.

**CI scaffold scripts (`action.yml`, `.gitlab/ci/scripts/*.sh`):** The forge abstraction extends to CI scaffold scripts across every forge, not only the GitHub composite action. New forge API operations added to `action.yml`, GitHub scaffold scripts under `internal/scaffold/fullsend-repo/`, GitLab scaffold scripts under `internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/`, or any future Forgejo scaffold must be implemented as `fullsend` CLI subcommands (under `internal/cli/`) that use `forge.Client`. Do not add inline `gh api`, `glab`, or authenticated `curl`/`wget` calls that talk to a forge API.

**Negative example (PR #7793):** a closed PR added this lookup inside `run-agent-job.sh` to read an MR's source branch:

```bash
curl "${CI_API_V4_URL}/projects/${CI_PROJECT_ID}/merge_requests/${MR_IID}" \
  -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"
```

That is a new direct forge API path in the GitLab scaffold. Route the lookup through a `fullsend` CLI subcommand backed by `forge.Client` instead of adding another inline `curl`.

Existing `gh api` calls in `action.yml` (currently none) and under `internal/scaffold/fullsend-repo/` (GitHub scaffold scripts and workflow templates), and existing authenticated `curl` calls in `internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/scripts/`, that predate this rule are grandfathered but should be migrated when touched. Adding another call of the same shape is not grandfathered.

**When reviewing PRs:** Flag any new inline `gh api`, `glab`, or authenticated `curl`/`wget` call to a forge API in a CI scaffold script as a medium-severity or higher finding — the same class as a Go-side `exec.Command("gh", ...)` bypass.

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
