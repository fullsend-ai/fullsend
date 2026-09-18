---
title: "120. Receipt the absorbed update under the job token, and let the queued run skip on it"
status: Accepted
relates_to:
  - security-threat-model
  - flapping-convergence
topics:
  - concurrency
  - dispatch
  - security
---

# 120. Receipt the absorbed update under the job token, and let the queued run skip on it

Date: 2026-09-18

## Status

Accepted

## Context

A run in flight that absorbs an update to its work item
([ADR 0113](0113-steer-the-running-agent-on-work-item-updates.md)) leaves the follow-up run
queued behind it holding an event whose work is already done. Nothing tells that run so, and it
starts the agent and reviews the same head again — so steering costs *more* than preserving
alone, which produces one review of that head rather than two. The skip is what makes steering
pay; it is not an optimization laid on top of it.

Whatever carries the "already handled" claim is read by a run deciding to do *nothing*. Getting
it wrong in that direction drops an update silently, which is worse than the waste the claim
exists to remove — so the question is not where to record what a run absorbed, but who can write
such a record.

## Options

### The queued run always redoes the work

No new record and no new trust boundary: every queued run reconciles the item's current state,
exactly as it does today. It also makes steering a net loss, since the run in flight absorbs the
push and reviews the new head and the queued run then reviews it again.

### A marker on the terminal status comment

The runner already posts that comment, so the marker costs no extra request — but it goes out
under the App identity, the same one the agent's own output is posted under, so the marker
authenticates two public strings rather than the code path that wrote them. An injection can
induce the agent to emit a well-formed marker into its review body, and a post-script shelling
out to `gh` can post one directly, reaching none of the runner's sanitizing paths.

### A receipt posted and read under the GitHub Actions job token

The runner captures `GH_TOKEN` as the action passed it in, before minting swaps in the role
token, and posts the receipt as its own comment under that identity. It costs one request and
one identity resolution per run, and holds only where the platform gives the job an identity of
its own — GitHub Actions, not GitLab.

## Decision

Adopt the job-token receipt. **The author is the whole of the authentication, and it must be the
job token's.** After a successful run that absorbed at least one update, the runner posts a
receipt naming the follow-up runs it consumed; a queued run reads the latest receipt in its
pre-flight, before the start comment and before the pre-script, and exits 0 without starting the
agent when it finds its own run id listed.

Two conditions make that identity mean something, and both are enforced in code rather than
assumed. Minting must actually have swapped the credential — with no mint URL or no role nothing
is swapped, the same token stays in the environment a post-script inherits, and the receipt
credential is empty instead. And the receipt's login and the role's must differ, since the
action's `github_token` is a caller-supplied input that may resolve to the identity the agent
posts under; the check refuses to skip when they match.

A receipt claims the work is done, so it is written only for an outright success, once per run
rather than per steer, naming only what the iteration whose output shipped absorbed — and only
when that output was published, so a run whose agent reported an error and had its post-script
withheld writes none, as do failed, cancelled, skipped and unvalidated runs, and runs that
absorbed nothing. Both halves fail open in every direction — no receipt, an unreadable timeline,
an unresolvable login, a malformed run id, a failed post — because a false "already handled"
silently drops work while a false "not handled" costs one short run, which is the window the
platform has today.

The terminal status comment keeps an informational copy of the marker so a reader can see what a
run absorbed. The skip check does not honour it, and no App-authored marker of any shape is a
receipt; agent-authored text is neutralized on every path that posts it — the status comment, the
sticky comment and the tracker comment bodies — so the copy stays a report rather than something
the agent can compose. Minting swaps the job token out under both `GH_TOKEN` and `GITHUB_TOKEN`,
and a child script's environment drops any entry still holding it, so the credential is unreachable
by name and by value. What `consumed` attests differs by runtime: Claude Code acknowledges a
delivery by echoing its text verbatim, while pi acknowledges only the rpc id, so on pi a receipt
records that the runner delivered an update under that id rather than that the operator's words
reached the agent. The acknowledgement record the receipt intersects against is therefore
influenced by the run's own agent on pi — it can change the text an id acknowledges, though not
add an id the runner never delivered — which bounds the claim to delivery rather than content.
The mechanics are in
[steering.md](../contributing/steering.md#who-may-write-a-receipt).

## Consequences

- Steering stops costing *more* than preserving alone, which is the condition for enabling it
  anywhere.
- The trust boundary is the repository's own workflows: any job in it posts under the same login
  and could write a receipt, which is maintainer-controlled and deliberately not narrowed further.
- A credential leaked out of the runner's process could forge a receipt, but that is a compromise
  of the runner rather than of the sandbox, and of the same credential that already reads the
  Actions API.
- A caller that passes an App installation token as `github_token` never skips, and is warned
  once per run rather than silently trusting the agent's own identity.
- GitLab has no receipt, because its job token can neither post nor read notes, so a queued
  pipeline there does the work exactly as it does today.
