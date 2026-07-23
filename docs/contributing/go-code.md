# Go Code

**Mint function:** The mint Cloud Function source lives in two places that must stay in sync:
- `internal/mint/main.go` — the source of truth (has its own `go.mod`, tests run from `internal/mint/`)
- `internal/dispatch/gcf/mintsrc/main.go.embed` — the embedded copy deployed as a GCP Cloud Function

When changing `internal/mint/main.go`, always copy it to `internal/dispatch/gcf/mintsrc/main.go.embed`. If `go.mod` or `go.sum` changed, sync those to `go.mod.embed` and `go.sum.embed` too.

**Standalone mint:** `cmd/mint/` is a standalone HTTP server variant of the token mint that serves the same purpose as the GCF mint (`internal/mint/`) but runs without GCP infrastructure. Both use the shared `internal/mintcore/` library for token minting logic; they differ only in deployment model (filesystem PEM vs Secret Manager, JWKS vs STS verification). It supports custom role permissions via `CUSTOM_ROLE_PERMISSIONS` and a fallback proxy to an upstream mint. It has its own `go.mod` and tests run from `cmd/mint/`.

**Mint client:** `internal/mintclient/` is the Go client for calling the mint service at runtime. It exchanges a GitHub Actions OIDC JWT for a role-scoped installation token. Unlike `internal/mint/` and `internal/mintcore/`, it has no embedded copies or sync requirements.

The `internal/mintcore/` module is shared between the mint and devmint. Its files are also embedded for Cloud Function deployment at `internal/dispatch/gcf/mintsrc/mintcore/*.embed`. When changing any file in `internal/mintcore/`, sync it to the corresponding `.embed` file under `mintsrc/mintcore/`. Note: the mint's `go.mod.embed` uses `replace mintcore => ./mintcore` (not `../mintcore`), because `provisioner.go` rewrites the replace directive at bundle time to match the deployed directory layout.

**When adding a new file to `internal/mintcore/`:**
1. **Create the `.embed` copy:** Place it in `internal/dispatch/gcf/mintsrc/mintcore/` (required for all files — `lint-mint-embed-sync` enforces this).
2. **Register in `embeddedMintFiles`:** If the file will be included in the GCF bundle — either no build tag (e.g., `config.go`) or `//go:build !js` (e.g., `sts_verifier.go`, `gcp_pem.go`, `wif.go`) — add it to `embeddedMintFiles` in `internal/dispatch/gcf/provisioner.go` and to the `go:embed` directive.
3. **Add to `gcfSkip`:** If the file should NOT be in the GCF bundle — Worker-only files (`//go:build js`) or standalone-mint-only files — add it to the `gcfSkip` map in `TestEmbeddedMintSource_MatchesOriginal` in `provisioner_test.go` instead of `embeddedMintFiles`. The three current entries are `fetch_js.go` and `pem_js.go` (Worker-only, `//go:build js`) and `file_pem.go` (standalone-mint-only, `//go:build !js`).

**Dispatch workflows:** The scaffold `dispatch.yml` (at `internal/scaffold/fullsend-repo/.github/workflows/dispatch.yml`) and the repo's `reusable-dispatch.yml` (at `.github/workflows/reusable-dispatch.yml`) share identical routing logic for different installation modes (per-org vs per-repo). When changing the jq payload construction, stage routing, or input/secret threading in one, apply the same change to the other. The GitLab scaffold has its own dispatch template at `internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-dispatch.yml` — it follows the same two-path model (native MR events + cron-polled events) but constructs a NormalizedEvent v1 payload (ADR 0061) from GitLab CI variables. Stage routing uses shell checks annotated with equivalent CEL trigger expressions; when built-in harness triggers land (#2896-2901), the routing can be replaced by `fullsend dispatch --input-driver json`.

When making changes to Go code under `cmd/` or `internal/`:

1. **Unit tests:** Run `make go-test` (or `go test ./...`) and fix any failures before committing.
2. **Coverage:** CI enforces thresholds via [Codecov](https://about.codecov.io/) (see [`.codecov.yml`](../../.codecov.yml)). **Patch coverage** on changed lines must meet **80%** (with a 5% tolerance). **Project coverage** must not drop more than **1%** below the base branch. `make go-test` runs tests with `-cover` locally but does not enforce these thresholds — a PR can still fail the Codecov status check if new or changed code lacks tests. Add or extend `_test.go` files for logic you introduce or modify.
3. **Vet:** Run `make go-vet` to catch common issues.
4. **E2E tests:** Run `make e2e-test` if your changes touch `internal/appsetup/`, `internal/forge/`, `internal/cli/`, or `internal/layers/`. These tests exercise the full admin install/uninstall flow against live GitHub pool orgs using mint/OIDC authentication.

## Running e2e tests

The e2e tests mint short-lived GitHub App installation tokens via the central token mint. Pool-org admin operations use mint/OIDC in CI and do not require a dedicated mint URL secret.

- **CI (mint):** Uses the hosted public mint (same default as `fullsend admin --mint-url`) with the workflow's OIDC identity. The e2e workflow exchanges the OIDC JWT for an `e2e`-role installation token on the pool org. Override with `FULLSEND_MINT_URL` if needed.
- **Local:** Run `gh auth login` (or set `GH_TOKEN` / `GITHUB_TOKEN` with pool-org admin access). Mint uses `FULLSEND_MINT_URL` or the hosted default.

See [`docs/guides/dev/e2e-testing.md`](../guides/dev/e2e-testing.md) and `make help` for pool org setup and troubleshooting.
