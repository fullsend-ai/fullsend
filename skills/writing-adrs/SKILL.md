---
name: writing-adrs
description: >-
  Use when writing, proposing, or accepting Architecture Decision Records (ADRs)
  in this repo. Use when a decision has crystallized from a problem doc and needs
  to be recorded, or when updating living documents after an ADR is accepted.
---

# Writing ADRs

## Overview

An ADR records exactly **one** decision. Problem docs explore; ADRs decide.
`docs/architecture.md` and problem docs are the current state (mutable). ADRs
are point-in-time records (immutable once accepted).

### ADRs are immutable records

Once an ADR is accepted, its content is frozen. It captures the decision, the
context that existed at that time, and the consequences as understood then. When
new information arrives or circumstances change, write a **new** ADR that
supersedes the old one -- do not edit the original's Context, Decision, or
Consequences sections.

**Acceptable modifications to an accepted ADR:**

- Changing its `status` (e.g., from Accepted to Deprecated or Superseded)
- Adding a link or note pointing to a newer ADR that supersedes it

**Not acceptable:**

- Rewriting the Context to reflect updated understanding
- Editing the Decision to match a revised approach
- Modifying Consequences based on what actually happened

If a decision turned out to be wrong, that is exactly what supersession is for.
The original ADR remains as a historical record of what was decided and why.

### docs/architecture.md is always current

Unlike ADRs, `docs/architecture.md` is a **living document**. It must always
reflect the current state of architectural decisions. When a new ADR is accepted
(or when an ADR supersedes an older one), `docs/architecture.md` must be updated
to reflect the latest decision. It is the single place a reader can go to
understand what is true *now*, without tracing a chain of ADRs.

## When to Use

- A specific decision has emerged from discussion in a problem doc
- You need to frame an upcoming decision with options (Undecided status)
- An ADR has been accepted and living documents need updating

Do NOT use for open-ended exploration -- that belongs in problem docs.

## The Rules

### One decision per ADR

Each ADR decides a single thing. If you find yourself writing "Additionally, we
decide..." or "We also require...", stop. That is a second ADR.

### Be concise

- **Context:** 1-3 short paragraphs. Link to problem docs for background
  instead of restating them.
- **Decision:** State the decision directly. A few paragraphs at most.
- **Consequences:** 3-5 bullet points. Each one sentence.
- **Total ADR length:** Aim for under 80 lines of content (excluding
  frontmatter). If you're over 100, you are probably deciding too many things
  or repeating context that already exists in problem docs.

| Section | Target | Anti-pattern |
|---------|--------|-------------|
| Context | 1-3 paragraphs | Restating entire problem docs |
| Options | 1 paragraph each | Multi-page analysis per option |
| Decision | Direct statement + brief rationale | Burying the decision in prose |
| Consequences | 3-5 one-sentence bullets | Essay-length explanations |

### Link, don't repeat

Problem docs exist. Architecture.md exists. Reference them:

```markdown
# Good
The threat model establishes least-privilege as a cross-cutting principle
(see [security-threat-model.md](../problems/security-threat-model.md)).

# Bad
[3 paragraphs restating the threat model's least-privilege section]
```

### Cross-reference other ADRs

If this decision builds on or relates to another ADR, say so in Context.

### Normative specs (`docs/normative/`)

- **Use** when the decision needs **byte-level or field-level** contracts, JSON
  Schema (or equivalent), **canonical YAML** or snapshots, or **compatibility
  / conformance** artifacts aimed at **multiple implementations** or automated
  checks.
- **Do not use** for open-ended trade-off exploration (that stays in **problem
  docs**), for a **single architectural choice** with no large artifact (keep
  that in the **ADR only**), or for duplicating
  [ADR 0003](../../docs/ADRs/0003-org-config-repo-convention.md)-style
  repository convention **without** a versioned contract (do not invent a
  normative subtree for that).
- **Relationship:** the **ADR** states the decision, high-level versioning
  expectations, and **links** to `docs/normative/<topic>/v<major>/...`; the
  normative folder holds the detailed, **versioned** contract (`v1`, `v2`, …).

## Checklist

Follow these steps in order:

1. **Find the next number.** List `docs/ADRs/` on current `main`, then scan
   **open pull requests** for new `docs/ADRs/NNNN-*.md` files so you do not
   collide with in-flight ADRs. Pick the lowest unused four-digit `NNNN`.
2. **Read the template.** Use `docs/ADRs/0000-adr-template.md` exactly.
3. **Fill in frontmatter.** `relates_to` must reference existing filenames
   (without `.md`) from `docs/problems/`. Use `"*"` only for ADRs that truly
   apply to all problem areas. The `status` in frontmatter must match the
   `## Status` heading in the body (the linter enforces this).
4. **Choose the right status.** Use **Proposed** for a draft awaiting
   discussion. Use **Undecided** when the problem is identified and options are
   described but no choice has been made -- Undecided ADRs must include an
   Options section. Use **Accepted** when the decision is made.
5. **Write the ADR.** Follow the conciseness rules above.
6. **Run linters.** Execute `make lint` and fix any errors before committing.
7. **If status is Accepted, update living documents** (see below).

## Updating Living Documents After Acceptance

When an ADR is accepted, the current-state documents must reflect the decision.

### docs/architecture.md

Add a "Decided:" line or short paragraph under the relevant component. Keep
the existing structure -- do NOT rewrite entire sections. Add a link to the ADR.
Remove or annotate open questions that the ADR resolves. Add new open questions
for consequences that surface new unknowns.

```markdown
## Agent Sandbox

[existing description unchanged]

**Decided:**

- Filesystem access model: ephemeral read-only source mounts with separate
  writable workspace ([ADR 0002](ADRs/0002-ephemeral-sandbox-filesystems.md)).

**Open questions:**

- [remaining unanswered questions]
- [any new questions raised by the ADR's consequences]
```

### Problem docs

If an ADR resolves an open question in a problem doc, annotate that question
with a link to the ADR. Do NOT delete the question -- mark it answered:

```markdown
- ~~How do agents access source code?~~ Decided in
  [ADR 0002](../ADRs/0002-ephemeral-sandbox-filesystems.md).
```

If the ADR partially answers a question, add a parenthetical:

```markdown
- How do we provide agents with resources? (Filesystem access decided in
  [ADR 0002](../ADRs/0002-ephemeral-sandbox-filesystems.md); tool and API
  access remain open.)
```

### What NOT to update

- Do not update documents unrelated to the ADR's `relates_to` problem areas.
- Do not rewrite sections. Make surgical additions.
- Do not change the tone or structure of existing prose.

## Red Flags -- Stop and Reconsider

- ADR is over 100 lines of content -- probably deciding too many things
- Context section restates information from problem docs at length
- You wrote "Additionally, we decide..." -- split into two ADRs
- You're rewriting a section of architecture.md -- make a surgical edit instead
- `relates_to` lists more than 3 problem docs -- the decision may be too broad
- You didn't run `make lint` -- stop and run it
- You're editing the Context, Decision, or Consequences of an accepted ADR --
  write a new superseding ADR instead

## Common Mistakes

| Mistake | Fix |
|---------|-----|
| Bundling multiple decisions | Split into separate ADRs |
| Verbose context | Link to problem docs |
| Forgetting frontmatter `relates_to` | Check template, list problem doc filenames |
| Not updating architecture.md | Follow the update checklist above |
| Rewriting existing doc sections | Make surgical additions only |
| Skipping linters | Run `make lint` before committing |
| Wrong ADR number | Check existing files in `docs/ADRs/` first |
| Editing an accepted ADR's content | Write a new ADR that supersedes it |
| Forgetting to update architecture.md | It must always reflect current decisions |
