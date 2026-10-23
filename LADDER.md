# Contributor Ladder

This document defines the path from first-time contributor to
maintainer. Each role grants additional capabilities and carries
additional expectations. Advancement is not automatic — it requires a
request, a track record, and maintainer approval.

For the Maintainer role specifically — including eligibility, the
nomination process, and what maintainers do — see
[MAINTAINERS.md](MAINTAINERS.md).

**A good place to start is reviewing existing pull requests.** Reviewing
teaches you the codebase, the project's direction, and its conventions,
and it gives contributors and agents useful feedback. Substantive
reviews count toward the Triage role on equal footing with merged PRs,
so you do not need a merged PR of your own to advance. See
[Demonstrating readiness](#demonstrating-readiness) for what reviews
and other contribution evidence should look like.

## Roles

### Vouched

**What it means:** You have been approved to submit pull requests from
your own fork, including work produced manually or with tools outside
Fullsend.

**What it unlocks:**

- PRs are no longer auto-closed by the vouch gate
- You can contribute code, docs, and tests

**Criteria:**

- Describe what you want to work on and why
- Write the request yourself (not AI-generated)

**How to check:** Your GitHub username is listed in
[`.github/VOUCHED.td`](https://github.com/fullsend-ai/fullsend/blob/vouched/.github/VOUCHED.td)
on the `vouched` branch.

**How to request:** Open a
[Vouch Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=vouch-request)
discussion. A maintainer will review and comment `/vouch` if approved.

**Who approves:** Any single maintainer.

### Triage

**What it means:** You have GitHub `triage` collaborator access on the
repository.

**What it unlocks:**

- `/fs-triage` and `/fs-review` slash commands (observation agents)
- Ability to label, close, and reopen issues. A triage user can apply
  labels such as `ready-to-code`, but that label only dispatches the code
  agent when applied by a user with `write` access or by an agent bot.
  The same applies to `ok-to-test`: CI removes it when a triage user
  applies it, and it does not authorize e2e runs in this repository.
  Ask someone with the Write role to apply it.
- Triage contributors do not receive automatic e2e CI runs. Your PRs
  need an `ok-to-test` label from someone with the Write role.

**Criteria:**

- Already vouched
- At least 2 [substantive reviews](#substantive-reviews) or merged PRs,
  in any combination, that are high quality. For example, two substantive
  reviews, one review and one merged PR, or two merged PRs all meet
  the minimum. Merged PRs include
  [agent-authored PRs you drove](#agent-authored-prs-and-assignment).
- Responsive to feedback on your contributions — on changes you drove
  and in review discussions you took part in

**How to check:** You are a member of
[@fullsend-ai/triagers](https://github.com/orgs/fullsend-ai/teams/triagers).

**How to request:** Open a
[Triage Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=triage-request)
discussion. Link to your substantive reviews or merged PRs, and briefly
describe the work and judgment each one shows (see
[Making your case](#making-your-case)).

You do not need to be an expert in the codebase or community conventions
to request Triage. If you think your contributions demonstrate
readiness, open a request and describe what you did. Maintainers will
assess the evidence and explain any additional experience needed.

**Who approves:** Any single maintainer.

### Write

**What it means:** You have GitHub `write` collaborator access on the
repository.

**What it unlocks:**

- `/fs-code` and `/fs-fix` slash commands (mutation agents)
- `/fs-retro` and `/fs-prioritize` slash commands (analysis agents
  that require write-level trust)
- Push access to non-protected branches
- Authorizing e2e CI on PRs from authors without write access by
  applying the `ok-to-test` label after reviewing the latest push

**Criteria:**

- Already at the Triage role
- Sustained contribution history: multiple merged PRs across different
  areas of the codebase, or
  [deep expertise in a specific subsystem](#subsystem-expertise)
- Demonstrated understanding of project conventions (commit messages,
  PR workflow, review etiquette)
- Trusted not to push directly to protected branches or bypass review

**How to check:** You are a member of
[@fullsend-ai/writers](https://github.com/orgs/fullsend-ai/teams/writers).

**How to request:** Open a
[Write Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=write-request)
discussion. Link to your contribution history and describe the work
and judgment it demonstrates (see [Making your case](#making-your-case)).

**Who approves:** Two maintainers must approve. A single maintainer
can request more discussion but cannot unilaterally grant write access.

### Maintainer

**What it means:** You have merge and approval rights and are a member
of the [@fullsend-ai/core](https://github.com/orgs/fullsend-ai/teams/core)
team. You are expected to be an active regular participant in the
project and to help review contributions you are not driving.

**What it unlocks:**

- CODEOWNERS approval required to merge PRs
- Issue triage and backlog management
- All capabilities from previous roles

**Criteria and process:** See [MAINTAINERS.md](MAINTAINERS.md) for the
full eligibility criteria, nomination process, and time commitment.

## Demonstrating readiness

Advancement is a human assessment of demonstrated trust and readiness.
The minimums above are a starting point for that assessment, not a
threshold that grants access automatically. Maintainers look at what
your contributions show about your judgment, not only at how many there
are.

### Substantive reviews

A substantive review shows that you examined a change and formed your
own judgment about it. Examples of what a substantive review can
include:

- Checking whether the change fits the project's direction, existing
  design decisions, and ADRs
- Identifying correctness problems, edge cases, or security issues
- Validating behavior — running the change, reproducing the original
  problem, or confirming the fix works
- Assessing whether the tests cover the change and would catch a
  regression
- Following your findings through to resolution — responding to the
  author, re-checking the updated change, and confirming the problem is
  addressed

A review does not need to find problems to be substantive; a careful
approval that explains what you checked also counts. A bare approval
with no evidence of examination, or an agent-generated review posted
without your own scrutiny, does not demonstrate the same judgment.

### Agent-authored PRs and assignment

Many changes in this project are opened by agents rather than by a
person. The bot identity on a PR does not determine whose contribution
it is. When you link an agent-authored PR, describe your role in it —
for example, how you shaped the issue or the solution, validated the
behavior, reviewed the changes, or resolved review feedback.

GitHub assignment helps coordinate work, but it does not determine
whether your contribution counts. If you contributed to a PR or issue
you were not assigned to, link it and describe what you did.

Distinguish between driving a change and independently reviewing it:

- **Driving a change** means you were responsible for getting it done —
  for example, you filed or refined the issue, steered the
  implementation, and iterated on feedback. A change you
  drove counts like a PR you authored, whether the commits came from
  you or from an agent.
- **Independently reviewing a change** means you evaluated work you did
  not drive. Your review of a change you drove is not an independent
  review.

### Subsystem expertise

Deep expertise in a specific subsystem means understanding its
responsibilities, design, interactions with other parts of the system,
tradeoffs, and security boundaries — well enough to evaluate changes to
it and explain why it works the way it does. Evidence can include:

- Substantive reviews of changes to that subsystem
- Design discussions in issues, PRs, or problem documents
- [ADR](docs/ADRs/) contributions, as an author or a substantive
  reviewer
- Changes you drove in that subsystem

### Making your case

A role request should make it easy for maintainers to see what you did.
Include the role you are requesting, links to the relevant
contributions, and a brief explanation, in your own words, of the work
and judgment each one shows. For example:

```text
Requested role: Triage

Contributions:
- Review of https://github.com/fullsend-ai/fullsend/pull/NNN: I ran
  the changed command against a test repository, found that it
  ignored the --dry-run flag, and confirmed the fix in the follow-up
  commit.
- https://github.com/fullsend-ai/fullsend/pull/NNN (opened by the code
  agent, assigned to someone else): I refined the issue, pointed out
  that the first attempt changed a config field without updating the
  reference docs, and verified the final behavior before it merged.

Summary: These show that I can validate behavior, catch gaps between
code and documentation, and follow feedback through to resolution.
```

## Requesting a role

Role changes use the following request mechanisms:

| Role | Request mechanism | Approval |
|------|-------------------|----------|
| Vouched | [Vouch Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=vouch-request) | 1 Maintainer |
| Triage | [Triage Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=triage-request) | 1 Maintainer |
| Write | [Write Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=write-request) | 2 Maintainers |
| Maintainer | Issue (see [MAINTAINERS.md](MAINTAINERS.md)) | Maintainer consensus |

## Revoking access

Any maintainer may revoke a collaborator's access at any role level. Common
reasons include:

- Extended inactivity (no contributions or reviews for 6+ months)
- Misuse of granted permissions
- Violation of project norms or [contributing guidelines](CONTRIBUTING.md)

Revocation is not punitive — it reflects current activity level. A
contributor whose access is revoked for inactivity can re-request it
through the normal process.
