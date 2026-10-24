# Architecture Decision Records (ADRs)

These rules apply whenever you touch `docs/ADRs/` or review a PR that does. Full authoring guidance is in [`skills/writing-adrs/SKILL.md`](../../skills/writing-adrs/SKILL.md); invoke that skill when writing a new ADR.

**Immutability:** Once an ADR on `main` has status **Accepted**, it is a point-in-time record. Do not substantially rewrite its Context, Decision, or Consequences sections. When circumstances change, write a **new** ADR that supersedes the old one. Minor annotations are welcome: cross-references to related ADRs, short notes linking to newer decisions, typo and broken-link fixes, and status changes (e.g., to Deprecated or Superseded). Call out any edits to accepted ADRs in the PR description.

**New ADRs in pull requests:** Approval happens at **merge**, not when the branch is created. If the decision is made, set status to **Accepted** in the ADR you are proposing — not a lesser status merely because the PR is open. Valid statuses are **Accepted**, **Deprecated**, and **Superseded**. When status is Accepted, update `docs/architecture.md` and related problem docs in the same PR per the writing-adrs skill. When editing an ADR that has not yet merged to `main`, change the content directly — do not add "Revised" annotations, revision dates, or revision history sections. The ADR is still being authored; treat edits as normal authoring, not post-acceptance amendments.

**Keep implementation out of ADR pull requests:** The recommendation is that
implementation does **not** accompany an ADR. An ADR PR carries the decision
record and the living-document updates. Track implementation in follow-up
issues instead. The exception is implementation that is really short and
trivial, which will rarely be the case. When in doubt, split the
implementation into its own PR.

**When reviewing PRs:** Flag substantial rewrites to Context, Decision, or
Consequences on Accepted ADRs already on `main` as a policy violation. Allow
minor annotations (cross-references, short notes, typo fixes), status updates,
and supersession links. For brand-new ADR files on the PR branch, evaluate
whether the recorded decision matches the diff — do not treat **Accepted** on a
new file as a mistake if the ADR is ready for human review at merge. Also flag
in new ADRs:

- A missing or implicit **Forge scope**: GitHub-only discussion that does not
  say whether GitLab and Forgejo are covered, excluded (with a reason or
  tracking issue), or unaffected.
- Claims about current behavior that are uncited, cite only `main` (not a
  commit SHA), or do not match the code at the cited SHA.
