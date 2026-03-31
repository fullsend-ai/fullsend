---
title: "8. Reusable workflows for credential isolation"
status: Undecided
relates_to:
  - agent-infrastructure
  - security-threat-model
  - agent-architecture
topics:
  - security
  - credentials
  - infrastructure
---

# 8. Reusable workflows for credential isolation

Date: 2026-03-27

## Status

Undecided — the core premise (that GitHub reusable workflows prevent the
calling repo from accessing the called workflow's secrets) needs experimental
validation before this can be accepted.

## Context

[ADR 0007](0007-github-actions-initial-execution-platform.md) establishes
GitHub Actions as the initial execution platform. Enrolled repos get a thin
`.github/workflows/` stub that triggers on forge events. The question is how
to isolate the right to act as the agent — keeping it in a namespace where
the owners of individual enrolled repos cannot execute arbitrary code or
arbitrary workflows.

This decision is about **execution isolation**, not credential format. Whether
the agent authenticates via an OIDC JWT, a long-lived secret, or a short-lived
token, the core requirement is the same: owners of enrolled repos must not be
able to access agent credentials directly. The choice between OIDC federation
and stored secrets is a complementary decision that can be layered on top of
this one. (And if an attacker must compromise the agent runtime itself to
exfiltrate credentials, that's a sandboxing concern — taken up separately.)

The threat: a contributor to an enrolled repo submits a PR that modifies the
workflow file to exfiltrate credentials or run arbitrary code in the agent's
execution context. Even if the workflow file is CODEOWNERS-protected (as
ADR 0007 requires), the question is whether the architecture can provide
defense in depth — making the agent's execution namespace structurally
inaccessible to enrolled repos regardless of workflow file contents.

GitHub's [reusable workflows](https://docs.github.com/en/actions/sharing-automations/reusing-workflows)
allow one workflow to call another via `workflow_call`. The called workflow
runs in the context of the calling repo but is defined in a different repo.
The premise of this ADR is that reusable workflows provide this execution
isolation — code and secrets defined in the `.fullsend` repo's workflow are
not accessible to the calling repo's workflow, even if an attacker modifies
the calling workflow.

**This premise needs experimental proof.** Specifically:

1. Can a calling workflow access secrets that are only available to the
   reusable workflow's repo? (Expected: no.)
2. Can a calling workflow inject steps that run before or after the reusable
   workflow and access its environment? (Expected: unclear.)
3. Can a calling workflow pass inputs that cause the reusable workflow to leak
   secrets via outputs or logs? (Expected: possible — the reusable workflow
   must be hardened against this.)
4. If the calling workflow is modified in a PR (not yet merged), does GitHub
   Actions run the PR's version of the workflow or the base branch version?
   (Expected: PR version for `pull_request` triggers, base version for
   `push` triggers — but this matters for whether a malicious PR can
   substitute a different reusable workflow reference.)

## Options

### Option 1: Reusable workflow in `.fullsend` repo

The enrolled repo's workflow is a stub:

```yaml
# .github/workflows/fullsend.yml in the enrolled repo
name: fullsend
on:
  issues:
    types: [labeled]
jobs:
  dispatch:
    uses: <org>/.fullsend/.github/workflows/agent-dispatch.yml@main
```

The real workflow lives in `<org>/.fullsend/.github/workflows/agent-dispatch.yml`
and has access to secrets defined in the `.fullsend` repo.

**Pros:**
- Secrets never exist in the enrolled repo's settings.
- Centralized — updating the reusable workflow updates all enrolled repos.
- The enrolled repo's stub is trivially auditable.

**Cons:**
- The isolation properties of `workflow_call` need experimental verification.
- Reusable workflows have constraints: they cannot use `strategy`, and the
  calling workflow cannot add steps to the called workflow's jobs.
- Debugging is harder — the workflow definition is in a different repo from
  the workflow run.

### Option 2: Workflow in enrolled repo with org-level secrets

The full workflow lives in each enrolled repo. Secrets are configured as
GitHub org-level secrets, scoped to repos that need them.

**Pros:**
- Simpler — one repo, one workflow, one set of logs.
- Org-level secrets are a well-understood GitHub feature.

**Cons:**
- Org-level secrets are available to any workflow run in the scoped repos. A
  modified workflow file can access them.
- No structural isolation — defense depends entirely on CODEOWNERS preventing
  workflow file changes, with no fallback.
- Each enrolled repo has its own copy of the workflow to maintain.

## Credential provisioning (orthogonal)

How credentials are provisioned — OIDC federation, stored GitHub secrets,
external secret managers — is a separate decision that layers on top of this
one. OIDC federation determines *how* a credential is issued; the options
above determine *where the code that receives it runs*. In practice, OIDC
federation would be used *inside* whichever execution model is chosen, not
instead of it.

Notably, GitHub's OIDC token includes a `job_workflow_ref` claim that
identifies the reusable workflow. An OIDC trust policy can restrict federation
to only the `.fullsend` repo's workflow, reinforcing Option 1's isolation.
This means the two approaches compose well — execution isolation prevents
enrolled repos from running arbitrary code in the agent namespace, and OIDC
federation ensures that even within that namespace, credentials are
short-lived and auditable.

OIDC federation does not solve execution isolation on its own: an enrolled
repo that can run arbitrary code in the workflow can also request the OIDC
token.

## Decision

_Undecided pending experimental validation._

The reusable workflow approach (Option 1) is the leading candidate. If the
experiment confirms that secrets in the `.fullsend` repo are structurally
inaccessible to the calling repo's workflow, this provides meaningful defense
in depth beyond CODEOWNERS protection of the workflow file.

### Experiment needed

Create a test GitHub App with minimal permissions. Set up:

1. A `.fullsend`-equivalent repo with a reusable workflow that accesses a
   secret (the App's private key) and prints a confirmation (not the secret
   itself).
2. An enrolled-equivalent repo with a stub workflow that calls the reusable
   workflow.
3. Attempt to access the secret from the calling repo's workflow — via
   additional jobs, modified inputs, environment inspection, and log
   examination.
4. Test with both `push` and `pull_request` triggers to determine whether
   PR-submitted workflow changes affect the reusable workflow reference.

The experiment should be logged in `experiments/` following the project's
existing convention (see `experiments/67-claude-github-app-auth/`).

## Consequences

_Consequences depend on the experiment's outcome._

If the reusable workflow approach is validated:

- The `.fullsend` repo becomes the only place where the GitHub App private key
  is stored, reducing the attack surface to a single, tightly-governed repo.
- Enrolled repos never need secret configuration — they just reference the
  reusable workflow.
- The enrolled repo's workflow stub is simple enough to be templated and
  version-checked by a drift scanner.
- Contributors to enrolled repos cannot exfiltrate credentials by modifying
  workflow files, even if CODEOWNERS review is somehow bypassed — structural
  isolation provides defense in depth.

If the experiment reveals that reusable workflows do not provide sufficient
isolation, Option 2 should be reconsidered, and CODEOWNERS protection of the
workflow file (per ADR 0007) becomes the primary defense rather than a
secondary one.
