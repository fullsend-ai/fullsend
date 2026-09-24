---
title: Problem Documents
---

# Problem Documents

These rules apply when adding or editing files under `docs/problems/`. The docs site auto-discovers that directory.

**Audience.** Write for any contributor community considering autonomous agents. Keep language accessible and do not presume a particular solution.

**Shape.** Present multiple options with trade-offs; do not prescribe a single solution. Every problem document has an "Open questions" section — that is where unresolved issues live.

**Organization-agnostic core.** Keep core problem documents organization-agnostic. Organization-specific details belong in `docs/problems/applied/<org-name>/`.

**Threat model.** The [security threat model](../problems/security-threat-model.md) (priority: external injection > insider > drift > supply chain) should inform all other documents.

**Bidirectional backlinks.** For each existing problem doc that a new problem doc links to, add a reciprocal contextual backlink at the passage that motivated the outbound link, in the same PR. Then search `docs/problems/` for the concept, adjacent terms, synonyms, and the new doc's title words. Wherever an existing doc *substantively discusses* the concept (not merely mentions it in passing), add a single contextual backlink at that passage — not a link at every match.

When the new doc is organization-specific (`docs/problems/applied/<org-name>/`), frame any backlink you add to a core problem doc as an explicit org-specific pointer so core docs stay organization-agnostic.
