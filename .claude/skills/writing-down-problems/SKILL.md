<!-- GENERATED FROM skills/writing-down-problems/SKILL.md — DO NOT EDIT -->
---
name: writing-down-problems
description: Write a new problem document for the fullsend design exploration repo
allowed-tools:
  - Read
  - Write
  - Edit
  - Grep
  - Glob
  - Bash
---

# Writing Down Problems

Write a new problem document for the fullsend repo, which explores fully autonomous agentic development for the konflux-ci GitHub organization.

The problem area topic is: **$ARGUMENTS**

## Before writing: research

Before writing anything, read the existing documents to understand context, style, and cross-references.

1. **Read CLAUDE.md** at the repo root — it describes the repo's conventions and key design decisions.

2. **Read README.md** — it lists and links all existing problem documents. You will need to add the new document here when done.

3. **Read at least 3-4 existing problem documents** in `docs/problems/` to internalize the style and structure. Good exemplars to start with:
   - `security-threat-model.md` — thorough threat analysis with ranked priorities and adversarial thinking
   - `intent-representation.md` — multiple approaches with clear trade-offs, strong adversarial analysis
   - `human-factors.md` — research-grounded, nuanced, avoids prescribing solutions
   - `agent-architecture.md` — clear problem framing, well-structured options, concrete interaction model

4. **Search for cross-references** — use grep to find which existing documents mention topics related to your problem area. Your document should cross-reference related problem areas where the topics intersect.

## Document structure patterns

The existing documents follow a consistent (but not rigid) structure. Use these patterns as guidance:

### Title and opening

- Start with a level-1 heading (`# Problem Area Name`)
- Follow immediately with a one-to-two sentence framing of the problem — what question this document is trying to answer
- The opening should make the scope clear without being overly formal

### Problem framing

- Explain *why* this is hard or interesting before jumping to approaches
- Ground the problem in the konflux-ci context — what makes this relevant to this specific organization and its goals
- Use concrete examples to make abstract problems tangible

### Approaches and options

- Present multiple approaches, options, or perspectives — never prescribe a single solution
- For each approach, discuss both pros and cons (or trade-offs)
- Use clear structure: named options, tables comparing dimensions, or explicit "Pros / Cons" lists
- It's fine to indicate which direction seems most promising, but frame it as a recommendation with reasoning, not a decree

### Adversarial and security analysis

- Consider how the problem area interacts with the security threat model (threat priority: external prompt injection > insider/compromised creds > agent drift > supply chain)
- Think about how proposed approaches could be attacked, gamed, or subverted
- This applies even when the document isn't primarily about security — every problem area has security implications in an agentic system

### Cross-references

- Link to related problem documents where topics intersect
- Many existing documents include a "Relationship to other problem areas" section (usually near the end, before "Open questions") with bullet points explaining how the topic connects to other problem domains
- Use relative links: `[security-threat-model.md](security-threat-model.md)` or `[human-factors.md](human-factors.md)`

### Open questions

- End with an "Open questions" section (`## Open questions`)
- Include genuine unresolved questions — things you don't know the answer to
- Questions should be specific enough to drive future exploration or experimentation
- Avoid rhetorical questions or questions with obvious answers
- Aim for 4-10 questions

## Tone and style

- **Design exploration, not specification.** Use language that invites exploration: "could," "might," "one approach" rather than "must," "will," "the solution is"
- **Accessible language.** The audience is the konflux-ci contributor community — not academics, not a single team. Explain technical terms. Use concrete examples.
- **Don't presume solutions.** Present the problem space and options. Let the reader form their own conclusions.
- **No frontmatter.** Existing problem documents don't use YAML frontmatter — they start directly with the markdown heading.
- **Referencing research.** When citing research or evidence, include links and brief context (see how `human-factors.md` cites automation research).

## After writing

1. **Add the document to README.md.** Find the problem documents list in README.md and add a line for the new document, following the existing format:
   ```
   - [Problem Title](docs/problems/filename.md) — Brief description of what this document covers
   ```
   Place it in a logical position relative to the existing entries (alphabetical or thematic grouping — match the existing order).

2. **Verify cross-references.** Confirm that any documents you linked to actually exist at the paths you referenced.

3. **Self-review.** Before finishing, check your document against these criteria:
   - Does it present multiple options with trade-offs (not prescribe a single solution)?
   - Does it have an "Open questions" section with genuine unresolved questions?
   - Does it consider adversarial/security implications?
   - Is the language accessible?
   - Does it cross-reference related problem areas?
   - Is it linked from README.md?
   - Does it avoid presuming solutions?
