# Contributing to Fullsend

Thank you for your interest in contributing! This document covers the social norms and processes we follow.

## First-Time Contributors

This project uses a **vouch system**. AI tools make it trivial to generate plausible-looking but low-quality contributions, so we require first-time contributors to be vouched by a maintainer before submitting pull requests.

1. Open a [Vouch Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=vouch-request) discussion.
2. Describe what you want to change and why.
3. Write in your own words — do not have an AI generate the request. Requests that read like LLM output will be denied.
4. A maintainer will comment `/vouch` if approved.
5. Once vouched, you can submit pull requests.

**If you are not vouched, any pull request you open will be automatically closed.** Org members and collaborators with write access bypass this check.

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/). See [COMMITS.md](COMMITS.md) for the full specification, type selection rules, and examples.

## DCO (Developer Certificate of Origin)

This project uses the [Probot DCO app](https://github.com/apps/dco) to enforce sign-off on commits. Add `Signed-off-by` to your commits with `git commit -s`.

**Human-driven agent sessions** (e.g., using Claude Code locally) should sign off — the human directing the session is the one certifying the DCO, just as they would for any other commit.

**Autonomous agent commits are exempt.** The fullsend code and fix agents run without a human in the loop at commit time. The DCO is a human attestation — it certifies personhood and legal authority to contribute. No one is present to make that certification in an autonomous session. These agents commit using the GitHub App's bot identity (`<id>+<slug>[bot]@users.noreply.github.com`), which GitHub recognizes as `author.type: "Bot"`. The Probot DCO app auto-skips bot-authored commits. The human who merges an agent PR accepts responsibility for the contribution.

## Pull request workflow

### Opening a PR

- Stage your changes, then run `make lint` before pushing and fix any failures.
- For Go changes, run `make go-test` and add tests for new or modified logic. CI uploads coverage to Codecov and enforces the thresholds in [`.codecov.yml`](.codecov.yml): **80% patch coverage** on changed lines (5% tolerance) and **no more than 1% drop** in overall project coverage relative to the base branch.
- **If your PR introduces a breaking change**, the PR title must carry the `!` suffix (e.g., `feat(harness)!: require role field`). GoReleaser builds release notes from PR titles — a missing `!` means users get no warning before upgrading. See [COMMITS.md](COMMITS.md#breaking-changes) for how to identify breaking changes and what to include in the commit body.
- Keep PRs focused. One problem area or decision per PR is easier to review than a grab-bag.
- If your change touches a problem doc, make sure the "Open questions" section still makes sense after your edit.

### Review etiquette

- **Comment resolution belongs to the PR author.** When a reviewer leaves a comment, the PR author is free to address the feedback and resolve the conversation themselves. This keeps the review cycle moving.
- **If you need to block a PR on your feedback, use "Request changes."** A comment alone is advisory — the author may resolve it at their discretion. The "Request changes" review status is how a reviewer signals that the PR should not merge until their concern is addressed. This is the only mechanism for enforcing your review.
- **Be constructive.** This is a design exploration — disagreement is expected and valuable. Critique ideas, not people. When you push back on a proposal, suggest an alternative or explain what concern drives your objection.

### Reworking a PR

When a PR needs a significant change in approach — not just addressing review feedback, but rethinking the implementation or design — close the existing PR with a comment explaining why, and open a new one. Link the new PR to the old one for historical continuity. This is preferred over force-pushing because:

- Reviewers see a fresh PR in their queue instead of missing that the content changed completely.
- The closed PR preserves the original discussion and the reasoning behind the pivot.
- Metrics can track rework cycles accurately.

Small adjustments in response to review feedback are normal iteration — this guideline applies when the underlying approach changes.

### Merging

- PRs require approval from a [CODEOWNERS](CODEOWNERS) member before merging.

## Working with ADRs

ADRs (Architecture Decision Records) are **point-in-time records**. Once accepted, do not substantially rewrite their Context, Decision, or Consequences sections — if a decision needs to change, write a new ADR that supersedes the old one. Minor annotations are welcome: cross-references to related ADRs, short notes linking to newer decisions, and typo fixes. See the [ADR template](docs/ADRs/0000-adr-template.md) and [ADR 0001](docs/ADRs/0001-use-adrs-for-decision-making.md) for full details.

### ADRs and implementation code

Human contributors may include an ADR and its implementation in the same PR when it makes sense. Bundling helps reviewers see what a decision actually means in code and avoids an extra review cycle. Use your judgment based on two factors:

- **PR size.** If adding the implementation would make the PR excessively large, submit the ADR first and follow up with implementation.
- **Rewrite risk.** If the ADR discussion is likely to change direction — causing significant implementation rework — submit the ADR on its own. Get alignment on the decision before writing the code.

**Autonomous agents should always submit ADRs and implementation as separate PRs.** The ADR should be merged first, then a separate issue drives the implementation. This keeps agent-produced PRs focused and independently reviewable.

### ADR numbering

ADR filenames use a four-digit number (`NNNN-short-description.md`). When multiple PRs add ADRs concurrently, number collisions can happen. Before merging, use the `/renumber-adr` skill to check whether your ADR number is still available on the target branch and renumber if needed.

## Deprecation notices in documentation

When a feature, field, command, or workflow is deprecated, label it clearly in
the user-facing documentation so readers can distinguish current functionality
from deprecated functionality. Use the following conventions consistently.

### Blockquote notice (preferred for sections)

Place a blockquote at the top of the section that describes the deprecated
feature. Use this format:

```markdown
> **Deprecated:** `<feature>` is deprecated per
> [ADR-NNNN](path/to/adr.md). Use `<replacement>` instead.
> <Migration guidance — one or two sentences explaining how to migrate.>
```

Key elements:
- Start with **`> **Deprecated:**`** (bold, followed by a colon).
- Name the deprecated feature explicitly.
- Link to the ADR or issue that made the decision.
- Describe the replacement and how to migrate.

### Inline annotation (for field references and tables)

When a deprecated item appears in a table, code block, or field reference,
add a short inline annotation:

```yaml
runner_env:    # ⚠ Deprecated: use env.runner instead (ADR 0055)
```

```markdown
| `env`, `runner_env` (deprecated) | Merged; child keys win |
```

### Sidebar or guide index entry

When an entire guide page is deprecated, annotate the link in the index:

```markdown
- [Building custom agents](path) — _(deprecated — see [Replacement](path))_
```

### What to include

Every deprecation notice should answer three questions:
1. **What** is deprecated?
2. **What replaces it?** (link to the replacement feature or guide)
3. **How do I migrate?** (command, config change, or link to migration docs)

If there is no replacement yet, say so explicitly (e.g., "removal is planned
for a future release; no migration is needed").

### When to add notices

- When an ADR deprecates a feature, update the affected user-facing docs in
  the same PR or a follow-up.
- When touching a doc page that references a deprecated feature without a
  notice, add one.
- Do not remove deprecated content from docs — label it so users on older
  versions can still find the reference material.

## Building from source

```bash
make go-build
```

The binary is written to `./bin/fullsend`. To run agents locally with the built binary, see [Running agents locally](docs/guides/user/running-agents-locally.md).

## Issues

When in doubt about whether something warrants a PR, start with an issue. Issues are low-friction and can graduate into PRs, problem docs, or ADRs later.

To find open issues for human contribution, use the [contributor issue search](https://github.com/fullsend-ai/fullsend/issues?q=is%3Aissue%20is%3Aopen%20-author%3Aapp%2Ffullsend-ai-fullsend%20-author%3Aapp%2Ffullsend-ai-triage%20-author%3Aapp%2Ffullsend-ai-review%20-author%3Aapp%2Ffullsend-ai-prioritize%20-author%3Aapp%2Ffullsend-ai-coder%20-author%3Aapp%2Ffullsend-ai-retro%20-label%3Aready-to-code). This search excludes issues reserved for agents.

## License

All contributions to this project are made under the [Apache License, Version 2.0](LICENSE). By submitting a pull request, you agree that your contributions will be licensed under this license.
