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

**Re-running per-repo setup** (for example after a fullsend upgrade) refreshes the managed
workflow files but never rewrites an existing `.fullsend/config.yaml` on its own: `agents:` entries and
their per-agent settings, allowlists and hand-written comments stay as they are, the runtime prompt is skipped,
and the setup PR reports the runtime the file already selects. Passing a flag that targets a
config key — `--runtime`, `--agents`, `--mint-url`, `--inference-*` — changes that key on the
existing file and keeps the rest (the file is re-serialized, so comments are not preserved in
that case). `--config` rewrites `config.base.yaml` from the preset (byte-for-byte) and keeps
the existing overlay unless a persistent setup flag is also passed, in which case that flag is
written into the overlay. Persistent setup flags may be combined with `--config`; they override
the corresponding preset values. When an explicitly passed persistent flag equals the value the
overlay would otherwise inherit from `config.base.yaml` or a compiled default, setup still writes
it and warns that the key is now pinned locally and will not receive later changes from that
layer. Remove the key from `.fullsend/config.yaml` to inherit again. Omitted flags and per-run
flags (`--dry-run`, `--direct`, `--vendor`, …) do not pin or warn. A `config.yaml` that no longer
parses fails the re-run rather than being regenerated.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--mint-url` | | HTTPS endpoint of the token mint service |
| `--inference-provider` | | Inference provider; resolved to `vertex` if unset |
| `--inference-project` | | GCP project ID for Vertex inference; optional for per-repo setup |
| `--inference-wif-provider` | | Full WIF provider resource name; optional for per-repo setup |
| `--openai-audience` | | OpenAI Workload Identity audience for GPT on pi or codex; with the two flags below, written to `inference.openai` in `config.yaml` (all three or none) |
| `--openai-identity-provider-id` | | OpenAI Workload Identity provider ID |
| `--openai-service-account-id` | | OpenAI service account ID the provider maps this repository to |
| `--inference-gateway-url` | | Inference gateway URL (`https`) for `gateway/` models on pi ([ADR 0137](../ADRs/0137-inference-gateway-credential-route.md)); with `--inference-gateway-audience`, written to `inference.gateway` in `config.yaml` (both or none; pass both empty to remove the block) |
| `--inference-gateway-audience` | | OIDC audience the runner requests for the inference gateway |
| `--inference-gateway-model` | | Inference gateway model as `id=api`, where `api` is `openai-responses`, `anthropic-messages` or `openai-completions`. Repeatable; written to `inference.gateway.models`. Mutually exclusive with `--inference-gateway-models-file` |
| `--inference-gateway-models-file` | | Local [pi-inference-gateway config file](https://github.com/fullsend-ai/pi-inference-gateway/blob/v0.1.1/docs/configuration.md#config-file) listing the gateway models. Validated with the runner's rules, committed as `.fullsend/inference-gateway.json`, and named in `inference.gateway.models_file` |
| `--inference-region` | | GCP region for inference; resolved to `global` if unset |
| `--app-set` | `fullsend-ai` | App set name prefix for GitHub Apps. For per-repo setup it is persisted as the `FULLSEND_APP_SET` repository variable; reruns that omit `--app-set` preserve an existing custom value rather than overwriting it with the default. `repos install` accepts the same option and the `app_set` manifest field. |
| `--agents` | `triage,coder,review,fix,retro,prioritize` | Agent roles to provision |
| `--direct` | `false` | Push scaffold directly instead of creating a PR |
| `--runtime` | `claude` | Agent runtime backend (`claude`, `pi`, `codex`, `dummy` or `dummy-playback`; `dummy` and `dummy-playback` are for behaviour test orgs only — see [runtimes.md](../runtimes.md)) |
| `--fullsend-ref` | | Per-repo fullsend workflow ref override (conflicts with `--vendor`; per-repo only) |
| `--config` | | Local file path or HTTPS URL to a vendor preset (committed as `.fullsend/config.base.yaml`; per-repo only). Persistent setup flags override matching preset values in `.fullsend/config.yaml`. Fleet installs declare the same source in `repos.yaml` (`defaults.config_base` / per-repo `config_base`) |
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
| `FULLSEND_OPENAI_API_KEY` | Repo secret | Opt-in OpenAI API key used only when the WIF trio is unset. Do not add this via `github setup`; set it only when you cannot enrol OpenAI WIF. |

## See also

- [Configuring GitHub for fullsend](../guides/getting-started/configuring-github.md) — getting started guide
- [Advanced setup](../guides/infrastructure/advanced-setup.md) — non-standard installation paths and setup flags
- [Operations](../guides/getting-started/operations.md) — day-2 administration (configuration updates, workflow syncing, uninstall)
