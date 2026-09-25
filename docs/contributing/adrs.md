# Architecture Decision Records (ADRs)

These rules apply whenever you touch `docs/ADRs/` or review a PR that does. Full authoring guidance is in [`skills/writing-adrs/SKILL.md`](../../skills/writing-adrs/SKILL.md); invoke that skill when writing a new ADR.

**Immutability:** Once an ADR on `main` has status **Accepted**, it is a point-in-time record. Do not substantially rewrite its Context, Decision, or Consequences sections. When circumstances change, write a **new** ADR that supersedes the old one. Minor annotations are welcome: cross-references to related ADRs, short notes linking to newer decisions, typo and broken-link fixes, and status changes (e.g., to Deprecated or Superseded). Call out any edits to accepted ADRs in the PR description.

**New ADRs in pull requests:** Approval happens at **merge**, not when the branch is created. If the decision is made, set status to **Accepted** in the ADR you are proposing — not a lesser status merely because the PR is open. Valid statuses are **Accepted**, **Deprecated**, and **Superseded**. When status is Accepted, update `docs/architecture.md` and related problem docs in the same PR per the writing-adrs skill. When editing an ADR that has not yet merged to `main`, change the content directly — do not add "Revised" annotations, revision dates, or revision history sections. The ADR is still being authored; treat edits as normal authoring, not post-acceptance amendments.

**When reviewing PRs:** Flag substantial rewrites to Context, Decision, or Consequences on Accepted ADRs already on `main` as a policy violation. Allow minor annotations (cross-references, short notes, typo fixes), status updates, and supersession links. For brand-new ADR files on the PR branch, evaluate whether the recorded decision matches the diff — do not treat **Accepted** on a new file as a mistake if the ADR is ready for human review at merge. For new or edited `## References` entries that point at another ADR, require the canonical format below. Do not flag neighboring older entries that use another style, and do not require rewriting them.

## References section format

When adding or editing a `## References` entry that points at another ADR, use this form exactly:

```markdown
- [ADR 0007 — Per-role GitHub Apps with manifest-based creation](0007-per-role-github-apps.md)
```

Rules:

1. **Label.** `ADR NNNN — ` (four-digit number, space, em dash `—`, space) followed by the target ADR's frontmatter `title` with the leading `N. ` prefix removed. Match that title substring character-for-character: no truncation, no paraphrasing, no case changes.
2. **Target.** A relative path to the ADR markdown file.
3. **No trailing text.** Do not append ` — description` or other annotation after the link.

This format applies to entries you add or edit. Do not rewrite existing entries that use another style (colon in the label, unlinked text, trailing rationale) solely to match it.

The `lint-adr-frontmatter` pre-commit hook rejects `[ADR NNNN — …]` labels whose title text does not match the target's frontmatter title.
