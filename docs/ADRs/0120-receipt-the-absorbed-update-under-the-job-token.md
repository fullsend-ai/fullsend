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

A run in flight that absorbs an update
([ADR 0113](0113-steer-the-running-agent-on-work-item-updates.md)) leaves the queued follow-up
run holding work already done. Nothing tells it so, and it reviews the same head again, so
steering costs *more* than preserving alone. The skip is what makes steering pay.

An "already handled" claim is read by a run deciding to do *nothing*, and a wrong one drops an
update silently. So the question is who can write such a record.

## Options

### The queued run always redoes the work

No new record and no new trust boundary: every queued run reconciles the item's current state.
It also makes steering a net loss, since the queued run reviews the head the run in flight did.

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

Adopt the job-token receipt. **Authorship is the whole of the authentication: a receipt counts
only when the job token posted it, under the login that token resolves, and its body was never
edited since.** After a successful run that absorbed at least one update, the runner posts a
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
silently drops work while a false "not handled" costs one run, which can be a full review.

The terminal status comment keeps an informational copy of the marker so a reader can see what a
run absorbed. The skip check does not honour it, and no App-authored marker of any shape is a
receipt. Agent-authored text is neutralized in the sticky and tracker comment bodies, and the
status comment carries none beyond a detail field that escapes every `<`, so the copy stays a
report rather than something the agent can compose. Minting swaps the job token out under both
`GH_TOKEN` and `GITHUB_TOKEN`, and a child script's environment drops any entry still holding it,
so a post-script cannot reach it by name or by value. A provider credential expanding
`GH_WORKFLOW_TOKEN` ([ADR 0114](0114-github-packages-via-host-bound-workflow-token-provider.md))
does hand it to a sandbox, where the agent can recover it, and every job shares the login, so no
receipt is honoured while any definition in `.fullsend/providers/` expands it; one fetched by URL
is not inspected. What `consumed` attests differs by runtime: Claude Code acknowledges a delivery
by echoing its text verbatim, while pi acknowledges only the rpc id, so on pi a receipt records
that the runner delivered an update under that id rather than that the operator's words reached
the agent. The acknowledgement record the receipt intersects against is therefore influenced by
the run's own agent on pi — it can change the text an id acknowledges, though not add an id the
runner never delivered — which bounds the claim to delivery rather than content. The mechanics are
in [steering.md](../contributing/steering.md#who-may-write-a-receipt).

A second skip covers a duplicate no receipt can. Two events on one head, such as a pull request
opened and labelled a second apart, each route a review; the run in flight rejects the other as
not fresh and writes no receipt. So a queued review also exits 0 when the review App (exact
logins, the forge's App verdict) reviewed the current head after the queued run was created. A
human `/fs-review`, a manual re-run, an unreadable event and any error run the review.

## Consequences

- Steering stops costing *more* than preserving alone, which is the condition for enabling it
  anywhere.
- The trust boundary is the repository's own workflows: any job in it posts under the same login
  and could write a receipt, which is maintainer-controlled and deliberately not narrowed further.
- Where the workflow-token provider is configured, queued runs do the work until OpenShell
  placement stops the token being recoverable; elsewhere a forger needs the runner's credential.
- A caller that passes an App installation token as `github_token` never skips, and is warned
  once per run rather than silently trusting the agent's own identity.
- An injected review from the review App can suppress only repeat reviews of the head it names.
- GitLab has no receipt, because its job token can neither post nor read notes, so a queued
  pipeline there does the work exactly as it does today.
