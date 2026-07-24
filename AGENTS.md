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
- When adding a new doc under `docs/`, check `website/.vitepress/config.ts` sidebar config. Sections using `getMarkdownFiles()` are auto-discovered. All other sections need a manual `{ text, link }` entry.

## Topic-specific guidance

Detailed guidance lives in `docs/contributing/`. Read only the file relevant to your current task — do not read all of them.

| File | When to read |
|------|-------------|
| [Go Code](docs/contributing/go-code.md) | Changing Go code under `cmd/` or `internal/` — covers mint sync, coverage, vet, e2e tests, concurrency testing, and suite-timeout policy |
| [Shell Scripting](docs/contributing/shell-scripting.md) | Writing or reviewing shell scripts — covers `gh api --paginate` pitfalls and jq patterns |
| [Forge Abstraction](docs/contributing/forge-abstraction.md) | Adding forge operations — covers `forge.Client` interface rules |
| [CEL Triggers](docs/contributing/cel-triggers.md) | Writing or reviewing harness `trigger` CEL expressions or `.feature` CEL filters — covers normalized transition kinds |
| [ADRs](docs/contributing/adrs.md) | Touching `docs/ADRs/` or reviewing ADR changes — covers immutability and status rules |
| [Sandbox Topology](docs/contributing/sandbox-topology.md) | Modifying sandbox images, CI image pulling, or agent harness configs |
| [Bot Identities](docs/contributing/bot-identities.md) | Referencing bot identities in code — covers GitHub App logins and shared identities |
| [Design Decisions](docs/contributing/design-decisions.md) | Understanding architectural principles and key decisions |
| [Vouch System](docs/contributing/vouch-system.md) | Working with the contributor vouch gate or PR workflows |
| [Tier Conventions](docs/contributing/tier-conventions.md) | Using the term "tier" in code or docs — covers the three distinct tier contexts |
| [Harness Fields](docs/contributing/harness-fields.md) | Adding or modifying fields in the harness schema structs — covers expansion, environment, composition, and security pipelines |
