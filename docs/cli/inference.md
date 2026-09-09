---
sidebar_label: fullsend inference
---

# fullsend inference

Manage the inference credentials agent runs use. `provision`, `deprovision` and `status` create, inspect, and remove the GCP Workload Identity Federation (WIF) pool, OIDC provider, and IAM bindings that let GitHub Actions workflows authenticate with GCP for Agent Platform (Vertex) access. [`openai`](#inference-openai) enrols repositories with OpenAI WIF for GPT models on the pi runtime.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend inference provision <org\|owner/repo>` | Create WIF pool/provider and grant Agent Platform access |
| `fullsend inference deprovision <org\|owner/repo>` | Remove org or repo from WIF |
| `fullsend inference status <org\|owner/repo>` | Check WIF health and print config values |
| `fullsend inference openai request <owner/repo>[,…]` | Generate WIF provider/mapping request for OpenAI admin |
| `fullsend inference openai import [reply.json]` | Import OpenAI WIF identifiers into config |
| `fullsend inference openai status <owner/repo>` | Check OpenAI WIF configuration and exchange status |

## `inference provision`

Creates a WIF pool (`fullsend-inference`), an OIDC provider (`github-oidc`), and grants `roles/aiplatform.user` to the WIF principal. Idempotent and safe to re-run.

```bash
fullsend inference provision <org> \
  --project "<GCP_PROJECT>"
```

Per-repo mode scopes the WIF provider to a single repository:

```bash
fullsend inference provision <owner/repo> \
  --project "<GCP_PROJECT>"
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--project` | | GCP project ID |
| `--region` | `global` | GCP region |

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

Removes an org or repo from WIF by deleting the IAM binding and (optionally) the WIF provider.

```bash
fullsend inference deprovision <org|owner/repo> \
  --project "<GCP_PROJECT>"
```

### Required IAM roles

| Role | Description |
|------|-------------|
| `roles/iam.workloadIdentityPoolAdmin` | Modify WIF pool and provider |

## `inference status`

Checks WIF health and prints the configuration values needed for `github setup`.

```bash
fullsend inference status <org|owner/repo> \
  --project "<GCP_PROJECT>"
```

Read-only — makes no changes.

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

Prints the resolved OpenAI WIF identifiers and their source (config layer or environment variables), and flags a partial trio. When run inside a GitHub Actions job with `id-token: write`, performs one exchange and reports the returned scope and expiry without ever printing the token.

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
