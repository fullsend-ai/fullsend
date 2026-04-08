# CLAUDE.md

Fullsend is a platform for fully autonomous agentic development for GitHub-hosted organizations. It contains design documents organized by problem domain (`docs/`) and a Go CLI (`cmd/fullsend/`) that manages GitHub App setup and org configuration. See [README.md](README.md) for the full document index.

## How to work in this repo

- This is a design exploration, not a spec. Documents should present multiple options with trade-offs, not prescribe single solutions.
- Each problem document has an "Open questions" section — this is where unresolved issues live.
- When adding new problem areas, create a new file in `docs/problems/` and link it from `README.md`.
- The security threat model (threat priority: external injection > insider > drift > supply chain) should inform all other documents.
- Keep core problem documents organization-agnostic. Organization-specific details belong in `docs/problems/applied/<org-name>/`.
- The target audience is any contributor community considering autonomous agents — keep language accessible, avoid presuming solutions.
- Always run `pre-commit run --files <changed-files>` before submitting changes and fix any failures.
- Never commit secrets (tokens, API keys, PEM keys, gcloud credentials) or sensitive data (GCP project names, service account identifiers, Model Armor template names, internal hostnames). Use environment variables with no defaults for sensitive values.

## Go code

When making changes to Go code under `cmd/` or `internal/`:

1. **Unit tests:** Run `make go-test` (or `go test ./...`) and fix any failures before committing.
2. **Vet:** Run `make go-vet` to catch common issues.
3. **E2E tests:** Run `make e2e-test` if your changes touch `internal/appsetup/`, `internal/forge/`, `internal/cli/`, or `internal/layers/`. These tests exercise the full admin install/uninstall flow against a live GitHub org using Playwright browser automation.

### Running e2e tests

The e2e tests require GitHub credentials. There are three ways to provide them:

- **`E2E_GITHUB_PASSWORD` env var:** Set directly with the password.
- **`E2E_GITHUB_PASSWORD_FILE` env var:** Set to a file path containing the password (used in devaipod environments where secrets are mounted as files).
- **`E2E_GITHUB_SESSION_FILE` env var:** Set to a pre-exported Playwright session file (skips login).

If only `E2E_GITHUB_USERNAME` and a password source are available, `make e2e-test` will automatically generate a session file before running tests. See `make help` for all available targets.

## Key design decisions made

- **Autonomy model:** Binary per-repo, with CODEOWNERS enforcing human approval on specific paths
- **Problem structure:** Problem-oriented documents (not ADRs or RFCs) that can evolve independently, with ADRs spun off later when decisions crystallize
- **Threat priority order:** External prompt injection > insider/compromised creds > agent drift > supply chain
- **Code generation is considered a solved problem.** The hard problems are review, intent, governance, and security.
- **Trust derives from repository permissions, not agent identity.** No agent trusts another based on who produced the output.
- **CODEOWNERS files are always human-owned.** Agents cannot modify their own guardrails.
- **The repo is the coordinator.** No coordinator agent — branch protection, CODEOWNERS, and status checks are the coordination layer.
- **Organization-specific content is cordoned.** Core problem docs are general; applied considerations live in `docs/problems/applied/`.
