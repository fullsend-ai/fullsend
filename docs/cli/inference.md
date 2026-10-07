---
sidebar_label: fullsend inference
---

# fullsend inference

Manage the inference credentials agent runs use. `provision`, `deprovision` and `status` create, inspect, and remove the GCP Workload Identity Federation (WIF) pool, OIDC provider, and IAM bindings that let GitHub Actions workflows authenticate with GCP for Agent Platform (Vertex) access. [`openai`](#inference-openai) enrols repositories with OpenAI WIF for GPT models on the pi runtime.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend inference provision <owner/repo>` | Create WIF pool/provider and grant Agent Platform access |
| `fullsend inference deprovision <owner/repo>` | Delete the repository's WIF provider |
| `fullsend inference status <owner/repo>` | Check WIF health and print config values |
| `fullsend inference openai request <owner/repo>[,…]` | Generate WIF provider/mapping request for OpenAI admin |
| `fullsend inference openai import [reply.json]` | Import OpenAI WIF identifiers into config |
| `fullsend inference openai status <owner/repo>` | Check OpenAI WIF configuration and exchange status |

## `inference provision`

Creates a WIF pool (`fullsend-inference`) if needed, a dedicated OIDC provider scoped to a single repository (attribute condition `assertion.repository == '<owner/repo>'`, provider ID derived from owner/repo), and grants `roles/aiplatform.user` to the repository's WIF principal. Idempotent and safe to re-run.

```bash
fullsend inference provision <owner/repo> \
  --project "<GCP_PROJECT>"
```

The target must be `owner/repo`. Org-scoped inference has been removed: a bare org argument is rejected, so provision each repository separately.

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID (required) |
| `--pool` | `fullsend-inference` | WIF pool name (always created at `locations/global`) |
| `--dry-run` | `false` | Preview changes without making them |

### Required IAM roles

| Role | Description |
|------|-------------|
| `roles/iam.workloadIdentityPoolAdmin` | Create WIF pool and provider |
| `roles/resourcemanager.projectIamAdmin` | Grant `roles/aiplatform.user` to WIF principals |

### Required GCP APIs

```bash
gcloud services enable \
  iam.googleapis.com \
  cloudresourcemanager.googleapis.com \
  aiplatform.googleapis.com \
  --project="$GCP_PROJECT"
```

## `inference deprovision`

Removes a repository's inference access by deleting its dedicated WIF provider. The WIF pool is left in place for other repositories. The `roles/aiplatform.user` IAM binding is **not** revoked automatically; remove it manually to fully revoke access.

```bash
fullsend inference deprovision <owner/repo> \
  --project "<GCP_PROJECT>"
```

Accepts `--project`, `--pool`, and `--dry-run` (same as `provision`).

### Required IAM roles

| Role | Description |
|------|-------------|
| `roles/iam.workloadIdentityPoolAdmin` | Delete WIF providers |

## `inference status`

Checks WIF health and prints the configuration values needed for `github setup`.

```bash
fullsend inference status <owner/repo> \
  --project "<GCP_PROJECT>"
```

Read-only — makes no changes. Accepts `--project`, `--pool`, and `--format` (`text`, `json`, or `env`). The provider is reported healthy only when its attribute condition is scoped to that repository; an org-wide condition (`assertion.repository_owner == ...`) is reported as a mismatch.

## `inference openai`

Commands for enrolling repositories with OpenAI Workload Identity Federation (see the [operator guide](../guides/infrastructure/openai-workload-identity.md)). They need neither GCP access nor an OpenAI key.

What reaches the network, and what does not:

| Command | Network |
|---|---|
| `request` | None. The document is computed from the repository names. |
| `import` | None, unless `--variables` is passed: that calls the GitHub API through the forge client to set the three repository variables. |
| `status` | Reads configuration only, except inside a GitHub Actions job with `id-token: write`, where it performs one token exchange with OpenAI to prove the mapping accepts *that* job's repository — reporting the granted scope and expiry, never the token. |

No OpenAI API key is used or created by any of them.

### `inference openai request`

Generates the request document an administrator needs to enable OpenAI WIF for one or more repositories. Every value is computed from the repository names. Nothing is sent anywhere; the command needs no credentials.

```bash
fullsend inference openai request <owner/repo>[,<owner/repo>…] \
  [--audience "<audience>"] \
  [--project "<openai-project>"] \
  [--service-account "<existing-sa-id>"] \
  [--ref "refs/heads/<branch>"] \
  [--format json|md] \
  [--out <file>]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--audience` | `fullsend://<owner>` | OpenAI Workload Identity audience |
| `--project` | *(empty)* | OpenAI project name or ID for the service accounts |
| `--service-account` | *(none)* | Map this existing service account instead of asking for `fullsend-<repo>-ci` to be created in the mapping |
| `--ref` | *(none)* | Optional tightening. Must be a full ref (`refs/…`). When set, emits **two mappings** per repository — one asserting the given ref (e.g. `refs/heads/main`) and one asserting `refs/pull/*` — so both branch and PR-review-triggered runs work. Without `--ref`, mappings assert `iss`, `aud`, and `repository` only (no ref), matching the Vertex path's repository-scoped trust. The two-mapping cost halves the 50-mapping-per-provider budget to 25 repositories |
| `--format` | `json` | Output format: `json` (versioned schema) or `md` (copy-paste ticket) |
| `--out` | *(stdout)* | Write output to a file |

### `inference openai import`

Takes the administrator's reply and writes `inference.openai` into `.fullsend/config.yaml` through the same setters as `fullsend github setup --openai-*`. All three identifiers must be present — a partial trio is refused, and the config is validated before it is written. The write is local: commit `.fullsend/config.yaml` afterwards, since fullsend reads the base branch for pull-request events.

The reply file may be either the bare reply object or the whole document `request --format json` produced with its `reply` section filled in — an administrator can edit and return the same file. When it names service accounts for several repositories, pass `--repo <owner/repo>` to choose one, or `--service-account-id` to give the value outright.

```bash
# From a reply JSON file:
fullsend inference openai import reply.json

# From flags:
fullsend inference openai import \
  --audience "fullsend://<owner>" \
  --identity-provider-id "<idp-id>" \
  --service-account-id "<sa-id>"

# Set repository variables instead of config.yaml:
fullsend inference openai import \
  --variables --repo <owner/repo> \
  --audience "fullsend://<owner>" \
  --identity-provider-id "<idp-id>" \
  --service-account-id "<sa-id>"
```

| Flag | Default | Description |
|------|---------|-------------|
| `--audience` | | OpenAI Workload Identity audience |
| `--identity-provider-id` | | OpenAI identity provider ID |
| `--service-account-id` | | OpenAI service account ID |
| `--fullsend-dir` | `.fullsend` | Path to the .fullsend configuration directory |
| `--variables` | `false` | Set `FULLSEND_OPENAI_*` repository variables instead of config.yaml |
| `--repo` | | Two roles: the target repository for `--variables`, and the repository to select from a reply that names several (`service_account_ids`). Matched case-insensitively |

### `inference openai status`

Prints the resolved OpenAI WIF identifiers and their source (config layer or environment variables), and flags a partial trio. When nothing is configured, reports that a run will refuse the openai provider and names both remedies: the WIF trio, or the `FULLSEND_OPENAI_API_KEY` repository secret (exported as `OPENAI_API_KEY`). A static `OPENAI_API_KEY` already in the environment is reported as the source; in CI the runner warns and WIF remains preferred. When run inside a GitHub Actions job with `id-token: write`, performs one exchange and reports the returned scope and expiry without ever printing the token.

```bash
fullsend inference openai status <owner/repo> \
  [--fullsend-dir ".fullsend"]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--fullsend-dir` | `.fullsend` | Path to the .fullsend configuration directory |

## See also

- [Getting inference for fullsend](../guides/getting-started/getting-inference.md) — getting started guide
- [OpenAI Workload Identity](../guides/infrastructure/openai-workload-identity.md) — end-to-end OpenAI WIF setup guide
- [Advanced setup](../guides/infrastructure/advanced-setup.md) — non-standard installation paths and WIF configuration
- [CLI internals](../guides/dev/cli-internals.md) — command tree and implementation details
