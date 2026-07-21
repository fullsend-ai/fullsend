---
title: "54. Require authorization on all agent dispatch paths"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - slash-commands
  - dispatch
---

# 54. Require authorization on all agent dispatch paths

Date: 2026-05-29

## Status

Accepted

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
has write-level permission on the repository. Today, only a subset of
dispatch paths gate on this check:

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

All agent dispatch paths require authorization before dispatching.
The check applies universally — to slash commands and to automatic
event triggers where the acting user may be external.

Both `is_authorized` (slash commands) and `is_event_actor_authorized`
(event triggers) delegate to a shared `has_write_permission` helper
(renamed `has_repo_permission` — see note below) that calls the
collaborator permission API. This ensures consistent behavior across
all paths.

### Slash commands

The dispatch routing logic must call `is_authorized` for `/fs-triage`,
`/fs-code`, and `/fs-review` with the same guard pattern already used by
`/fs-fix`, `/fs-retro`, and `/fs-prioritize`:

```bash
if [[ "${COMMENT_USER_TYPE}" != "Bot" ]] && is_authorized; then
  STAGE="<stage>"
fi
```

### Authorization mechanism: collaborator permission API

**Why not `author_association`?** The `author_association` field in
webhook payloads does not correctly reflect private org membership — an
org admin with private membership gets `CONTRIBUTOR` instead of `MEMBER`
(see [github/gh-aw-mcpg#2862](https://github.com/github/gh-aw-mcpg/issues/2862)).

Instead, all authorization checks (both slash commands and event
triggers) use the collaborator permission API
(`GET /repos/{owner}/{repo}/collaborators/{username}/permission`) which
returns the user's **effective** role including inherited org grants
regardless of membership visibility.

The implementation uses a three-function layering:

- `has_write_permission(username)` — calls the API, checks `.role_name`
  (renamed `has_repo_permission` — see note below)
- `is_authorized()` — delegates to `has_write_permission` for the comment author
- `is_event_actor_authorized(username)` — delegates for event actors

See the workflow files (`reusable-dispatch.yml`, scaffold `dispatch.yml`)
for the canonical implementation.

Users with `admin`, `maintain`, or `write` role are authorized. Users
with only `triage` or `read` role are denied. This maps to "users with
push access to the repository."

### Automatic event triggers

| Event | Actor checked | Gated? |
|-------|---------------|--------|
| `issues.opened` | Issue opener | Yes |
| `issues.edited` | Event sender (editor) | Yes |
| `pull_request_target.opened` / `synchronize` | PR author | Yes |
| `issues.labeled` | Label applier | Already implicit (requires write access) |
| `pull_request_target.ready_for_review` | PR author | Yes (same branch as opened/synchronize) |
| `pull_request_target.closed` | Closer | Already implicit (requires write access) |
| `pull_request_review.submitted` | Reviewer | Already gated (requires review-bot authorship) |
| `issue_comment` (needs-info re-triage) | Commenter | Weaker gate: `author_association != NONE` or issue author (intentional — allows clarification from external reporters) |

For external contributors (issues opened or PRs submitted by
non-members), the agent does not fire automatically. A maintainer can
still trigger the agent explicitly by:

- Applying a label (`ready-to-code`, `ready-for-review`) — label
  application requires write access, which is an implicit auth gate.
- Posting a slash command (`/fs-triage`, `/fs-code`, `/fs-review`).

This does not prevent external contributions — it prevents spending
inference compute on them automatically.

### Bot-to-bot workflows are preserved

Agent-to-agent handoffs use label-based triggers, not slash commands.
When one agent completes a stage, its post-script applies a label
(e.g., `ready-for-triage`, `ready-to-code`, `ready-for-review`) which
triggers the next stage via the `issues.labeled` dispatch path. Label
application requires write access — an implicit authorization gate — so
no explicit `is_authorized` check is needed on that path.

The `COMMENT_USER_TYPE != "Bot"` check in the slash command guard means
bot accounts cannot invoke slash commands at all (the condition
short-circuits to false). This is intentional: bots have no need to use
slash commands because they orchestrate via labels.

### Visible feedback for unauthorized users

When a non-Bot user fails `is_authorized`, the dispatch script should
provide visible feedback. The dispatch mechanism is open source and
present in every enrolled repo's workflow files — silent failure
provides no security benefit but does confuse legitimate contributors.

The dispatch script should provide some form of visible response (e.g.,
a reaction, a comment, or both) so the user knows their command was
received but not executed. This is not yet implemented — commands
currently fail silently. Tracked as future work.

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
authorization rules (e.g., allowing `triage` or `read` permission), it
should do so by extending the `has_write_permission` function's
(renamed `has_repo_permission` — see note below) allowed permission
list, not by bypassing the check.

> **Note (2026-07-17, [#5223](https://github.com/fullsend-ai/fullsend/issues/5223)):**
> Observation stages (triage, review) now accept the GitHub `triage`
> role via a parameterized `has_repo_permission` helper (`min=triage`).
> Mutation stages (code, fix, and other write-gated slash commands)
> remain at `min=write`. Label-triggered `ready-to-code` requires a
> write+ labeler (or a bot, for agent handoff). Exception:
> `pull_request_target.closed` → retro stays intentionally ungated so
> any closer can trigger read-only lifecycle accounting. This follows
> the extension path above rather than bypassing the check.

> **Note (2026-07-21, [#5188](https://github.com/fullsend-ai/fullsend/issues/5188), [#2636](https://github.com/fullsend-ai/fullsend/issues/2636)):**
> `has_write_permission` referenced above was folded into the
> parameterized `has_repo_permission` helper introduced by #5223 and no
> longer exists as a separate function.
>
> GitHub App bot accounts (e.g. the org's own code, review, triage, and
> retro agents) are not resolvable via the collaborator permission API —
> `role_name` comes back empty even though the bot has legitimate
> push/comment access via its app installation grant. This silently
> broke two independent paths: `pull_request_target.opened/synchronize/
> ready_for_review` (PRs authored by the org's own agents never received
> automatic review, including after every fix-agent iteration — #5188),
> and `issues.opened/edited` (issues filed or edited by the org's own
> agents, e.g. the retro agent's proposal issues, never received
> automatic triage — #2636, previously worked around by having the
> filer also apply a routing label rather than fixing the actor check).
>
> Both paths now also accept a recognized fullsend agent bot actor
> (`is_org_bot`) as authorized, bypassing the collaborator permission API
> check. This follows the extension path above rather than bypassing the
> check, via two exact-match trust paths — never a prefix/suffix
> wildcard:
>
> 1. `fullsend-ai-<role>[bot]` (coder, review, triage, retro,
>    prioritize), unconditionally, regardless of the installing repo's
>    own org name. fullsend's default deployment model is a shared,
>    vendor-owned App per role (ADR 0029/0059/0068) — every adopting org
>    installs the *same* public Apps, so the bot identity is fixed and
>    does not vary with `ORG_NAME` (`github.repository_owner`). An
>    earlier version of this check instead matched `${ORG_NAME}-*[bot]`,
>    which only worked by coincidence on fullsend-ai/fullsend itself
>    (where org name and vendor-App prefix happen to collide) and was a
>    no-op for every other adopting org.
> 2. `${ORG_NAME}-<role>[bot]`, for self-managed orgs that run their own
>    private per-role Apps named after themselves (ADR 0029/0033) instead
>    of the shared vendor Apps.
>
> Both paths match one of a fixed, known set of role suffixes — never a
> bare prefix wildcard — so a third party can't forge the bypass by
> registering a GitHub App named `${ORG_NAME}-<anything>[bot]` or
> `fullsend-ai-<anything>[bot]`. `is_org_bot` also takes an optional
> second argument to restrict the match to one specific role (e.g.
> `is_org_bot "${REVIEW_USER_LOGIN}" review`) — used by the
> `pull_request_review` → fix auto-dispatch gate, which previously
> checked `REVIEW_USER_LOGIN == "${ORG_NAME}-review[bot]"` directly and
> had the identical ORG_NAME-vs-shared-App bug as #5188/#2636 (also only
> "worked" by coincidence on fullsend-ai/fullsend itself). The same
> single-identity `REVIEW_BOT="${ORG_NAME}-review[bot]"` mistake also
> existed in three more places that fetch a prior review comment/review
> by login for context rather than for dispatch authorization — the
> `reusable-dispatch.yml` and `reusable-fix.yml` "Pre-fetch review body"
> steps, and `pre-fetch-prior-review.sh` — all now check both identities
> (`fullsend-ai-review[bot]` and `${ORG_NAME}-review[bot]`) directly,
> since `jq` can't call the bash `is_org_bot` helper. The
> separate `PR_USER_LOGIN =~ \[bot\]$` check further down that same gate
> is untouched — it decides whether to auto-fix without requiring an
> explicit `fullsend-fix` label, and stays intentionally broad. The
> `issues.labeled` `ready-to-code` path is also unaffected by this note
> — it already allows a generic `[bot]$` actor for agent handoff, per
> the table above, which is a deliberate, broader trust decision for
> that specific bot-to-bot continuation path (see #2679): a request
> already reached that point via an authorized trigger, so trusting any
> bot to continue the chain doesn't extend trust to a new actor.
>
> This is a platform-constraint exception, not a relaxation of this
> ADR's core principle: authorization here still derives from
> repository permissions (either fullsend's own vendor-App identity or
> the installing org's own exact identity), not an arbitrary identity
> claim. The `pull_request_target`/`issues.opened`/`issues.edited` uses
> reach only read-only observation stages (triage, review); the
> `pull_request_review` use reaches the mutation `fix` stage, but only as
> a continuation of a request already authorized earlier in the same
> chain — a genuine `changes_requested` verdict from fullsend's own
> review bot — not a fresh grant of trust to a new actor, consistent
> with the `ready-to-code` reasoning above.
>
> **Note (2026-07-22):** the "not a fresh grant of trust" framing above
> assumes the `changes_requested` review genuinely came from fullsend's
> own review bot. That assumption is qualitatively weaker on the
> self-managed path than on the shared-vendor-App path: if a third party
> squats `${ORG_NAME}-review[bot]` at a self-managed org (the residual
> risk this ADR already accepts elsewhere), `is_org_bot` still passes —
> by design, since the identity itself is what's untrustworthy in that
> scenario — and the same login match is what the "Pre-fetch review
> body" steps in `reusable-fix.yml`/`reusable-dispatch.yml` use to pull
> that review's body text into the autonomous fix agent's prompt. Unlike
> the `ready-to-code` bot-to-bot continuation (which only ever advances
> a *stage*, not attacker-controlled *content*), this path feeds
> attacker-authored text into a privileged, auto-pushing agent — a
> prompt-injection channel, not benign observation. Pinning this
> specific check to `performed_via_github_app.id` (the numeric,
> non-reusable app ID) instead of login string would close this gap, but
> is out of scope for #5188/#2636 — tracked as
> [#5463](https://github.com/fullsend-ai/fullsend/issues/5463).

## Consequences

- All dispatch paths require write-level repository permission,
  closing the cost-exposure and abuse-surface gaps for both slash
  commands and automatic triggers.
- External users can no longer trigger agent runs by opening issues, PRs,
  or posting slash commands on public repos.
- Maintainers retain full control: labels and slash commands let them
  trigger agents on external contributions when appropriate.
- Bot-to-bot orchestration (e.g., triage → code handoff) is unaffected
  because it uses label-based triggers, which require write access and
  do not pass through the slash command authorization gate.
- The dispatch routing logic becomes consistent: every dispatch path
  checks authorization of the acting user, reducing cognitive load.
- Unauthorized slash command attempts currently fail silently (STAGE
  remains empty). Visible feedback (reaction + comment) is desirable
  future work to improve UX for legitimate contributors who don't yet
  have the required permission.
- External contributors who don't want to become members will depend on
  maintainers to trigger agents on their behalf — an acceptable
  trade-off to keep the abuse surface minimal.
- Future work: rate-limited auto-triage for external issue reporters
  ([#1687](https://github.com/fullsend-ai/fullsend/issues/1687),
  [vouch](https://github.com/mitchellh/vouch), or per-org trust
  policies) could relax this boundary for drive-by bug reports without
  re-opening the abuse surface for slash commands.
