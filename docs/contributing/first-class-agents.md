# First-class agent promotion

When a custom agent should graduate into the default catalog in
[`fullsend-ai/agents`](https://github.com/fullsend-ai/agents), and when it
should stay custom.

The decision is [ADR 0111](../ADRs/0111-first-class-agent-promotion.md). This
page is the operational checklist and a point-in-time classification. It is
not a second source of criteria — if this page and the ADR diverge, the ADR
wins.

This is **catalog membership**, not the configured-default / derived / custom
axis in [default-vs-custom.md](../agents/topics/default-vs-custom.md). A custom
agent in that sense can still be a promotion candidate here.

Do not use "tier" for these statuses. That word already means other things
in this repo — see [Tier Conventions](tier-conventions.md).

## Lifecycle statuses

| Status | Meaning |
|--------|---------|
| **Prototype** | Custom agent, baking in a consumer repo. May become first-class. |
| **First-class** | Ships in `fullsend-ai/agents` as part of the default catalog. |
| **Custom** | Stays out of the catalog (internal or toolchain-specific). |

Lifecycle: prototype as a custom agent → bake in production → promote, or
remain custom. How to build and register the prototype is
[Bring Your Own Agent](../guides/user/bring-your-own-agent.md). Where
experimental files live during prototyping is tracked in
[#1084](https://github.com/fullsend-ai/fullsend/issues/1084).

## Discover the current catalog

The agents repo is the source of truth. Do not copy harness names into docs
as a living inventory — they go stale.

```bash
# List the YAML harness files in fullsend-ai/agents
gh api repos/fullsend-ai/agents/contents/harness \
  --jq '.[] | select(.type == "file" and (.name | endswith(".yaml"))) | .name | rtrimstr(".yaml")'
```

A local clone works the same way: `ls harness/*.yaml` at the agents-repo
root (strip the `.yaml` suffix). Both commands enumerate harness files, not
the first-class set directly — a harness file existing does not by itself
confirm catalog-fit, and the harness directory can contain dogfood-only or
otherwise non-catalog harnesses (see `scribe` in the snapshot below). Filter
the raw listing against the documented default catalog — the table in
[`docs/agents/README.md`](../agents/README.md) and the shipped-role list in
[escalation-ladder.md](../agents/topics/escalation-ladder.md) — to get the
current first-class set. See the
[`author-fullsend-augmentations`](../../skills/author-fullsend-augmentations/SKILL.md)
skill for the full discovery pattern.

## Promotion checklist

All of the following must hold. Catalog fit and the cap are not optional
just because the prototype is reliable.

**Catalog fit**

- [ ] Expands fullsend into an SDLC stage most adopters will want, **or** is
      broadly useful regardless of a specific vendor toolchain.
- [ ] Not a team-internal workflow.
- [ ] Promoting it keeps the catalog at or below 15 first-class agents, or
      an existing first-class agent is retired or merged in the same change.

**Maturity**

- [ ] Production bake in at least one non-eval repository, covering the
      trigger events the agent claims.
- [ ] No open class of failure that would ship to every adopter.
- [ ] User-facing docs: purpose, triggers, and extension points.
- [ ] Script tests for pre/post scripts.
- [ ] At least one functional test case
      ([ADR 0052](../ADRs/0052-functional-tests-for-agent-pipelines.md)).
- [ ] Output schema
      ([ADR 0022](../ADRs/0022-harness-level-output-schema-enforcement.md)).
- [ ] Sandbox scoped to the agent's responsibility
      ([ADR 0020](../ADRs/0020-composable-single-responsibility-agents-with-individual-sandboxes.md)).
- [ ] Maintainer sign-off that catalog fit still holds.

## Classification snapshot (2026-09-09)

Snapshot of applying ADR 0111, not a living roster. Re-run the discovery
command above and filter it against the documented default catalog (as
described above) for the current first-class set.

| Agent | Status | Notes |
|-------|--------|-------|
| triage | First-class | SDLC stage. In the catalog. |
| code | First-class | SDLC stage. In the catalog. |
| review | First-class | SDLC stage. In the catalog. |
| fix | First-class | SDLC stage. In the catalog. |
| retro | First-class | SDLC stage. In the catalog. |
| prioritize | First-class | SDLC stage. Promoted from a prototype ([#329](https://github.com/fullsend-ai/fullsend/issues/329)) before these criteria existed. |
| scribe | Custom | Mint-only dogfood role (see [Configuring Agent Behavior](../guides/user/customizing-agents.md)); not in the `docs/agents/README.md` catalog list or the escalation-ladder's shipped-role list. Not counted toward the cap. |
| classify | Custom | Broadly useful if baked as a prototype; not currently in the catalog. |
| Specbot | Custom | Named in [#1084](https://github.com/fullsend-ai/fullsend/issues/1084). Would need to bake as a prototype, then pass catalog fit and the maturity bar. |
| release | Custom | SDLC-adjacent if scoped as a general release stage; stays custom if it is product-specific. |

Roles described in [agent-architecture.md](../problems/agent-architecture.md)
that have no harness (for example quality/drift detection) are not catalog
entries. Building one would start as a prototype and need this checklist.

As of this snapshot the catalog has 6 of the 15-agent soft cap.

## How to propose a promotion

1. Confirm the prototype is registered as a custom agent and has baked.
2. Open an issue that walks the checklist above. Link production evidence
   (runs, failure classes, docs, script tests, and the ADR 0052 functional
   test case).
3. If the catalog is at 15, name the first-class agent being retired or
   merged in the same change — that is the only way past the cap.
4. Land the agent in `fullsend-ai/agents` (harness, definition, schema,
   scripts, docs) only after maintainer sign-off. The functional test case
   itself lands in fullsend's `eval/` tree
   ([ADR 0052](../ADRs/0052-functional-tests-for-agent-pipelines.md)), not
   in the agents repo.

## See also

- [ADR 0111](../ADRs/0111-first-class-agent-promotion.md) — the decision
- [ADR 0058](../ADRs/0058-agent-registration.md) — how custom agents register
- [Default, derived, and custom agents](../agents/topics/default-vs-custom.md) —
  customization depth, not catalog membership
- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) — building a
  prototype
- [Testing the Agents](../problems/testing-agents.md) — eval and script-test
  expectations
