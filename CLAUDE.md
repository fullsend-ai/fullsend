# CLAUDE.md

Fullsend is a platform for fully autonomous agentic development for GitHub-hosted organizations. It contains design documents organized by problem domain (`docs/`) and a Go CLI (`cmd/fullsend/`) that manages GitHub App setup and org configuration. See [README.md](README.md) for the full document index.

## How to work in this repo

- This is a design exploration, not a spec. Documents should present multiple options with trade-offs, not prescribe single solutions.
- Each problem document has an "Open questions" section — this is where unresolved issues live.
- When adding new problem areas, create a new file in `docs/problems/` and link it from `README.md`.
- The security threat model (threat priority: external injection > insider > drift > supply chain) should inform all other documents.
- Keep core problem documents organization-agnostic. Organization-specific details belong in `docs/problems/applied/<org-name>/`.
- The target audience is any contributor community considering autonomous agents — keep language accessible, avoid presuming solutions.
- Always run `make lint` before submitting changes and fix any failures.
- You **must** read and follow [COMMITS.md](COMMITS.md) when writing or reviewing commit messages. Getting the prefix right is not optional — GoReleaser uses it to build release notes.
- Never commit secrets (tokens, API keys, PEM keys, gcloud credentials) or sensitive data (GCP project names, service account identifiers, Model Armor template names, internal hostnames). Use environment variables with no defaults for sensitive values.

## Go code

**Mint function:** The mint Cloud Function source lives in two places that must stay in sync:
- `internal/mint/main.go` — the source of truth (has its own `go.mod`, tests run from `internal/mint/`)
- `internal/dispatch/gcf/mintsrc/main.go.embed` — the embedded copy deployed as a GCP Cloud Function

When changing `internal/mint/main.go`, always copy it to `internal/dispatch/gcf/mintsrc/main.go.embed`. If `go.mod` or `go.sum` changed, sync those to `go.mod.embed` and `go.sum.embed` too.

The `internal/mintcore/` module is shared between the mint and devmint. Its files are also embedded for Cloud Function deployment at `internal/dispatch/gcf/mintsrc/mintcore/*.embed`. When changing any file in `internal/mintcore/`, sync it to the corresponding `.embed` file under `mintsrc/mintcore/`. Note: the mint's `go.mod.embed` uses `replace mintcore => ./mintcore` (not `../mintcore`), because `provisioner.go` rewrites the replace directive at bundle time to match the deployed directory layout.

**Dispatch workflows:** The scaffold `dispatch.yml` (at `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml`) and the repo's `reusable-dispatch.yml` (at `.github/workflows/reusable-dispatch.yml`) share identical routing logic for different installation modes (per-org vs per-repo). When changing the jq payload construction, stage routing, or input/secret threading in one, apply the same change to the other.

**Forge abstraction:** All git forge operations must go through the `forge.Client` interface in `internal/forge/forge.go`. Do not use `exec.Command("gh", ...)` or direct GitHub API calls outside `internal/forge/github/`. See [AGENTS.md](AGENTS.md#forge-abstraction) for details.

When making changes to Go code under `cmd/` or `internal/`:

1. **Unit tests:** Run `make go-test` (or `go test ./...`) and fix any failures before committing.
2. **Vet:** Run `make go-vet` to catch common issues.
3. **E2E tests:** Run `make e2e-test` if your changes touch `internal/appsetup/`, `internal/forge/`, `internal/cli/`, `internal/layers/`, or `internal/mintcore/`. These tests exercise the full admin install/uninstall flow against a live GitHub org from the halfsend pool.

### Running e2e tests

**CI:** GitHub Actions mints cross-org installation tokens via `E2E_MINT_URL` (role `e2e`, `target_org` = pool org). Pool orgs must install the e2e app and set `FULLSEND_FOREIGN_E2E_REPOS=fullsend-ai/fullsend` — see [ADR 0046](docs/ADRs/0046-cross-org-mint-authorization-via-org-variables.md) and [e2e-testing.md](docs/guides/dev/e2e-testing.md).

**Local:** Authenticate with pool-org admin access:

```bash
gh auth login --web
make e2e-test
```

Alternatively set `GH_TOKEN` or `GITHUB_TOKEN`. See `make help` and [e2e-testing.md](docs/guides/dev/e2e-testing.md) for pool provisioning (`hack/setup-new-e2e-org.sh`) and CI secrets.

## Key design decisions made

- **Autonomy model:** Binary per-repo, with CODEOWNERS enforcing human approval on specific paths
- **Problem structure:** Problem-oriented documents (not ADRs or RFCs) that can evolve independently, with ADRs spun off later when decisions crystallize
- **Threat priority order:** External prompt injection > insider/compromised creds > agent drift > supply chain
- **Code generation is considered a solved problem.** The hard problems are review, intent, governance, and security.
- **Trust derives from repository permissions, not agent identity.** No agent trusts another based on who produced the output.
- **CODEOWNERS files are always human-owned.** Agents cannot modify their own guardrails.
- **The repo is the coordinator.** No coordinator agent — branch protection, CODEOWNERS, and status checks are the coordination layer.
- **Organization-specific content is cordoned.** Core problem docs are general; applied considerations live in `docs/problems/applied/`.
