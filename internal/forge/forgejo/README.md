# Forgejo / Gitea `forge.Client`

MVP implementation of `forge.Client` for Forgejo (and Gitea-compatible) API v1.

ForgeOps backlog: **FACTORY-FULLSEND-FORGEJO-CLIENT-001** (ADR-0020 deferred item).

## Usage

```go
c := forgejo.New(os.Getenv("FORGEJO_TOKEN"), forgejo.WithBaseURL("https://git.forge.internal"))
```

CLI wiring (`internal/cli/forge_client.go`) resolves:

| Env | Role |
|---|---|
| `FORGEJO_TOKEN` / `GITEA_TOKEN` | Access token |
| `FORGEJO_API_URL` / `GITEA_API_URL` | Optional API base (defaults to `https://git.forge.internal`) |

`forge.DetectForge` returns `"forgejo"` for `git.forge.internal`, `codeberg.org`, `gitea.com`, and hosts containing `forgejo`/`gitea`.

## Coverage

- Core REST methods (repos, issues, PRs, labels, branches, contents) are live.
- High-value wave (CHG stub-fill): forks, multi-file commits, cross-repo PRs, issue comment CRUD,
  PR reviews/commits/file diffs, branch protection reads, Actions secrets create/exists/delete,
  `IsInstallationToken` → false.
- Remaining methods (mostly GitLab CI / GitHub Actions workflows / reactions / review threads)
  return `forge.ErrNotSupported` via `stubs.go`.
- Live smoke: `go test ./internal/forge/forgejo/ -tags live_forgejo` with `FORGEJO_TOKEN` set.

## Non-goals

- Full repos.yaml install / converge parity for Forgejo
- GitLab pipeline APIs (stay stubs)
- Full GitHub Actions workflow parity
