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
- When invoking the fullsend CLI from this checkout, **prefer** `go run ./cmd/fullsend …` from the repo root. Do **not** use a `fullsend` binary from mise, `$PATH`, `go install`, or another checkout — those often lag this tree. A stale CLI once rewrote hosted-mint `ALLOWED_ROLES` during enrollment (dropping `e2e`/`fix`) and broke e2e. Details: [Go Code](docs/contributing/go-code.md#running-the-fullsend-cli).
- You **must** read and follow [COMMITS.md](COMMITS.md) when writing or reviewing commit messages and PR titles. Getting the prefix right is not optional — GoReleaser uses PR titles to build release notes. Breaking changes **must** carry the `!` suffix in both commit messages and PR titles; a missing `!` is an important-severity review finding.
- This repository requires a [Developer Certificate of Origin (DCO)](https://developercertificate.org/). Human-proposed commits **must** be signed off: use `git commit -s` (or add `Signed-off-by: Your Name <email>` as a trailer). Human-driven agent sessions (e.g., using Claude Code locally) should also sign off — the human directing the session is the one certifying the DCO. **Autonomous agent commits are exempt** and must never supply the DCO with `-s` or with `Signed-off-by`. These agents commit using the GitHub App's bot identity, which the [Probot DCO app](https://github.com/apps/dco) auto-skips.
- **Go coverage gate:** When changing Go production code (files under `cmd/` or `internal/` that are not `_test.go`), you **must** verify approximate patch coverage meets the 80% threshold from [`.codecov.yml`](.codecov.yml) before considering verification done. `make go-test` passing alone is not sufficient — it does not enforce Codecov thresholds. See [Verifying patch coverage locally](docs/contributing/go-code.md#verifying-patch-coverage-locally) for the exact commands. If coverage is below threshold, add tests for uncovered new/changed lines and re-check within the same run.
- Never commit secrets (tokens, API keys, PEM keys, gcloud credentials) or sensitive data (GCP project names, service account identifiers, Model Armor template names, internal hostnames). Use environment variables with no defaults for sensitive values.
- When adding a new doc under `docs/`, check `docs/.vitepress/config.ts` sidebar config. Sections using `getMarkdownFiles()` are auto-discovered. All other sections need a manual `{ text, link }` entry. Also add the new folder's prefix to `search.options.scopes` in the same file so the folder's pages are reachable when search scope pills are active.
- When removing, renaming, or changing the behavior or output format of a CLI command, public API, or user-facing feature, grep all documentation files under `docs/` for references and update them to reflect the current behavior. Pay special attention to `docs/cli/`, `docs/guides/`, and any getting-started or operations guides.
- When adding a new skill under `skills/`, check the user-facing guides for relevant cross-reference opportunities: `docs/guides/user/customizing-with-skills.md` (skill catalog and usage), `docs/guides/user/bring-your-own-agent.md` (agent composition and tuning), and `docs/guides/README.md` (guide index). Add a brief cross-reference or section pointer if the new skill fills a gap in those guides.
- **Per-org installation mode is deprecated** ([ADR 0044](docs/ADRs/0044-deprecate-per-org-installation-mode.md)) and is being removed. This applies to human contributors and agents alike: do not add or extend org-mode-specific content in docs or code, and when reviewing a PR that touches org-mode content, flag it as referencing deprecated functionality rather than engaging with the org/repo-mode distinction as active architecture. Per-repo is the sole supported installation model going forward.

## Topic-specific guidance

Detailed guidance lives in `docs/contributing/` and topic-specific guides under `docs/guides/`. Read only the file relevant to your current task — do not read all of them.

| File | When to read |
|------|-------------|
| [Runtime Implementation](docs/contributing/runtime-implementation.md) | Adding or changing a `runtime.Runtime` backend — covers the security feature matrix every runtime must fill in, the runtime interfaces, the sandbox hook contract and wire protocol, and the sandbox workspace layout |
| [Go Code](docs/contributing/go-code.md) | Changing Go code under `cmd/` or `internal/` — covers mint sync, coverage, vet, e2e tests, concurrency testing, suite-timeout policy, WASM binary size constraints, and preferring `go run` for the CLI |
| [Mintcore Architecture](docs/contributing/mintcore.md) | Changing `internal/mintcore/`, `cmd/mint-wasm/`, `cmd/mint/`, or `internal/mint/` — covers platform accessors, load-site construction, and WASM-safe wiring |
| [Behaviour Testing](docs/guides/dev/behaviour-testing.md) | Modifying behaviour test repo provisioning, fork handling, or workflow dispatch — covers forge API constraints (`auto_init`, fork name derivation, Actions readiness, CI timeout budgeting) |
| [Workflow Contracts](docs/contributing/workflow-contracts.md) | Changing GHA reusable workflows — covers dispatch sync, secret/input threading across installation-mode chains, and review rules |
| [Shell Scripting](docs/contributing/shell-scripting.md) | Writing or reviewing shell scripts — covers `gh api --paginate` pitfalls, jq patterns, and stdout contamination in command substitution |
| [Forge Abstraction](docs/contributing/forge-abstraction.md) | Adding forge operations — covers `forge.Client` interface rules |
| [Harness Composition](docs/contributing/harness-composition.md) | Changing merge functions in `internal/harness/` — covers the invariant between compose and forge merge functions |
| [Harness Field Reference](docs/contributing/harness-fields.md) | Adding or modifying fields in `Harness` or `ForgeConfig` — covers field classifications, merge rules, and the `ForgeConfig` struct |
| [CEL Triggers](docs/contributing/cel-triggers.md) | Writing or reviewing harness `trigger` CEL expressions or `.feature` CEL filters — covers normalized transition kinds |
| [ADRs](docs/contributing/adrs.md) | Touching `docs/ADRs/` or reviewing ADR changes — covers immutability and status rules |
| [Sandbox Topology](docs/contributing/sandbox-topology.md) | Modifying sandbox images, CI image pulling, or agent harness configs |
| [Bot Identities](docs/contributing/bot-identities.md) | Referencing bot identities in code — covers GitHub App logins and shared identities |
| [Design Decisions](docs/contributing/design-decisions.md) | Understanding architectural principles and key decisions |
| [Vouch System](docs/contributing/vouch-system.md) | Working with the contributor vouch gate or PR workflows |
| [Tier Conventions](docs/contributing/tier-conventions.md) | Using the term "tier" in code or docs — covers the three distinct tier contexts |
| [CI Workflows](docs/contributing/ci-workflows.md) | Adding or modifying GitHub Actions workflows under `.github/workflows/`, or adding secrets to `pull_request_target` jobs |
| [Documentation](docs/contributing/documentation.md) | Changing CLI command behavior, adding or removing subcommands, or renaming flags — covers cross-reference of CLI command groups to all documentation touchpoints |
