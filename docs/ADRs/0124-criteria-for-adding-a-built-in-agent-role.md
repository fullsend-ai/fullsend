---
title: "124. Criteria for adding a built-in agent role"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
  - agent-infrastructure
topics:
  - mint
  - least-privilege
  - roles
  - security
---

# 124. Criteria for adding a built-in agent role

Date: 2026-09-24

## Status

Accepted

## Context

Users keep building agents that need something the built-in roles do not
grant. The first request was `actions: write`, so an agent could re-run CI
jobs it classified as flaky (fullsend-ai/fullsend#7003). It was proposed as a
`ci-watch` role that combines `actions: write` with `pull_requests: write`
(fullsend-ai/fullsend#7353, closed). Maintainers had no written test for such
a request, so each one would be argued from scratch.

A built-in role is a trust boundary that every adopter shares
([ADR 0007](0007-per-role-github-apps.md)). The mint binds a role to its
allow-list and a GitHub App installation, not to an agent. Any harness may
name any served role. A role's `write` ceiling therefore reaches every agent
that names it, in every enrolled repository. Per-stage privilege levels
([ADR 0073](0073-named-mint-privilege-levels.md)) narrow a token within a role.
They never add a scope the App lacks. The threat model asks for least
privilege and separation of duties
([security-threat-model.md](../problems/security-threat-model.md#threat-2-insider-threat--compromised-credentials)).

## Decision

Maintainers add or widen a built-in agent role only when the request passes
all six tests below. A request that fails is answered with the alternative
that the failing test names.

1. **No path without a role.** Can the action run from a workflow in the
   user's repository with the job token, or with an existing role? Then it
   does. Actions taken after a run belong here: re-running, dispatching or
   cancelling workflow runs, deploying. The user chains a workflow on the
   shim's `workflow_run` event and reads the run's `fullsend-<agent>` artifact
   ([Chaining Follow-up Workflows](../guides/user/chaining-follow-up-workflows.md)).
2. **The agent's output needs the scope.** Built-in roles carry scopes on what
   agents produce: issues, pull requests, comments, reviews, labels, code and
   project items. Agent roles get no write access on control-plane scopes:
   `actions`, `workflows`, `administration`, `secrets`, variables,
   environments and deployments. Read access, such as reading run logs, is
   judged like any other scope. This follows GitHub's own practice in GitHub
   Agentic Workflows: agents run read-only and request actions through
   structured output, which separate permission-controlled jobs execute
   ([safe outputs](https://github.github.com/gh-aw/reference/safe-outputs/)).
   Its experimental `approve-workflow-run` safe output (merged in
   github/gh-aw#52541) calls `actions: write` "a broad GitHub permission
   scope" and grants it only where that output is explicitly enabled
   ([gh-aw ADR-52541](https://github.com/github/gh-aw/blob/38a5e1f56a1cdc7e606ce7bb48ed5b899375d212/docs/adr/52541-add-approve-workflow-run-safe-output.md)).
3. **The App identity is visible where it acts.** A role is also a GitHub
   App, and its name is how people see which agent acted: on comments,
   reviews, labels, commits and pull requests. A new App is worth installing
   only when its actions show up under that name on the issue or pull
   request, separate from the default `github-actions[bot]`. An action whose
   actor never surfaces there gains no visibility from its own App.
4. **One purpose.** The role serves one purpose. The request names the
   endpoints its agents call. The `read` level is a strict subset of `write`.
5. **Every write group earns its place next to the others.** This is where
   caution concentrates. A role that holds two or more write permission groups
   is judged by what the groups enable together, not one scope at a time. The
   request lists what each new scope unlocks across all of its endpoints, not
   only the one it needs. It then lists what the new scope enables combined
   with each other write group in the role. A combination that lets one token
   both produce content and control how automation runs fails.
   `coder` passes: contents, pull requests and issues together are its one
   purpose, writing a change and proposing it.
6. **General need.** More than one agent or adopter needs it, and it is safe
   for any harness that names it. A need specific to one agent is served by a
   custom role on its author's standalone mint
   ([Custom Agent Identity](../guides/user/custom-agent-identity.md)). What an
   operator serves on their own mint is outside this decision.

Existing roles are unchanged. Roles used only by Fullsend's own dispatch and
test infrastructure are outside this decision.

## Applying the criteria: `actions: write` to re-run CI

The `ci-watch` request fails tests 1, 2, 3 and 5.

- **Test 1.** A workflow the user owns, with `permissions: actions: write`,
  re-runs the job with the job token. The guide runs this end to end.
- **Test 2.** Re-running a job is a control-plane operation, not agent output.
- **Test 3.** A re-run shows on the pull request only as a new check result.
  That check run belongs to the `github-actions` app, whoever triggered the
  re-run, and the pull request timeline records no event. The actor appears
  only as `triggering_actor` on the run's attempt. A dedicated App would add
  an installation for every adopter and nothing a reviewer sees.
- **Test 5.** `actions: write` also approves fork pull request runs,
  dispatches any workflow on any ref, and cancels or deletes runs, logs and
  artifacts. With `pull_requests: write` in the same token, one agent could
  shape a pull request and control the runs it triggers.

The other routes to the same capability fail for their own reasons:

- **A follow-up job in `reusable-dispatch.yml`.** Fullsend would execute every
  agent's follow-up logic, and each new follow-up would need a release.
- **A custom privilege level carrying `actions: write`.** Extra levels exist
  only on custom roles served by a standalone mint. A level the mint does not
  serve fails at mint time with HTTP 400 ("role has no level"), after the
  agent has spent its budget. One stage receives one token, so a post-script
  that comments and re-runs still holds both writes. Custom levels are not
  enforced to nest (fullsend-ai/fullsend#7446).
- **The workflow token in the post-script.** One process would hold two
  identities, against the boundary in fullsend-ai/fullsend#7286.

## Consequences

- Maintainers answer a role request with the test it failed and that test's
  alternative, instead of a fresh debate.
- A role proposal must list endpoints and the combinations of its write
  groups, which makes review slower but auditable.
- Control-plane actions after a run carry `github-actions[bot]` from the
  user's workflow, so the `fullsend-<agent>` artifact becomes a surface users
  depend on; a runner-written summary artifact is planned as its stable form
  (fullsend-ai/fullsend#7413).
- A need specific to one agent costs its author a standalone mint to operate.
- How new roles are tested and promoted stays open in
  [architecture.md](../architecture.md); this decision covers only admission.
