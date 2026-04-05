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
| `.github/workflows/agent.yaml` | `WorkflowsLayer` |
| `.github/workflows/repo-onboard.yaml` | `WorkflowsLayer` |
| `CODEOWNERS` | `WorkflowsLayer` |

Write order for workflow-related paths: `agent.yaml`, then `repo-onboard.yaml`,
then `CODEOWNERS` (CODEOWNERS failure is non-fatal in the implementation).

## 3. Per-path requirements

### 3.1 `config.yaml`

- **Location:** repository root, filename exactly `config.yaml`.
- **Document body (fields, schema, validation):** specified in **ADR 0011**
  (this SPEC only requires that the file exists as the tracked carrier for
  org configuration).

### 3.2 `.github/workflows/agent.yaml`

- **Contents:** exactly the document in
  [`files/agent-dispatch-v1.yaml`](files/agent-dispatch-v1.yaml) (byte-for-byte
  normative snapshot for v1).

### 3.3 `.github/workflows/repo-onboard.yaml`

- **Contents:** exactly the document in
  [`files/repo-onboard-v1.yaml`](files/repo-onboard-v1.yaml) (byte-for-byte
  normative snapshot for v1).

### 3.4 `CODEOWNERS`

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
`internal/layers/workflows.go`. If implementation and this SPEC diverge, this
SPEC is normative for **v1** tracked layout and file bodies (except
`config.yaml` body per ADR 0011).

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
