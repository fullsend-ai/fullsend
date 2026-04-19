# SPEC: Git-tracked files in `<org>/.fullsend` (admin install v1)

## 1. Scope

This specification defines the **v1 set of paths committed in git** inside the
organization configuration repository named `.fullsend`, as created and
updated by the `fullsend admin install` flow (`ConfigRepoLayer` and
`WorkflowsLayer`). It is limited to **files**; repository secrets, variables,
and files written to other repositories are out of scope (see section 5).

The repository name and role of `.fullsend` follow
[ADR 0003](../../../../ADRs/0003-org-config-repo-convention.md).

## 2. Normative tracked paths (v1)

The following paths SHALL exist on the default branch after a complete admin
install of these layers:

| Path | Layer responsible |
|------|-------------------|
| `config.yaml` | `ConfigRepoLayer` |
| `.github/workflows/triage.yml` | `WorkflowsLayer` |
| `.github/workflows/code.yml` | `WorkflowsLayer` |
| `.github/workflows/review.yml` | `WorkflowsLayer` |
| `.github/workflows/repo-maintenance.yml` | `WorkflowsLayer` |
| `.github/actions/fullsend/action.yml` | `WorkflowsLayer` |
| `.github/scripts/setup-agent-env.sh` | `WorkflowsLayer` |
| `agents/triage.md` | `WorkflowsLayer` |
| `env/gcp-vertex.env` | `WorkflowsLayer` |
| `env/triage.env` | `WorkflowsLayer` |
| `harness/triage.yaml` | `WorkflowsLayer` |
| `policies/triage.yaml` | `WorkflowsLayer` |
| `scripts/validate-triage.sh` | `WorkflowsLayer` |
| `scripts/reconcile-repos.sh` | `WorkflowsLayer` |
| `templates/shim-workflow.yaml` | `WorkflowsLayer` |
| `CODEOWNERS` | `WorkflowsLayer` |

Write order: all scaffold files from `internal/scaffold/fullsend-repo/` in
filesystem walk order, then `CODEOWNERS` last (CODEOWNERS failure is non-fatal).

## 3. Per-path requirements

### 3.1 `config.yaml`

- **Location:** repository root, filename exactly `config.yaml`.
- **Document body (fields, schema, validation):** specified in **ADR 0011**
  (this SPEC only requires that the file exists as the tracked carrier for
  org configuration).

### 3.2 `.github/workflows/triage.yml`

- Triage workflow triggered via `workflow_dispatch` with `event_type`,
  `source_repo`, `event_payload` inputs.

### 3.3 `.github/workflows/code.yml`

- Code workflow, same dispatch pattern as triage.

### 3.4 `.github/workflows/review.yml`

- Review workflow, same dispatch pattern as triage.

### 3.5 `.github/workflows/repo-maintenance.yml`

- Reconciles enrollment shims in target repos when `config.yaml` changes.

### 3.6 `.github/actions/fullsend/action.yml`

- Composite action: installs the fullsend CLI and runs the agent.

### 3.7 `.github/scripts/setup-agent-env.sh`

- Env prefix stripping helper sourced by per-role workflows.

### 3.8 `agents/triage.md`

- Agent instructions for the triage role.

### 3.9 `env/gcp-vertex.env`

- Environment variables for GCP Vertex AI inference.

### 3.10 `env/triage.env`

- Environment variables specific to the triage role.

### 3.11 `harness/triage.yaml`

- Harness configuration for the triage role.

### 3.12 `policies/triage.yaml`

- Policy definitions for the triage role.

### 3.13 `scripts/validate-triage.sh`

- Validation script for triage role output.

### 3.14 `scripts/reconcile-repos.sh`

- Shell script that reconciles enrollment shims in target repos. Called by
  `repo-maintenance.yml`. Uses `gh` CLI and `yq` to read `config.yaml`.
  For enabled repos: creates branches, writes shim workflows, and opens
  enrollment PRs. For disabled repos: creates branches, deletes shim
  workflows (via GitHub Contents API DELETE with blob SHA), and opens
  removal PRs. Closes stale cross-direction PRs in both cases.

### 3.15 `templates/shim-workflow.yaml`

- The shim workflow template used by `scripts/reconcile-repos.sh` to write shim
  workflows to target repos during enrollment and verify content during updates.

### 3.16 `CODEOWNERS`

- **Purpose:** grant the installing human ownership of all paths in the
  configuration repo.
- **Normative pattern (v1):** exactly two lines — a comment line, then a
  wildcard rule for the authenticated GitHub user who ran install:

```text
# fullsend configuration is governed by org admins.
* @<AUTHENTICATED_GITHUB_USER_LOGIN>
```

Replace `<AUTHENTICATED_GITHUB_USER_LOGIN>` with the installing user’s GitHub
login (no `@` prefix inside the angle-bracket placeholder; the second line
includes `@` before the login as shown).

## 4. Relationship to implementation

The reference implementation lives in `internal/layers/configrepo.go` and
`internal/layers/workflows.go`. The `WorkflowsLayer` deploys content from the
embedded scaffold at `internal/scaffold/fullsend-repo/`. If implementation and
this SPEC diverge, this SPEC is normative for **v1** tracked layout and file
bodies (except `config.yaml` body per ADR 0011).

## 5. Out of scope (not Git-tracked in `.fullsend`)

The following are managed via the forge API, not as committed files in
`.fullsend`:

- **Repository secrets** — one per configured agent role, name pattern
  `FULLSEND_<ROLE>_APP_PRIVATE_KEY` (PEM material), where `<ROLE>` is the
  uppercased role string (`SecretsLayer`).
- **Repository variables** — one per role, name pattern
  `FULLSEND_<ROLE>_APP_ID` (string app id).

**Per-repository enrollment** (for example `.github/workflows/fullsend.yaml` in
application repos) is performed by `EnrollmentLayer` and is intentionally not
part of the `.fullsend` repository file set.

The `fullsend admin` CLI (`internal/cli/admin.go`) orchestrates these layers; it
does not introduce additional committed paths under `.fullsend` beyond those
listed in section 2.
