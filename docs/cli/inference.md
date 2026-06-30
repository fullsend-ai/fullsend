---
sidebar_label: fullsend inference
---

# fullsend inference

Manage GCP Workload Identity Federation (WIF) infrastructure for Agent Platform access. These commands create, inspect, and remove the WIF pool, OIDC provider, and IAM bindings that allow GitHub Actions workflows to authenticate with GCP.

## Commands

| Command | Description |
|---------|-------------|
| `fullsend inference provision <org\|owner/repo>` | Create WIF pool/provider and grant Agent Platform access |
| `fullsend inference deprovision <org\|owner/repo>` | Remove org or repo from WIF |
| `fullsend inference status <org\|owner/repo>` | Check WIF health and print config values |

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

## See also

- [Getting inference for fullsend](../guides/getting-started/getting-inference.md) — getting started guide
- [Advanced setup](../guides/infrastructure/advanced-setup.md) — non-standard installation paths and WIF configuration
- [CLI internals](../guides/dev/cli-internals.md) — command tree and implementation details
