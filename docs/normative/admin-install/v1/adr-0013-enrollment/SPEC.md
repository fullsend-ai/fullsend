# Enrollment v1 (admin install) — normative specification

**Version:** v1
**ADR:** [0013](../../../../ADRs/0013-admin-install-repo-enrollment-v1.md)
**Scope:** How the admin install enrolls a GitHub-hosted repository into the fullsend agent pipeline by opening a pull request that adds a *shim* workflow. This spec is the contract for tooling (for example the enrollment layer and `forge.Client`); hosting details follow GitHub’s REST API unless stated otherwise.

## 1. Definitions

- **Owner** — GitHub organization or user that owns the repository (the install target). In admin-install configuration this is the organization login passed to tooling as the owner namespace.
- **Target repository** — A repository listed as enabled for the pipeline in org configuration (see ADR 0011).
- **Shim workflow** — The workflow file at **shim path** whose only job is to delegate to the reusable workflow published from the org’s `.fullsend` config repository (see ADR 0012).
- **Enrollment branch** — The head branch for the enrollment change proposal.
- **Base branch** — The branch the enrollment pull request targets.

## 2. `{org}` placeholder

The shim workflow template contains the literal substring `{org}` in exactly one normative place: the `uses:` value that names the reusable workflow.

**Rules:**

1. `{org}` MUST be replaced with the **Owner** string (the same identifier used in forge calls as `owner` / org login for the target repository). No other substitution or templating is defined in v1.
2. After substitution, the `uses:` line MUST be exactly:

   `{Owner}/.fullsend/.github/workflows/agent.yaml@main`

   where `{Owner}` is the substituted value (for example `acme-corp/.fullsend/.github/workflows/agent.yaml@main`).
3. Implementations MUST NOT leave `{org}` in the committed shim file.

## 3. Constants (v1)

| Item | Value |
|------|--------|
| Shim path | `.github/workflows/fullsend.yaml` |
| Enrollment branch name | `fullsend/onboard` |
| Reusable workflow ref | `@main` (pinned to the default branch of `.fullsend`) |
| Default base branch if unspecified | `main` |
| Commit message for adding the shim | `chore: add fullsend shim workflow` |

## 4. Enrollment detection

A target repository is **enrolled** for v1 if and only if the file at **shim path** is readable on the repository’s **default branch** (forge `GetFileContent(owner, repo, shim path)` semantics: no other ref).

- If the file exists (any content): the repository MUST be treated as already enrolled; installers MUST NOT create another enrollment change proposal for v1.
- If the forge reports “not found” (404 / `ErrNotFound`): the repository MUST be treated as not enrolled.
- Any other error while checking MUST fail that repository’s enrollment attempt and MUST be surfaced to the operator; it MUST NOT be interpreted as enrolled.

## 5. Shim workflow content (normative)

The file at **shim path** MUST be valid GitHub Actions workflow YAML and MUST match the following structure and keys. Comments are non-normative except where they restate these rules.

```yaml
# fullsend shim workflow
# Routes events to the reusable agent dispatch workflow in .fullsend.
name: fullsend

on:
  issues:
    types: [opened, edited, labeled]
  issue_comment:
    types: [created]
  pull_request:
    types: [opened, synchronize, ready_for_review]
  pull_request_review:
    types: [submitted]

jobs:
  dispatch:
    uses: {org}/.fullsend/.github/workflows/agent.yaml@main
    with:
      event_type: ${{ github.event_name }}
      event_payload: ${{ toJSON(github.event) }}
    secrets:
      APP_PRIVATE_KEY: ${{ secrets.FULLSEND_FULLSEND_APP_PRIVATE_KEY }}
```

Before commit, `{org}` MUST be replaced per §2.

## 6. Forge operation sequence (install)

For each enabled target repository that is not enrolled (§4), the installer MUST perform these operations in order:

1. **CreateBranch** — Create **enrollment branch** from the target repository’s **default branch** tip (same SHA the hosting API uses for “branch from default”).
2. **CreateFileOnBranch** — Create **shim path** on **enrollment branch** with content from §5 (after `{org}` substitution), using the commit message from §3.
3. **CreateChangeProposal** — Open a pull request with:
   - **head:** enrollment branch name (`fullsend/onboard`)
   - **base:** the configured default branch name for that repository, or `main` if none is configured (§3)
   - **title (exact):** `Connect to fullsend agent pipeline`
   - **body (exact text, Markdown):**

     ```markdown
     This PR adds a shim workflow that routes repository events to the fullsend agent dispatch workflow in the `.fullsend` config repo.

     Once merged, issues, PRs, and comments in this repo will be handled by the fullsend agent pipeline.
     ```

## 7. Batch and failure behavior

- Install MUST iterate all enabled target repositories from org configuration.
- Cancellation (`context` cancelled) MUST abort the install and return an error.
- Failure on one repository (after detection) MUST be reported (for example warning + message) and MUST NOT prevent attempting enrollment for remaining repositories.
- Uninstall: v1 does **not** require removing the shim from target repositories (no normative uninstall automation).

## 8. Analyze (read model)

For reporting, a repository is **enrolled** / **not enrolled** / **unknown** using the same **shim path** and default-branch read as §4. Partial results across enabled repos MUST be representable as degraded state when some are enrolled and some are not.
