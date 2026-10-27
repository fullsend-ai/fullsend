---
title: Pre-submit Review
---

# Pre-submit Review

This page is for people contributing to **fullsend itself**, not for
operators of a fullsend installation. Fullsend still runs its own
[review agent](../agents/review.md) on every pull request; that is the
platform review path and the gate after you open a PR.

Separately, we recommend a local multi-model **review squad** as a
pre-submit check. The two tools are complementary, not alternatives.

## Why two review tools

The Fullsend review agent evaluates every PR automatically. It is the
authoritative in-platform review.

Guannan Sun's [review squad](https://gitlab.cee.redhat.com/gsun/ai-agent-base/-/tree/main/skills/review-squad)
runs multiple models on the same change and has been catching issues
the platform review agent currently misses — for example
[cross-file impact analysis](https://github.com/fullsend-ai/fullsend/issues/1525)
and
[sibling-command consistency](https://github.com/fullsend-ai/fullsend/issues/3965).
The main differentiator is collaborative multi-model review, which
Fullsend cannot yet host natively.

Running the squad locally before you open a PR catches those issues
earlier and reduces review-iteration churn after submit.

## Interim recommendation

Until the Fullsend review agent gains a comparable multi-model
strategy ([#6322](https://github.com/fullsend-ai/fullsend/issues/6322)),
contributors should run the review squad locally before opening a PR
when they can.

This is **not** a replacement for the platform review agent. The
review agent still runs on the PR. When #6322 lands, this
recommendation will be retired so the external squad is no longer the
default pre-submit path.

## Cost and local setup

The review squad is an external tool. Fullsend does not vendor, host,
or pay for it.

- **Local setup.** Follow the instructions in the [review squad
  skill](https://gitlab.cee.redhat.com/gsun/ai-agent-base/-/tree/main/skills/review-squad).
  The source is hosted on Red Hat's internal GitLab and may require
  access.
- **Contributor-side cost.** The squad uses multiple models, so you
  pay for inference on your own machine (or your own API accounts).
  That cost sits with the contributor until Fullsend can absorb
  multi-model review into the platform path.

If you cannot run the squad (no access, no local setup, or cost),
open the PR anyway. The platform review agent still reviews it.

## Related

- [Review agent](../agents/review.md) — the in-platform PR review path
- [CONTRIBUTING.md](../../CONTRIBUTING.md) — contribution workflow
- [#6322](https://github.com/fullsend-ai/fullsend/issues/6322) — native
  multi-model review (the work that retires this recommendation)
- [#1525](https://github.com/fullsend-ai/fullsend/issues/1525),
  [#3965](https://github.com/fullsend-ai/fullsend/issues/3965) —
  review-agent gaps the squad currently covers
