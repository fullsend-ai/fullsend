---
title: "31. Reusable workflows for action-installed distribution"
status: Proposed
relates_to:
  - agent-infrastructure
topics:
  - workflows
  - distribution
  - reusable-workflows
  - composite-action
---

# 31. Reusable workflows for action-installed distribution

Date: 2026-05-06

## Status

Proposed

## Context

`fullsend admin install` copies ~80 files from the Go binary's embedded scaffold
(`internal/scaffold/fullsend-repo/`) into each org's `.fullsend` repo. This
includes agent workflows (78–305 lines each), four composite actions (`fullsend`,
`mint-token`, `validate-enrollment`, `setup-gcp`), setup scripts, and a
dispatcher. When a bug is fixed or a security patch lands in the scaffold, every
org must re-run `fullsend admin install` to pick up the change. Workflow drift
across orgs is the norm.

The dispatch chain established in
[ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md) —
shim → `dispatch.yml` → agent workflows — is preserved. Only the agent
workflows themselves change from full copies to thin callers.

## Options

### Option A: Scaffold copies (status quo)

`fullsend admin install` writes full agent workflows into `.fullsend`. Each org
gets its own copy. Updates require re-running install in every org.

### Option B: Published composite actions only

Publish the four composite actions at `fullsend-ai/fullsend@v0`. Agent workflows
in `.fullsend` replace `uses: ./.github/actions/*` with published references.
Infrastructure logic (checkout, token minting, GCP auth, sandbox setup) stays
duplicated in each org's workflow files.

### Option C: Reusable workflows + published composite actions

Publish reusable workflows (`workflow_call`) and composite actions from
`fullsend-ai/fullsend`. Agent workflows in `.fullsend` shrink to ~40–70 line thin
callers that delegate infrastructure logic upstream via `workflow_call` with
explicit secret passthrough. Org-specific content (agents, harness, env, policies,
scripts) stays local.

## Decision

Use Option C. Publish reusable workflows
(`fullsend-ai/fullsend/.github/workflows/reusable-{agent}.yml`) and composite
actions (`fullsend-ai/fullsend@v0`, plus `mint-token`, `validate-enrollment`,
`setup-gcp` under `.github/actions/`).

Thin callers in `.fullsend` use `workflow_call` to invoke upstream reusable
workflows. Since `workflow_call` runs the callee in the caller's repo context,
the reusable workflow has access to `.fullsend` secrets and checks out the
config repo directly. Secrets pass via explicit `secrets:` declarations — each
thin caller lists only the secrets the reusable workflow needs (GCP credentials),
following least-privilege over blanket `secrets: inherit`. Org-specific `vars.*`
values (mint URL, GCP region, auth mode) are automatically available in the
reusable workflow from the caller's context, but thin callers pass them as
explicit `inputs.*` to make the interface contract self-documenting, ensure
missing variables surface visibly with org-appropriate defaults, and provide an
auditable manifest of configuration flowing across the trust boundary. Each
reusable workflow also declares agent-specific inputs (e.g., `trigger_source`,
`pr_number`, `instruction` for the fix workflow) beyond the common set.

Composite actions referenced via fully-qualified paths
(`fullsend-ai/fullsend/.github/actions/*@v0`) in reusable workflows resolve to
the upstream repo, not the caller's repo. This is why the four composite actions
must live upstream. `run:` steps execute in the caller's workspace, so scripts
in `.fullsend/scripts/` remain accessible.

`dispatch.yml` is enhanced with centralized routing logic
([ADR 0034](0034-centralized-shim-routing-via-dispatch.md)). Thin callers
retain `# fullsend-stage:` markers so dispatch can route events to the correct
agent workflow.

The dispatch chain uses 1 level of `workflow_call` nesting (limit is 10 as of
November 2025; was 4 previously):

```
shim ──workflow_call──> .fullsend/dispatch.yml
        ──workflow_dispatch──> .fullsend/code.yml (thin caller)
            ──workflow_call──> reusable-code.yml (level 1)
                ──uses──> fullsend-ai/fullsend@v0 (composite action)
```

The `workflow_dispatch` from dispatch to thin caller is an API call (`gh workflow
run`), which starts a new workflow run and resets the `workflow_call` nesting
counter. The reusable workflow sees only 1 level of nesting from the thin
caller's perspective.

## Consequences

- Infrastructure patches (token minting, GCP auth, sandbox setup) ship once
  upstream and propagate to all orgs on next workflow run — no re-install
  required.
- `fullsend-ai/fullsend` must remain public for `workflow_call` and `uses:`
  references to resolve (it already is).
- Thin callers map `vars.*` to explicit `inputs.*` even though `vars` are
  automatically available from the caller's context. This makes the reusable
  workflow's parameter contract visible in the caller's YAML, surfaces
  missing variables with explicit defaults instead of silent empty strings,
  and creates a natural review checkpoint when the upstream interface changes.
- Thin callers pin upstream by tag (`@v0`) or SHA — orgs control when they
  adopt upstream changes. SHA pinning recommended for production since tags
  are mutable.
- Stage-based dispatch ([ADR 0026](0026-stage-based-dispatch-for-agent-workflow-decoupling.md)),
  shim workflows, and org-specific content (agents, harness, policies, scripts)
  are unchanged.
- **Trust boundary shift:** Thin callers pass GCP credential secrets to
  reusable workflow code hosted in `fullsend-ai/fullsend` (a public repo)
  via explicit `secrets:` declarations (not `secrets: inherit`, limiting
  exposure to only the secrets the reusable workflow declares). Under the
  scaffold-copy model, secrets are consumed by code in the org's own repo.
  Under the reusable-workflow model, secrets flow to upstream code. SHA
  pinning (not just tag pinning) gives orgs full control over which upstream
  code runs with their secrets. This aligns with the project's threat model
  priority on external injection — a compromised upstream ref could affect
  all downstream orgs simultaneously, making SHA pinning the recommended
  default for production installations.
- **Upstream availability:** `fullsend-ai/fullsend` becomes a runtime
  dependency for all downstream orgs. If the repo is unavailable or a pinned
  ref is deleted, downstream workflow runs fail. Scaffold copies are immune
  to upstream outages after install.
- **Cross-repo debugging:** When reusable workflows fail, the call stack spans
  two repositories (the org's `.fullsend` and `fullsend-ai/fullsend`).
  Developers must inspect both the thin caller and the upstream workflow to
  diagnose failures. GitHub's workflow run UI shows reusable workflow steps
  inline, which partially mitigates this.
- **GitHub-specific mechanism:** `workflow_call` and `secrets:` passthrough are
  GitHub Actions primitives with no direct equivalent in other CI systems.
  Multi-forge support ([ADR 0028](0028-gitlab-support.md)) will need its own
  distribution mechanism (e.g., GitLab CI/CD Components or `include:`)
  independent of this ADR.
- **Scaffold output changes:** `fullsend admin install` will emit thin callers
  (~40–70 lines each) instead of full agent workflows (78–305 lines each). This
  is a user-visible change — orgs running `admin install` after this change
  ships will see substantially different workflow files in `.fullsend`.
- **Token generation uses OIDC:** Reusable workflows use the `mint-token`
  composite action for OIDC-based token minting
  ([ADR 0029](https://github.com/fullsend-ai/fullsend/pull/655)). Each
  reusable workflow requests a scoped token for its role (triage, coder,
  review, fullsend) — no PEMs or App secrets in the calling repo. The fix
  workflow reuses the coder role.
