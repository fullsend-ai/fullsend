# Forgejo / Gitea `forge.Client`

Implementation of `forge.Client` for Forgejo (and Gitea-compatible) API v1.

ForgeOps backlog: **FACTORY-FULLSEND-FORGEJO-CLIENT-001** (ADR-0020 deferred item).

## Usage

```go
c := forgejo.New(os.Getenv("FORGEJO_TOKEN"), forgejo.WithBaseURL("https://codeberg.org"))
```

CLI wiring (`internal/cli/forge_client.go`) resolves:

| Env | Role |
|---|---|
| `FORGEJO_TOKEN` / `GITEA_TOKEN` | Access token |
| `FORGEJO_API_URL` / `GITEA_API_URL` | Required API base (or manifest URL) |

`forge.DetectForge` returns `"forgejo"` for `codeberg.org`, `gitea.com`, and hosts containing `forgejo`/`gitea`. Self-hosted hosts without those markers need `--forge forgejo`.

## Coverage

- Core REST methods (repos, issues, PRs, labels, branches, contents) are live.
- High-value: forks, multi-file commits (sequential/non-atomic), cross-repo PRs, issue comment CRUD,
  PR reviews/commits/file diffs, branch protection reads, Actions secrets create/exists/delete,
  `IsInstallationToken` → false, `GetTokenScopes` → nil (no OAuth scope enum).
- Actions workflows/runs/jobs/artifacts/variables, issue/comment reactions, PR authorization context,
  and best-effort review threads (per review comment) are live via `actions_reactions.go`.
- Remaining methods (mostly GitLab CI / org-level Actions / annotations) return `forge.ErrNotSupported` via `stubs.go`.
- Live smoke: `go test ./internal/forge/forgejo/ -tags live_forgejo` with `FORGEJO_TOKEN` + `FORGEJO_API_URL` set.

## Non-goals

- Full repos.yaml install / converge parity for Forgejo
- GitLab pipeline APIs (stay stubs)
- GitHub GraphQL review-thread parity (Forgejo maps comments → threads)
