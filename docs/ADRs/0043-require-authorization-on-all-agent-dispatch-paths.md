---
title: "43. Require authorization on all agent dispatch paths"
status: Proposed
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - slash-commands
  - dispatch
---

# 43. Require authorization on all agent dispatch paths

Date: 2026-05-29

## Status

Proposed

Builds on [ADR 0034](0034-centralized-shim-routing-via-dispatch.md)
(centralized dispatch routing) and
[ADR 0042](0042-fs-prefix-for-slash-commands.md) (`/fs-` prefix
convention).

Related: [#877](https://github.com/fullsend-ai/fullsend/issues/877)
(agents must not model their own authority limitations — this ADR
implements the platform-level enforcement that principle requires).

## Context

The dispatch routing logic (`dispatch.yml` / `reusable-dispatch.yml`)
defines an `is_authorized` helper that checks whether the acting user
has an `author_association` of OWNER, MEMBER, or COLLABORATOR. Today,
only a subset of dispatch paths gate on this check:

| Trigger | Gated? | Notes |
|---------|--------|-------|
| `/fs-triage` | No | Any commenter triggers triage |
| `/fs-code` | No | Any commenter triggers code |
| `/fs-review` | No | Any commenter triggers review |
| `/fs-fix` | Yes | `is_authorized` + non-Bot check |
| `/fs-retro` | Yes | `is_authorized` + non-Bot check |
| `/fs-prioritize` | Yes | `is_authorized` + non-Bot check |
| `issues.opened` | No | Any issue opener triggers triage |
| `pull_request_target.opened` | No | Any PR author triggers review |

The ungated paths allow any GitHub user to trigger agent inference runs
— either by commenting a slash command on a public issue/PR, or by
opening an issue or PR directly. This creates two risks:

1. **Cost exposure.** Each agent run consumes inference compute. An
   external user opening issues or posting `/fs-code` across a public
   org could generate significant cost with no rate limit.
2. **Abuse surface.** The security threat model
   ([security-threat-model.md](../problems/security-threat-model.md))
   ranks external prompt injection as the highest-priority threat. An
   unauthorized user triggering agent runs is a prerequisite for many
   injection attacks — the attacker needs the agent to run before they
   can influence its behavior.

The inconsistency also violates the principle of least surprise: a
contributor who sees `/fs-fix` rejected would reasonably expect
`/fs-code` and auto-triage to behave the same way.

## Decision

All agent dispatch paths require `is_authorized` before dispatching.
The authorization check applies universally — to slash commands and to
automatic event triggers where the acting user may be external.

### Slash commands

The dispatch routing logic must call `is_authorized` for `/fs-triage`,
`/fs-code`, and `/fs-review` with the same guard pattern already used by
`/fs-fix`, `/fs-retro`, and `/fs-prioritize`:

```bash
if [[ "${COMMENT_USER_TYPE}" != "Bot" ]] && is_authorized; then
  STAGE="<stage>"
fi
```

### Automatic event triggers

For events where the acting user may be external, the dispatch logic
must check the actor's `author_association` before setting a `STAGE`:

| Event | Actor checked | Gated? |
|-------|---------------|--------|
| `issues.opened` / `issues.edited` | Issue opener | Yes |
| `pull_request_target.opened` / `synchronize` | PR author | Yes |
| `issues.labeled` | Label applier | Already implicit (requires write access) |
| `pull_request_target.ready_for_review` | PR author/maintainer | Already implicit |
| `pull_request_target.closed` | Closer | Already implicit (requires write access) |
| `pull_request_review.submitted` | Reviewer | Already gated (requires review-bot authorship) |

For external contributors (issues opened or PRs submitted by
non-members), the agent does not fire automatically. A maintainer can
still trigger the agent explicitly by:

- Applying a label (`ready-to-code`, `ready-for-review`) — label
  application requires write access, which is an implicit auth gate.
- Posting a slash command (`/fs-triage`, `/fs-code`, `/fs-review`).

This does not prevent external contributions — it prevents spending
inference compute on them automatically.

### Bot-to-bot workflows are preserved

The `COMMENT_USER_TYPE != "Bot"` check precedes `is_authorized` in the
slash command guard. Bot accounts (GitHub App bots) bypass the
`is_authorized` gate entirely. This preserves existing automated
workflows where one agent's post-script triggers the next stage by
posting a slash command (e.g., triage completing and commenting
`/fs-code` to start implementation).

Bot accounts are trusted because they authenticate via GitHub App
installation tokens scoped to the org, not via user credentials.

### Visible feedback for unauthorized users

When a non-Bot user fails `is_authorized`, the dispatch script must
provide visible feedback. The dispatch mechanism is open source and
present in every enrolled repo's workflow files — silent failure
provides no security benefit but does confuse legitimate contributors.

The dispatch script must provide some form of visible response (e.g., a
reaction, a comment, or both) so the user knows their command was
received but not executed. The exact mechanism is an implementation
detail.

For automatic triggers (e.g., unauthorized user opens an issue), no
feedback is needed — the user didn't explicitly request an agent run.

### Interaction with per-repo configurability

The `is_authorized` check is a platform-level security boundary, not a
per-repo policy. Individual repos cannot disable it. Per-repo
configurability (e.g., which stages are enabled, which labels trigger
automation) operates within the authorization boundary — a repo can
disable `/fs-code` entirely, but it cannot make `/fs-code` available to
unauthorized users.

If a future per-repo configuration system needs to customize
authorization rules (e.g., allowing CONTRIBUTOR association in addition
to OWNER/MEMBER/COLLABORATOR), it should do so by extending the
`is_authorized` function's association list, not by bypassing the check.

## Consequences

- All dispatch paths require OWNER, MEMBER, or COLLABORATOR association,
  closing the cost-exposure and abuse-surface gaps for both slash
  commands and automatic triggers.
- External users can no longer trigger agent runs by opening issues, PRs,
  or posting slash commands on public repos.
- Maintainers retain full control: labels and slash commands let them
  trigger agents on external contributions when appropriate.
- Bot-to-bot orchestration (e.g., triage → code handoff) is unaffected
  because bot accounts bypass the human authorization check.
- The dispatch routing logic becomes consistent: every dispatch path
  checks authorization of the acting user, reducing cognitive load.
- Unauthorized slash command attempts get visible feedback (reaction +
  comment), improving UX for legitimate contributors who don't yet have
  the required association.
- External contributors who don't want to become members will depend on
  maintainers to trigger agents on their behalf — an acceptable
  trade-off to keep the abuse surface minimal.
