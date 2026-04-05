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

## When to Use

- A specific decision has emerged from discussion in a problem doc
- You need to frame an upcoming decision (Undecided status)
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
| Options (if any) | 1 paragraph each | Multi-page analysis per option |
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

## Checklist

Follow these steps in order:

1. **Find the next number.** List `docs/ADRs/` and pick the next sequential
   four-digit number.
2. **Read the template.** Use `docs/ADRs/0000-adr-template.md` exactly.
3. **Fill in frontmatter.** `relates_to` must reference existing filenames
   (without `.md`) from `docs/problems/`. Use `"*"` only for ADRs that truly
   apply to all problem areas. The `status` in frontmatter must match the
   `## Status` heading in the body (the linter enforces this).
4. **Choose the right status.** Use **Proposed** for a draft awaiting
   discussion. Use **Undecided** when the problem is identified but no choice
   has been made. Use **Accepted** when the decision is made. Include an
   Options section only when there are genuine alternatives worth documenting;
   if the decision is obvious, just decide it.
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
