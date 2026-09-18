---
title: "118. Take a steer's authority from the follow-up run's Route job"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - authorization
  - dispatch
  - security
---

# 118. Take a steer's authority from the follow-up run's Route job

Date: 2026-09-18

## Status

Accepted

## Context

[ADR 0106](0106-serialize-agent-runs-and-coalesce-subsequent-events.md) ends its Decision with:
"How content provenance and actor authority constrain agent behavior is deferred to a separate
ADR." This is that ADR, for the updates a run in flight absorbs under
[ADR 0113](0113-steer-the-running-agent-on-work-item-updates.md): some of the delivered text is a
person instructing the agent, the rest is whatever anyone wrote on the item.

Every update that can steer a run also dispatches a follow-up run, whose `Route` job already
applied [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)'s collaborator check
to the principal its arm names — the comment author for `issue_comment`, not always the run's
actor. [ADR 0098](0098-entity-first-harness-evaluation.md) requires an action-indicating element
to carry its actor's current permission, resolved at evaluation time.

## Options

**Who may amend the task.** *Re-check permission per steer* makes the runner a second authorizer,
needing that token scope and free to drift from the Route job. *Trust the accepted run* delivers
everything swept up with an authorized run as instruction, so a stranger's comment seconds before
a collaborator's push arrives under the collaborator's authority. *Provenance only* (chosen): the
runner never authorizes; an instruction is only the comment the Route job evaluated.

**Whose text is context.** *Exclude every bot* — the `[bot]` suffix rule — drops the reviews of
Apps the repository installed and reads trust off a name any user account can imitate. *Include
everything* lets the run steer itself with its own status comment and fullsend agents steer each
other ([agent-to-agent injection](../problems/security-threat-model.md#threat-5-agent-to-agent-prompt-injection)).
*Exclude fullsend's own identities* (chosen): an installed App is a trust boundary the repository
drew, and its output is context the agent needs.

## Decision

A steer is authorized once, by the follow-up run's `Route` job. The runner verifies provenance
from run records the sender cannot write (the checks are in
[steering.md](../contributing/steering.md#provenance-what-the-runner-verifies)) and authorizes
nothing itself. It matches the reusable workflow by path and ref, not sha, so the trust it places
in "the same `Route` job" reaches exactly as far as the ref is protected: fullsend pins protected
branches, and that protection is an invariant the check relies on rather than verifies.

An **amendment** — text the agent acts on, over its original task — is the one comment an
accepted run's `Route` job evaluated, with its text unchanged since. A run whose `run-name`
names its comment id binds to that comment exactly; otherwise a run binds only when exactly one
of that actor's stage commands lies between the actor's previous run and this one — rejected
runs included, so a refused command keeps its own — and any other count binds nothing. Only events
whose run actor is by construction the login the arm checked confer it (`issue_comment` today,
per the [per-arm audit](../contributing/steering.md#amendments-and-context)); a re-run confers
none.
Everything else is **context**: data the agent reads and must not obey. A steer is content, never
capability, and an authorized author's content is still not safe
([agent-architecture.md](../problems/agent-architecture.md#core-principle-trust-derives-from-repository-permissions-not-agent-identity)).
This meets ADR 0098's rule: the `Route` job is the evaluation for that event, and the amendment
is bound to the text it saw.

Context excludes only fullsend's own output, never by the shape of a login. The primary rule is an
exact match against logins the runner resolves at start — its own App login and the review App
logins as `reusable-dispatch.yml` builds them; a runner that cannot resolve its login does not
steer. A supplement excludes an App-authored body carrying a `<!-- fullsend:` marker under any
login, which is how the receipt posted as `github-actions[bot]` stays out. App or human is the
forge's own verdict (`user.type`). A misclassification can only move an author from amendment to
context; no rule promotes anyone by name.

## Consequences

- No permission API is called inside a run, so there is no second authorizer to drift from ADR 0054.
- A stranger's comment on a busy item is context whatever authorized event it rides in with.
- Installed Apps' reviews reach the review agent as context, and a user named like a bot gains nothing.
- `pull_request_target` and `issues` runs arrive as context only; a head move needs no instruction.
- Without the comment id on the run, binding fails closed: a burst of commands by one login, a
  delayed dispatch, a run out of view or a run trailing its comment by over ten minutes leaves
  the update to the queued run as context.
- Steering depends on the runner knowing its own login, and a fullsend App it did not name posting
  without a marker enters context like any other App's comment.
