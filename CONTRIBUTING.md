# Contributing to Fullsend

Thank you for your interest in contributing! This document covers the social norms and processes we follow. For where to place your contribution (problem docs, ADRs, etc.), see the [README](README.md#how-to-contribute).

## Commit messages

This project uses [Conventional Commits](https://www.conventionalcommits.org/). See [COMMITS.md](COMMITS.md) for the full specification, type selection rules, and examples.

## DCO (Developer Certificate of Origin)

This project uses the [Probot DCO app](https://github.com/apps/dco) to enforce sign-off on commits. Add `Signed-off-by` to your commits with `git commit -s`.

**Human-driven agent sessions** (e.g., using Claude Code locally) should sign off — the human directing the session is the one certifying the DCO, just as they would for any other commit.

**Autonomous agent commits are exempt.** The fullsend code and fix agents run without a human in the loop at commit time. The DCO is a human attestation — it certifies personhood and legal authority to contribute. No one is present to make that certification in an autonomous session. These agents commit using the GitHub App's bot identity (`<id>+<slug>[bot]@users.noreply.github.com`), which GitHub recognizes as `author.type: "Bot"`. The Probot DCO app auto-skips bot-authored commits. The human who merges an agent PR accepts responsibility for the contribution.

## Pull request workflow

### Opening a PR

- Run `make lint` before pushing and fix any failures.
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

## Issues

When in doubt about whether something warrants a PR, start with an issue. Issues are low-friction and can graduate into PRs, problem docs, or ADRs later.

To find open issues for human contribution, use the [contributor issue search](https://github.com/fullsend-ai/fullsend/issues?q=is%3Aissue%20is%3Aopen%20-author%3Aapp%2Ffullsend-ai-fullsend%20-author%3Aapp%2Ffullsend-ai-triage%20-author%3Aapp%2Ffullsend-ai-review%20-author%3Aapp%2Ffullsend-ai-prioritize%20-author%3Aapp%2Ffullsend-ai-coder%20-author%3Aapp%2Ffullsend-ai-retro%20-label%3Aready-to-code). This search excludes issues reserved for agents.

## License

All contributions to this project are made under the [Apache License, Version 2.0](LICENSE). By submitting a pull request, you agree that your contributions will be licensed under this license.
