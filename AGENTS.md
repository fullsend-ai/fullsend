# AGENTS.md

Fullsend is a platform for fully autonomous agentic development for GitHub-hosted organizations. It contains design documents organized by problem domain (`docs/`) and a Go CLI (`cmd/fullsend/`) that manages GitHub App setup and org configuration. See [README.md](README.md) for the full document index.

## How to work in this repo

- Problem documents (`docs/problems/`) should present multiple options with trade-offs, not prescribe single solutions.
- Each problem document has an "Open questions" section — this is where unresolved issues live.
- When adding new problem areas, create a new file in `docs/problems/` and link it from `README.md`.
- The security threat model (threat priority: external injection > insider > drift > supply chain) should inform all other documents.
- Keep core problem documents organization-agnostic. Organization-specific details belong in `docs/problems/applied/<org-name>/`.
- The target audience for problem documents is any contributor community considering autonomous agents — keep language accessible and avoid presuming solutions.
- Always run `make lint` before submitting changes and fix any failures.
- You **must** read and follow [COMMITS.md](COMMITS.md) when writing or reviewing commit messages. Getting the prefix right is not optional — GoReleaser uses it to build release notes.
- This repository requires a [Developer Certificate of Origin (DCO)](https://developercertificate.org/). Human-proposed commits **must** be signed off: use `git commit -s` (or add `Signed-off-by: Your Name <email>` as a trailer). Human-driven agent sessions (e.g., using Claude Code locally) should also sign off — the human directing the session is the one certifying the DCO. **Autonomous agent commits are exempt** and must never supply the DCO with `-s` or with `Signed-off-by`. These agents commit using the GitHub App's bot identity, which the [Probot DCO app](https://github.com/apps/dco) auto-skips.
- Never commit secrets (tokens, API keys, PEM keys, gcloud credentials) or sensitive data (GCP project names, service account identifiers, Model Armor template names, internal hostnames). Use environment variables with no defaults for sensitive values.

## Go code

**Mint function:** The mint Cloud Function source lives in two places that must stay in sync:
- `internal/mint/main.go` — the source of truth (has its own `go.mod`, tests run from `internal/mint/`)
- `internal/dispatch/gcf/mintsrc/main.go.embed` — the embedded copy deployed as a GCP Cloud Function

When changing `internal/mint/main.go`, always copy it to `internal/dispatch/gcf/mintsrc/main.go.embed`. If `go.mod` or `go.sum` changed, sync those to `go.mod.embed` and `go.sum.embed` too.

**Mint client:** `internal/mintclient/` is the Go client for calling the mint service at runtime. It exchanges a GitHub Actions OIDC JWT for a role-scoped installation token. Unlike `internal/mint/` and `internal/mintcore/`, it has no embedded copies or sync requirements.

The `internal/mintcore/` module is shared between the mint and devmint. Its files are also embedded for Cloud Function deployment at `internal/dispatch/gcf/mintsrc/mintcore/*.embed`. When changing any file in `internal/mintcore/`, sync it to the corresponding `.embed` file under `mintsrc/mintcore/`. Note: the mint's `go.mod.embed` uses `replace mintcore => ./mintcore` (not `../mintcore`), because `provisioner.go` rewrites the replace directive at bundle time to match the deployed directory layout.

**Dispatch workflows:** The scaffold `dispatch.yml` (at `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml`) and the repo's `reusable-dispatch.yml` (at `.github/workflows/reusable-dispatch.yml`) share identical routing logic for different installation modes (per-org vs per-repo). When changing the jq payload construction, stage routing, or input/secret threading in one, apply the same change to the other.

When making changes to Go code under `cmd/` or `internal/`:

1. **Unit tests:** Run `make go-test` (or `go test ./...`) and fix any failures before committing.
2. **Vet:** Run `make go-vet` to catch common issues.
3. **E2E tests:** Run `make e2e-test` if your changes touch `internal/appsetup/`, `internal/forge/`, `internal/cli/`, or `internal/layers/`. These tests exercise the full admin install/uninstall flow against a live GitHub org using Playwright browser automation.

### Running e2e tests

The e2e tests require GitHub credentials. There are three ways to provide them:

- **`E2E_GITHUB_PASSWORD` env var:** Set directly with the password.
- **`E2E_GITHUB_PASSWORD_FILE` env var:** Set to a file path containing the password (used in devaipod environments where secrets are mounted as files).
- **`E2E_GITHUB_SESSION_FILE` env var:** Set to a pre-exported Playwright session file (skips login).
- **`E2E_GITHUB_TOTP_SECRET` env var:** Optional. The TOTP secret (base32) for the test account's 2FA. Required only when the test account has 2FA enabled — used during session export and sudo confirmation.

If only `E2E_GITHUB_USERNAME` and a password source are available, `make e2e-test` will automatically generate a session file before running tests. See `make help` for all available targets.

## Forge abstraction

All git forge operations (GitHub API calls, PR comments, issue creation, workflow dispatch, etc.) **must** go through the `forge.Client` interface defined in `internal/forge/forge.go`. This is a fundamental architectural rule — the codebase supports multiple forges (GitHub, GitLab, Forgejo) and direct coupling to any single forge breaks the abstraction.

**Prohibited outside `internal/forge/github/`:**

- `exec.Command("gh", ...)` — shelling out to the GitHub CLI
- Direct GitHub REST or GraphQL API calls (e.g., raw `net/http` to `api.github.com`)
- Any other forge-specific operation that bypasses `forge.Client`

**Where forge-specific code belongs:** Only the `internal/forge/github/` package (the GitHub implementation of `forge.Client`) should contain GitHub-specific logic. All other packages must use the `forge.Client` interface, which is injected as a dependency.

**When writing code:** If you need a forge operation that `forge.Client` does not yet support, add a new method to the interface and implement it in the GitHub client — do not work around the interface.

**When reviewing PRs:** Flag any direct `exec.Command("gh", ...)`, raw GitHub API calls, or other forge-specific operations outside `internal/forge/github/` as a medium-severity or higher finding. This is an architectural violation, not a style preference.

## Architecture Decision Records (ADRs)

These rules apply whenever you touch `docs/ADRs/` or review a PR that does. Full authoring guidance is in [`skills/writing-adrs/SKILL.md`](skills/writing-adrs/SKILL.md); invoke that skill when writing a new ADR.

**Immutability:** Once an ADR on `main` has status **Accepted**, it is a point-in-time record. Do not substantially rewrite its Context, Decision, or Consequences sections. When circumstances change, write a **new** ADR that supersedes the old one. Minor annotations are welcome: cross-references to related ADRs, short notes linking to newer decisions, typo and broken-link fixes, and status changes (e.g., to Deprecated or Superseded). Call out any edits to accepted ADRs in the PR description.

**New ADRs in pull requests:** Approval happens at **merge**, not when the branch is created. If the decision is made, set status to **Accepted** in the ADR you are proposing — not a lesser status merely because the PR is open. Valid statuses are **Accepted**, **Deprecated**, and **Superseded**. When status is Accepted, update `docs/architecture.md` and related problem docs in the same PR per the writing-adrs skill.

**When reviewing PRs:** Flag substantial rewrites to Context, Decision, or Consequences on Accepted ADRs already on `main` as a policy violation. Allow minor annotations (cross-references, short notes, typo fixes), status updates, and supersession links. For brand-new ADR files on the PR branch, evaluate whether the recorded decision matches the diff — do not treat **Accepted** on a new file as a mistake if the ADR is ready for human review at merge.

## Key design decisions made

- **Autonomy model:** Binary per-repo, with CODEOWNERS enforcing human approval on specific paths
- **Problem structure:** Problem-oriented documents (not ADRs or RFCs) that can evolve independently, with ADRs spun off later when decisions crystallize
- **Threat priority order:** External prompt injection > insider/compromised creds > agent drift > supply chain
- **Code generation is considered a solved problem.** The hard problems are review, intent, governance, and security.
- **Trust derives from repository permissions, not agent identity.** No agent trusts another based on who produced the output.
- **CODEOWNERS files are always human-owned.** Agents cannot modify their own guardrails.
- **The repo is the coordinator.** No coordinator agent — branch protection, CODEOWNERS, and status checks are the coordination layer.
- **Organization-specific content is cordoned.** Core problem docs are general; applied considerations live in `docs/problems/applied/`.
