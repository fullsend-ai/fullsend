# AGENTS.md

Fullsend is a platform for fully autonomous agentic development for Git-hosted organizations (GitHub, GitLab, Forgejo). It contains design documents organized by problem domain (`docs/`) and a Go CLI (`cmd/fullsend/`) that manages forge setup and org configuration. See [fullsend.sh](https://fullsend.sh) for the full documentation site.

## How to work in this repo

- Problem documents (`docs/problems/`) should present multiple options with trade-offs, not prescribe single solutions.
- Each problem document has an "Open questions" section — this is where unresolved issues live.
- When adding new problem areas, create a new file in `docs/problems/`. The documentation site auto-discovers files in this directory.
- The security threat model (threat priority: external injection > insider > drift > supply chain) should inform all other documents.
- Keep core problem documents organization-agnostic. Organization-specific details belong in `docs/problems/applied/<org-name>/`.
- The target audience for problem documents is any contributor community considering autonomous agents — keep language accessible and avoid presuming solutions.
- Always stage your changes before running `make lint` and fix any failures. Pre-commit only checks staged files — without staging first, it stashes your work and finds nothing to lint.
- You **must** read and follow [COMMITS.md](COMMITS.md) when writing or reviewing commit messages and PR titles. Getting the prefix right is not optional — GoReleaser uses PR titles to build release notes. Breaking changes **must** carry the `!` suffix in both commit messages and PR titles; a missing `!` is an important-severity review finding.
- This repository requires a [Developer Certificate of Origin (DCO)](https://developercertificate.org/). Human-proposed commits **must** be signed off: use `git commit -s` (or add `Signed-off-by: Your Name <email>` as a trailer). Human-driven agent sessions (e.g., using Claude Code locally) should also sign off — the human directing the session is the one certifying the DCO. **Autonomous agent commits are exempt** and must never supply the DCO with `-s` or with `Signed-off-by`. These agents commit using the GitHub App's bot identity, which the [Probot DCO app](https://github.com/apps/dco) auto-skips.
- Never commit secrets (tokens, API keys, PEM keys, gcloud credentials) or sensitive data (GCP project names, service account identifiers, Model Armor template names, internal hostnames). Use environment variables with no defaults for sensitive values.

## Go code

**Mint function:** The mint Cloud Function source lives in two places that must stay in sync:
- `internal/mint/main.go` — the source of truth (has its own `go.mod`, tests run from `internal/mint/`)
- `internal/dispatch/gcf/mintsrc/main.go.embed` — the embedded copy deployed as a GCP Cloud Function

When changing `internal/mint/main.go`, always copy it to `internal/dispatch/gcf/mintsrc/main.go.embed`. If `go.mod` or `go.sum` changed, sync those to `go.mod.embed` and `go.sum.embed` too.

**Standalone mint:** `cmd/mint/` is a standalone HTTP server variant of the token mint that serves the same purpose as the GCF mint (`internal/mint/`) but runs without GCP infrastructure. Both use the shared `internal/mintcore/` library for token minting logic; they differ only in deployment model (filesystem PEM vs Secret Manager, JWKS vs STS verification). It supports custom role permissions via `CUSTOM_ROLE_PERMISSIONS` and a fallback proxy to an upstream mint. It has its own `go.mod` and tests run from `cmd/mint/`.

**Mint client:** `internal/mintclient/` is the Go client for calling the mint service at runtime. It exchanges a GitHub Actions OIDC JWT for a role-scoped installation token. Unlike `internal/mint/` and `internal/mintcore/`, it has no embedded copies or sync requirements.

The `internal/mintcore/` module is shared between the mint and devmint. Its files are also embedded for Cloud Function deployment at `internal/dispatch/gcf/mintsrc/mintcore/*.embed`. When changing any file in `internal/mintcore/`, sync it to the corresponding `.embed` file under `mintsrc/mintcore/`. Note: the mint's `go.mod.embed` uses `replace mintcore => ./mintcore` (not `../mintcore`), because `provisioner.go` rewrites the replace directive at bundle time to match the deployed directory layout.

**Workflow contracts:** The scaffold `dispatch.yml` (at `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml`) and the repo's `reusable-dispatch.yml` (at `.github/workflows/reusable-dispatch.yml`) share identical routing logic for different installation modes (per-org vs per-repo). When changing the jq payload construction, stage routing, or input/secret threading in one, apply the same change to the other.

More broadly, GHA reusable workflows do not inherit secrets or variables — they must be explicitly forwarded by every caller. When any reusable workflow (`.github/workflows/reusable-*.yml`) adds a new `secrets:` or `inputs:` entry, trace both installation-mode chains and ensure every hop forwards the new entry:

- **Per-org chain:** scaffold thin callers (`internal/scaffold/fullsend-repo/.github/workflows/<agent>.yml`) → reusable workflow (`.github/workflows/reusable-<agent>.yml`)
- **Per-repo chain:** shim template → `reusable-dispatch.yml` → `reusable-<agent>.yml` ([ADR 62](docs/ADRs/0062-dispatch-version-skew.md) will inline stages into `reusable-dispatch.yml`, collapsing this to a single hop; new secrets must then be threaded to the inlined stage jobs directly)

Omitted optional secrets arrive as empty strings at runtime, which silently breaks authenticated backends instead of failing loudly. Treat a missing forwarding hop the same as a missing sync — it is a correctness bug, not a cosmetic issue.

**When reviewing PRs:** If a diff adds or renames a `secrets:` or `inputs:` entry in a reusable workflow, check that all callers in both chains have been updated. Flag a missing forwarding hop as a medium-severity or higher finding.

When making changes to Go code under `cmd/` or `internal/`:

1. **Unit tests:** Run `make go-test` (or `go test ./...`) and fix any failures before committing.
2. **Coverage:** CI enforces thresholds via [Codecov](https://about.codecov.io/) (see [`.codecov.yml`](.codecov.yml)). **Patch coverage** on changed lines must meet **80%** (with a 5% tolerance). **Project coverage** must not drop more than **1%** below the base branch. `make go-test` runs tests with `-cover` locally but does not enforce these thresholds — a PR can still fail the Codecov status check if new or changed code lacks tests. Add or extend `_test.go` files for logic you introduce or modify.
3. **Vet:** Run `make go-vet` to catch common issues.
4. **E2E tests:** Run `make e2e-test` if your changes touch `internal/appsetup/`, `internal/forge/`, `internal/cli/`, or `internal/layers/`. These tests exercise the full admin install/uninstall flow against live GitHub pool orgs using mint/OIDC authentication.

### Running e2e tests

The e2e tests mint short-lived GitHub App installation tokens via the central token mint. Pool-org admin operations use mint/OIDC in CI and do not require a dedicated mint URL secret.

- **CI (mint):** Uses the hosted public mint (same default as `fullsend admin --mint-url`) with the workflow's OIDC identity. The e2e workflow exchanges the OIDC JWT for an `e2e`-role installation token on the pool org. Override with `FULLSEND_MINT_URL` if needed.
- **Local:** Run `gh auth login` (or set `GH_TOKEN` / `GITHUB_TOKEN` with pool-org admin access). Mint uses `FULLSEND_MINT_URL` or the hosted default.

See `docs/guides/dev/e2e-testing.md` and `make help` for pool org setup and troubleshooting.

## Shell scripting

### `gh api --paginate` and jq

`gh api --paginate` applies the `--jq` expression **independently to each page** of results, not to the combined output. This is a documented `gh` CLI behavior and a common source of bugs.

**Do not** use aggregating jq filters directly in `--jq` with `--paginate`:

```bash
# WRONG — `length` runs per-page; produces one number per page, not a total
count=$(gh api --paginate /repos/{owner}/{repo}/issues/comments --jq 'length')
```

**Do** collect all pages first, then pipe to a separate `jq -s` (slurp) call. `jq -s` slurps the input into an array; use `add` to flatten before aggregating:

```bash
# CORRECT — slurp all pages, flatten with add, then aggregate
count=$(gh api --paginate /repos/{owner}/{repo}/issues/comments | jq -s 'add | length')
```

Without `--jq`, `gh api --paginate` merges all page arrays into a single flat JSON array before writing to stdout. `jq -s` then wraps that into an array-of-one; `add` unwraps it back to the flat array, and the aggregating filter runs once over all items. This pattern is defensive — it works correctly whether the upstream emits one merged array or (as when `--jq` is present) one array per page.

This applies to any aggregating filter: `length`, `sort_by`, `group_by`, `add`, `min_by`, `max_by`, etc. If the filter only selects or transforms individual items (e.g., `.[] | .id`), per-page application is fine — but pipe the result through a final `jq -s` step before any cross-page aggregation.

**When reviewing shell scripts:** Flag `--paginate --jq '... | length'` (or any other aggregating filter in `--jq`) as a medium-severity finding. The fix is always to move the aggregation to a separate `| jq -s 'add | ...'` pipe.

**Alternative — `--slurp` flag:** When no inline `--jq` transform is needed, `gh api --paginate --slurp` combines pages into a single array directly. However, `--slurp` is mutually exclusive with `--jq` (errors with `"the --slurp option is not supported with --jq or --template"`), so the `| jq -s 'add | ...'` pipe pattern is required whenever you also need per-item filtering.

## Forge abstraction

All git forge operations (GitHub API calls, PR comments, issue creation, workflow dispatch, etc.) **must** go through the `forge.Client` interface defined in `internal/forge/forge.go`. This is a fundamental architectural rule — the codebase supports multiple forges (GitHub, GitLab, Forgejo) and direct coupling to any single forge breaks the abstraction.

**Prohibited outside `internal/forge/github/`:**

- `exec.Command("gh", ...)` — shelling out to the GitHub CLI
- Direct GitHub REST or GraphQL API calls (e.g., raw `net/http` to `api.github.com`)
- Any other forge-specific operation that bypasses `forge.Client`

**Where forge-specific code belongs:** Only the `internal/forge/github/` package (the GitHub implementation of `forge.Client`) should contain GitHub-specific logic. All other packages must use the `forge.Client` interface, which is injected as a dependency.

**When writing code:** If you need a forge operation that `forge.Client` does not yet support, add a new method to the interface and implement it in the GitHub client — do not work around the interface.

**When reviewing PRs:** Flag any direct `exec.Command("gh", ...)`, raw GitHub API calls, or other forge-specific operations outside `internal/forge/github/` as a medium-severity or higher finding. This is an architectural violation, not a style preference.

**Composite action (`action.yml`):** The forge abstraction extends to `action.yml` bash scripts. New GitHub API operations in action steps should be implemented as `fullsend` CLI subcommands (under `internal/cli/`) that use `forge.Client`, not as inline `gh api` calls. Existing `gh api` calls in `action.yml` that predate this rule are grandfathered but should be migrated when touched.

## Architecture Decision Records (ADRs)

These rules apply whenever you touch `docs/ADRs/` or review a PR that does. Full authoring guidance is in [`skills/writing-adrs/SKILL.md`](skills/writing-adrs/SKILL.md); invoke that skill when writing a new ADR.

**Immutability:** Once an ADR on `main` has status **Accepted**, it is a point-in-time record. Do not substantially rewrite its Context, Decision, or Consequences sections. When circumstances change, write a **new** ADR that supersedes the old one. Minor annotations are welcome: cross-references to related ADRs, short notes linking to newer decisions, typo and broken-link fixes, and status changes (e.g., to Deprecated or Superseded). Call out any edits to accepted ADRs in the PR description.

**New ADRs in pull requests:** Approval happens at **merge**, not when the branch is created. If the decision is made, set status to **Accepted** in the ADR you are proposing — not a lesser status merely because the PR is open. Valid statuses are **Accepted**, **Deprecated**, and **Superseded**. When status is Accepted, update `docs/architecture.md` and related problem docs in the same PR per the writing-adrs skill.

**When reviewing PRs:** Flag substantial rewrites to Context, Decision, or Consequences on Accepted ADRs already on `main` as a policy violation. Allow minor annotations (cross-references, short notes, typo fixes), status updates, and supersession links. For brand-new ADR files on the PR branch, evaluate whether the recorded decision matches the diff — do not treat **Accepted** on a new file as a mistake if the ADR is ready for human review at merge.

## Documentation site

When adding a new doc under `docs/`, check `website/.vitepress/config.ts` sidebar config. Sections using `getMarkdownFiles()` are auto-discovered. All other sections need a manual `{ text, link }` entry.

## Sandbox image topology

Fullsend agents run inside sandboxed containers. Two images exist in a
parent–child hierarchy; which image an agent uses depends on whether it
needs a compiled-language toolchain.

```
ghcr.io/nvidia/openshell-community/sandboxes/base   (upstream)
  └── fullsend-sandbox                                (base sandbox)
        └── fullsend-code                             (extends base with Go)
```

| Image | Agents | Run frequency | Key additions over parent |
|-------|--------|---------------|--------------------------|
| `fullsend-sandbox` | triage, prioritize, retro | High (most agent runs) | Claude Code, jq, gitleaks, acli, pre-commit, gitlint, tirith, ProtectAI DeBERTa model |
| `fullsend-code` | code, fix, review | Lower (code/fix are the least-run agents; review runs per-PR) | Go toolchain, scan-secrets, gopls, lychee |

Harness definitions that map agents to images live in
`internal/scaffold/fullsend-repo/harness/*.yaml` (the `image:` field).
Image Containerfiles live in `images/sandbox/` and `images/code/`.
The CI build pipeline is `.github/workflows/sandbox-images.yml`.

**When reviewing CI changes:** If a PR modifies image pulling, caching,
or pre-warming logic in `action.yml`, consider which agent types are
affected. Changes that only benefit `fullsend-code` have a smaller blast
radius (fewer agent runs) than changes to `fullsend-sandbox`. A cache or
pull optimization may not be worth the complexity if it only helps the
least-frequently-run agents.

## Bot identities

Fullsend agents authenticate as GitHub Apps; the table below also includes non-agent bots that appear in trusted-actor lists. Multiple agent roles may share a single app identity. The GitHub App login is derived from the `slug` field in each harness file (`internal/scaffold/fullsend-repo/harness/*.yaml`).

| Agent role | GitHub App login | Notes |
|---|---|---|
| code | `fullsend-ai-coder[bot]` | Opens PRs from issues |
| fix | `fullsend-ai-coder[bot]` | Shares the coder app; pushes to existing PR branches |
| review | `fullsend-ai-review[bot]` | Posts review comments |
| triage | `fullsend-ai-triage[bot]` | Posts triage summaries on issues |
| retro | `fullsend-ai-retro[bot]` | Files retro issues, posts PR comments |
| prioritize | `fullsend-ai-prioritize[bot]` | Prioritizes issues |
| renovate | `renovate-fullsend[bot]` | Dependency updates (not a fullsend agent) |

When referencing bot identities in code (e.g., trusted actor lists, dispatch filters), always verify the login name against this table. Do not assume each agent role has a unique app identity — the fix agent reuses `fullsend-ai-coder[bot]`, not a separate `fullsend-ai-fix[bot]`.

## Key design decisions made

- **Autonomy model:** Binary per-repo, with CODEOWNERS enforcing human approval on specific paths
- **Problem structure:** Problem-oriented documents (not ADRs or RFCs) that can evolve independently, with ADRs spun off later when decisions crystallize
- **Threat priority order:** External prompt injection > insider/compromised creds > agent drift > supply chain
- **Code generation is considered a solved problem.** The hard problems are review, intent, governance, and security.
- **Trust derives from repository permissions, not agent identity.** No agent trusts another based on who produced the output.
- **CODEOWNERS files are always human-owned.** Agents cannot modify their own guardrails.
- **The repo is the coordinator.** No coordinator agent — branch protection, CODEOWNERS, and status checks are the coordination layer.
- **Organization-specific content is cordoned.** Core problem docs are general; applied considerations live in `docs/problems/applied/`.

## Vouch System

- First-time external contributors must be vouched before their PRs are accepted. The `vouch-check` workflow auto-closes PRs from unvouched users.
- Org members and collaborators with write access bypass the vouch gate automatically.
- Maintainers vouch users by commenting `/vouch` on a Vouch Request discussion. The `vouch-command` workflow appends the username to `.github/VOUCHED.td` on the `vouched` branch.
- Agent bot identities (`fullsend-ai-*[bot]`, `renovate-fullsend[bot]`, `github-actions[bot]`) are skipped automatically because they have `user.type: 'Bot'`.
- The `vouched` branch is protected — only the `vouch-command` workflow (via `GITHUB_TOKEN`) can push to it. Do not push to, rebase, or target PRs at the `vouched` branch.
- The vouch gate is separate from the e2e authorization gate. Vouch determines whether a PR stays open; e2e authorization determines whether tests run.
- PRs from unvouched external contributors are automatically closed with a comment linking to the vouch process.
- PRs should follow the PR template structure: Summary, Related Issue, Changes, Testing, Checklist.

## Terminology: tier conventions

The term "tier" is used in multiple distinct contexts across this codebase. Always use a descriptive prefix to avoid ambiguity:

| Prefix | Meaning | Defined in |
|---|---|---|
| **credential delivery tier** | The four-tier model for how agents receive credentials: (1) prefetch + post-process, (2) providers + L7, (3) host-side REST server, (4) host files | [ADR 0025](docs/ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md) |
| **intent authorization tier** | The four-tier model for change authorization: (0) standing rules, (1) tactical/issue, (2) strategic, (3) organizational | [intent-representation.md](docs/problems/intent-representation.md) |
| **configuration tier** | The three-tier inheritance model for agent configuration: upstream defaults → org config → per-repo overrides. The `customized/` overlay mechanism (ADR-0035) is deprecated; use config-driven agent registration per [ADR 0064](docs/ADRs/0064-deprecate-customized-directory-overlay.md) | [ADR 0035](docs/ADRs/0035-layered-content-resolution.md) |

**Do not** use bare "Tier N" or "tier" without a prefix — the same number means different things in different contexts (e.g., "Tier 2" could be provider-based credential delivery or strategic intent authorization). External tier references (e.g., "GitLab Free tier", "GitHub plan tiers") are exempt from this convention.
