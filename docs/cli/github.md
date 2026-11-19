---
sidebar_label: fullsend github
---

# fullsend github

Configure fullsend on GitHub repositories without requiring GCP credentials. All GCP infrastructure values (mint URL, WIF provider) are passed as flags.

Every command takes an `owner/repo` target. Organization-only targets (per-org mode) are no longer supported and are rejected with an error; install each repository individually, or use [`fullsend repos`](repos.md) to manage many repositories from a manifest.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend github setup <owner/repo>` | Configure fullsend for a repo |
| `fullsend github set <owner/repo> <key> <value>` | Update a single config value (secret or variable) |

## `github setup`

Configures a GitHub repository with fullsend: writes the `.fullsend/` scaffold, installs GitHub Apps, and sets repository variables and secrets.

Setup requires repo admin access only:

```bash
fullsend github setup <owner/repo> \
  --mint-url="<MINT_URL>" \
  --inference-project "<GCP_PROJECT>" \
  --inference-wif-provider "<WIF_PROVIDER>"
```

The values passed to `--inference-gateway-*` and `--openai-*` come from the gateway operator and the OpenAI organization admin; see [Using an inference gateway](../guides/user/inference-gateway.md) and [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md).

### Re-running per-repo setup

A re-run with no flags is the supported way to upgrade a single repository that is not declared in a [`fullsend repos`](repos.md) manifest, because `fullsend repos install` needs one. The steps are in [Upgrading a single repository](../guides/getting-started/operations.md#upgrading-a-single-repository).

**What the re-run changes:**

| Change | When it takes effect |
|---|---|
| Managed workflow files, including the shim trigger (`pull_request_target` → `pull_request`) and the workflow pins (moved to the CLI's release, or to `--fullsend-ref`) | In one setup PR, when you merge it |
| Repository variables and secrets, resolved from the repository's configuration (`.fullsend/config.yaml` over `config.base.yaml` over compiled defaults) | When the command runs, before the PR is merged |
| Vendored assets, when the re-run has no `--vendor` | Deleted by a separate commit on the default branch when the command runs |

Run with `--dry-run` first: it lists the files it would commit, the variable values it would set and the vendored assets it would remove.

**What the re-run keeps:**
- An existing `.fullsend/config.yaml`: `agents:` entries and their settings, allowlists and hand-written comments stay as they are. The runtime prompt is skipped, and the setup PR reports the runtime the file selects. A `config.yaml` that no longer parses fails the re-run rather than being regenerated.
- `config.base.yaml`, unchanged. Only `--config` refreshes it.
- A custom `FULLSEND_APP_SET` repository variable.
- Existing GCP secrets for `inference.project` and `inference.wif_provider` when the configuration does not set them. When the configuration sets one of the two but not the other, the re-run reuses the other's existing secret and fails with `--inference-wif-provider is required for per-repo setup (no existing secret found)` (or the `--inference-project` equivalent) when that secret is missing.

**When to add flags:**
- **`--vendor`:** pass it again if the install is vendored, or the re-run deletes the vendored assets.
- **`--config` (and `--config-hash`):** pass them again to refresh `config.base.yaml` from the preset.
- **`--fullsend-ref`:** pass it only with a ref at or after the release that installs the `pull_request` shim. An older ref pins the new shim to a reusable workflow that predates it. Without the flag the pins move to the CLI's release.
- **A value set only as a repository variable,** such as `FULLSEND_MINT_URL` or `FULLSEND_GCP_REGION`, goes back to the configuration value or the default. Put it in `config.yaml`, or pass its flag (`--mint-url`, `--inference-region`).
- **A config flag** (`--runtime`, `--agents`, `--mint-url`, `--inference-*`) changes that key on the existing `config.yaml` and keeps the rest. The file is re-serialized, so its comments are lost.

**How `--config` and config flags combine:** `--config` rewrites `config.base.yaml` from the preset (byte-for-byte) and keeps the existing overlay unless a persistent setup flag is also passed, in which case that flag is written into the overlay. Persistent setup flags may be combined with `--config`; they override the corresponding preset values. When an explicitly passed persistent flag equals the value the overlay would otherwise inherit from `config.base.yaml` or a compiled default, setup still writes it and warns that the key is now pinned locally and will not receive later changes from that layer. Remove the key from `.fullsend/config.yaml` to inherit again. Omitted flags and per-run flags (`--dry-run`, `--direct`, `--vendor`, ...) do not pin or warn.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mint-url` | | HTTPS endpoint of the token mint service (plain `http` is accepted only for `localhost` or `127.0.0.1`) |
| `--inference-provider` | | Inference provider; resolved to `vertex` if unset |
| `--inference-project` | | GCP project ID for Vertex inference; optional for per-repo setup |
| `--inference-wif-provider` | | Full WIF provider resource name; optional for per-repo setup |
| `--openai-audience` | | OpenAI Workload Identity audience for GPT on pi or codex; with the two flags below, written to `inference.openai` in `config.yaml` (all three or none) |
| `--openai-identity-provider-id` | | OpenAI Workload Identity provider ID |
| `--openai-service-account-id` | | OpenAI service account ID the provider maps this repository to |
| `--inference-gateway-url` | | Inference gateway origin, for example `https://gateway.example.com`, optionally with the path prefix the gateway serves its API under, for example `https://gateway.example.com/team-a` (`https`, no port other than 443, and no trailing `/v1`, because the runner adds the `/v1/...` paths and the egress profile allows port 443 only; plain `http` only for a loopback test host; see the [config reference](../reference/config-reference.md) for the prefix rules) for `gateway/` models on pi, codex or Claude Code ([ADR 0137](../ADRs/0137-inference-gateway-credential-route.md)), written to `inference.gateway.url` in `config.yaml`. `fullsend run` acts on the block when it is complete; in the default `oidc` mode that also needs a forge OIDC endpoint, while in the `api-key` mode it applies on local runs too (see the [config reference](../reference/config-reference.md)). The resolved block needs the URL, and in the `oidc` mode the audience too, from these flags or inherited from `config.base.yaml`; pass both flags empty to remove the repository's own block (a block in `config.base.yaml` then applies again); the same commit deletes `.fullsend/inference-gateway.json` if the repository has it, unless the inherited block still sets `models_file` to that file (setup then keeps it and says so). Check the result with [`fullsend inference gateway status`](inference.md#inference-gateway-status). Each gateway flag changes only its own key |
| `--inference-gateway-audience` | | OIDC audience the runner requests for the inference gateway (required in the `oidc` auth mode) |
| `--inference-gateway-auth` | | Inference gateway credential mode, written to `inference.gateway.auth`: `oidc` (the default; the job's OIDC token) or `api-key` ([ADR 0138](../ADRs/0138-inference-gateway-api-key-credential-mode.md)). `api-key` reads the gateway key from `FULLSEND_INFERENCE_GATEWAY_API_KEY` and needs no audience. It is supported for gateways that cannot trust forge OIDC, but relies on a long-lived secret, so use it with caution. Passing it alone never creates a block without a URL, and clearing the block removes it |
| `--inference-gateway-model` | | Inference gateway model as `id=api`, where `api` is `openai-responses`, `anthropic-messages` or `openai-completions`. Repeatable; written to `inference.gateway.models`. Mutually exclusive with `--inference-gateway-models-file` |
| `--inference-gateway-models-file` | | Local [pi-inference-gateway config file](https://github.com/fullsend-ai/pi-inference-gateway/blob/v0.1.1/docs/configuration.md#config-file) listing the gateway models. Validated with the runner's rules, committed as `.fullsend/inference-gateway.json`, and named in `inference.gateway.models_file` |
| `--inference-region` | | GCP region for inference; resolved to `global` if unset |
| `--app-set` | `fullsend-ai` | App set name prefix for GitHub Apps. For per-repo setup it is persisted as the `FULLSEND_APP_SET` repository variable; reruns that omit `--app-set` preserve an existing custom value rather than overwriting it with the default. `repos install` has no `--app-set` flag; manifest-managed repositories declare the `app_set` field in `repos.yaml` instead (see [Migrating to manifest-driven `repos install`](../guides/getting-started/migrating-from-github-setup.md)). |
| `--agents` | `triage,coder,review,fix,retro,prioritize` | Agent roles to provision |
| `--direct` | `false` | Push scaffold directly instead of creating a PR |
| `--runtime` | `claude` | Agent runtime backend (`claude`, `pi`, `codex`, `opencode`, `dummy` or `dummy-playback`; `dummy` and `dummy-playback` are for behaviour tests only — see [runtimes.md](../runtimes.md)) |
| `--fullsend-ref` | | Per-repo fullsend workflow ref override (conflicts with `--vendor`; per-repo only). A ref to an older release needs the CLI from that release: this CLI's shim passes `FULLSEND_GITHUB_RUNNER_SECRETS` ([ADR 0136](../ADRs/0136-runner-secrets-through-one-stored-secret.md)), and GitHub rejects the call when the older reusable workflow does not declare it |
| `--config` | | Local file path or HTTPS URL to a vendor preset (committed as `.fullsend/config.base.yaml`; per-repo only). Persistent setup flags override matching preset values in `.fullsend/config.yaml`. Setup refuses a preset that contains `trace_context`, because that block belongs to one repository's `repos.yaml` entry. Fleet installs declare the same source in `repos.yaml` (`defaults.config_base` / per-repo `config_base`) |
| `--config-hash` | | SHA-256 hex digest to validate the preset content (requires `--config`). Same semantics as `repos.yaml` `config_base.sha256` |

**Fetching an HTTPS `--config` preset** rejects URLs containing userinfo (e.g.
`https://user:pass@host/...`) and validates every resolved address — on the initial
request and on every redirect — against loopback, link-local, private, and cloud
metadata IP ranges. `HTTP_PROXY`/`HTTPS_PROXY` are ignored for preset fetches. This
applies to any HTTPS preset source, whether passed via `--config` or declared in
`repos.yaml` (`defaults.config_base` / per-repo `config_base`).

### Required OAuth scopes

| Scope | Required |
|-------|:--------:|
| `repo` | x |
| `workflow` | x |

## `github set`

Updates a single configuration value (secret or variable) on a GitHub repo.

```bash
fullsend github set <owner/repo> <key> <value>
```

| Key | Storage | Description |
|-----|---------|-------------|
| `FULLSEND_GCP_REGION` | Repo variable | GCP region for inference |
| `FULLSEND_REVIEW_CLIENT_ID` | Repo variable | Review app OAuth client ID |
| `FULLSEND_PER_REPO_INSTALL` | Repo variable | Per-repo install marker |
| `FULLSEND_GCP_PROJECT_ID` | Repo secret | GCP project for inference |
| `FULLSEND_GCP_WIF_PROVIDER` | Repo secret | WIF provider resource name |
| `FULLSEND_OPENAI_API_KEY` | Repo secret | Opt-in OpenAI API key used only when the WIF trio is unset. Do not add this via `github setup`; set it only when you cannot enrol OpenAI WIF. Any workflow in the repository with access to secrets can read it, so prefer keeping the key behind an [inference gateway](../guides/user/inference-gateway.md) where you can run one. |
| `FULLSEND_INFERENCE_GATEWAY_API_KEY` | Repo secret | Inference gateway API key, read only when `inference.gateway.auth` is `api-key` ([ADR 0138](../ADRs/0138-inference-gateway-api-key-credential-mode.md)). A long-lived secret: prefer `auth: oidc` where the gateway can trust forge OIDC. |

## See also

- [Configuring GitHub for fullsend](../guides/getting-started/configuring-github.md) — getting started guide
- [Advanced setup](../guides/infrastructure/advanced-setup.md) — non-standard installation paths and setup flags
- [Operations](../guides/getting-started/operations.md) — day-2 administration (configuration updates, workflow syncing, uninstall)
