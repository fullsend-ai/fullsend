---
title: "115. Follow-up automation after an agent run is a user-owned workflow"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
  - agent-infrastructure
topics:
  - agents
  - dispatch
  - security
  - workflows
---

# 115. Follow-up automation after an agent run is a user-owned workflow

Date: 2026-09-17

## Status

Accepted

## Context

An agent run ends with a validated result: the harness `validation_loop`
schema gates the sandbox output, and the post-script acts on it from the
trusted runner with the token minted for the harness `role`. Users now want
actions after the run that no agent role covers, starting with re-running
CI jobs an agent classified as flaky, which needs `actions: write`
(fullsend-ai/fullsend#7003). The first proposal was a new GitHub App role that
combines `actions: write` with `pull_requests: write` in one token.

A role is a trust boundary, not a configuration detail. The mint binds a role
to `ALLOWED_ROLES` and an App installation, not to an agent, so any harness may
claim any served role and a wider ceiling reaches every agent in every enrolled
repository. The threat model asks for least privilege and separation of duties
([security-threat-model.md](../problems/security-threat-model.md#threat-2-insider-threat--compromised-credentials)),
and control-plane scopes such as `actions: write` unlock approving fork runs,
dispatching workflows on any ref, and deleting runs and logs. Per-stage
privilege levels ([ADR 0073](0073-named-mint-privilege-levels.md), implemented
in fullsend-ai/fullsend#7394) let a harness hand the sandbox a narrower token
than its scripts, but every level is still a slice of one role's App
permissions; they cannot grant a scope the role does not carry, and any
harness naming the role may request any of its levels. GitHub Agentic
Workflows reaches the same conclusion: its agent job runs with read-only
repository permissions, every write is an opt-in typed "safe output" executed
in a separate job whose permissions are the union of the enabled output types,
and its draft `approve-workflow-run` design calls `actions: write` a broad
scope to be granted only where that output is explicitly enabled
([safe outputs reference](https://github.github.com/gh-aw/reference/safe-outputs/)).

Every run already publishes what a follow-up needs. The runner action uploads
the run directory as the `fullsend-<agent>` artifact on every run, including
the validated result under `iteration-N/output/`, and the shim workflow
completing is a `workflow_run` event in the same repository. Per-repo
installation ([ADR 0033](0033-per-repo-installation-mode.md)) runs the shim in
the user's repository, so both signals land where the user's own workflows
live. Per-org mode is deprecated ([ADR 0044](0044-deprecate-per-org-installation-mode.md))
and its job token cannot reach enrolled repositories.

## Options

### A new App role per capability

Each control-plane need becomes a mint role and a GitHub App. Rejected: the
ceiling applies to every agent that names the role, adopters need an org admin
to install one more App, and the operations it grants (approve, dispatch,
delete) sit in the same token as pull request write.

### A follow-up job inside the reusable dispatch workflow

Fullsend runs the re-run itself in `reusable-dispatch.yml` after the agent job.
Rejected: agent-specific logic lands in the shared workflow, fullsend becomes
the executor of every user's control-plane action, and each new follow-up
needs a fullsend release.

### Per-stage privilege levels on a custom multi-level role

The mint operator adds a level carrying `actions: write` to the agent's custom
role and the harness maps `post_script` to it
([ADR 0073](0073-named-mint-privilege-levels.md), fullsend-ai/fullsend#7394).
Rejected on three facts and their cost to the user. Extra levels exist only on
operator-defined custom roles, so the harness author depends on the mint
operator for every new scope and learns of a mismatch as a 403 that aborts the
run after the agent has spent its budget. One stage receives one token, and
the post-script both comments and re-runs, so its level must hold
`pull_requests: write` and `actions: write` together; the field separates
sandbox from scripts, not one write from another. The App installation still
carries `actions: write`, so every adopting repository needs that App and any
harness naming the role may request the level; custom levels are not
enforced to nest (fullsend-ai/fullsend#7446), so a stage may receive a
different token rather than a narrower one. The author would reason about
three tokens across three stages plus the App ceiling, against one read-only
artifact and one job token in the alternative.

### Hand the workflow token to the post-script

The post-script receives the job token next to the minted token. Rejected: one
process then holds two identities, and it contradicts the boundary being
decided for the workflow token in fullsend-ai/fullsend#7286, which keeps that
token out of every pre- and post-script child.

### A user-owned follow-up workflow

The user's own workflow subscribes to the run and acts with the permissions it
declares for itself. Chosen: it is the only option where the user sees one
identity per place (the App in the post-script, `github-actions[bot]` in the
follow-up), needs no coordination with the mint operator or an org admin, and
can read every permission involved in the two files they own.

## Decision

Follow-up automation after an agent run is a workflow the user owns in the
same repository. It triggers on `workflow_run` for the shim workflow, reads the
run's `fullsend-<agent>` artifact, and runs with the job token and the
permissions the user grants it. Fullsend does not add follow-up jobs to
dispatch, does not mint tokens for follow-ups, and does not create roles for
control-plane operations such as re-running, dispatching, approving, or
cancelling workflow runs.

The `fullsend-<agent>` artifact becomes a documented compatibility surface:
`<run-dir>/iteration-N/output/<result-file>`, where on a successful run the
highest-numbered iteration is the one that passed validation. Consumers treat
the agent's result as a filter, confirm every identifier against the forge API
before acting, and bound repeats with the run's own attempt counter.

Agent authors who want to share a follow-up ship it as a reusable workflow next
to the harness. Adopters add a thin `workflow_run` caller pinned by commit,
the same shape as the fullsend shim. This decision applies to per-repo
installation mode only.

## Consequences

- Requests for control-plane permissions are answered with a consumer workflow,
  so agent roles keep their current ceilings and no new App is installed.
- Identity is split on purpose: comments and reviews carry the App identity,
  control-plane actions carry `github-actions[bot]` attributed to the follow-up
  run.
- The artifact name and layout are now a contract; changing them needs a
  deprecation path and a note in the user guide.
- Consumers carry the confirmation cost: identifiers come from the API, the
  agent output only selects among them, and the runner fails a run whose output
  never validates, so `workflow_run.conclusion` is the first filter.
- Agents without a `validation_loop` schema publish only status; the guide says
  so, and the fleet agents gain a result contract only when they adopt a schema.
- Consumers currently pay for discovery: an artifacts-API call to learn which
  agent ran, a download of the full artifact including transcripts and logs,
  and knowledge of each agent's result filename. A runner-written summary
  artifact that carries the envelope and the validated result would collapse
  that to one small download and a `jq` filter; it is tracked as a follow-up
  in fullsend-ai/fullsend#7413 and does not change this decision.
