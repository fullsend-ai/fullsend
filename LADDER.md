# Contributor Ladder

This document defines the path from first-time contributor to
maintainer. Each tier grants additional capabilities and carries
additional expectations. Advancement is not automatic — it requires a
request, a track record, and maintainer approval.

For the maintainer role specifically — including eligibility, the
nomination process, and what maintainers do — see
[MAINTAINERS.md](MAINTAINERS.md).

## Tiers

### 1. Vouched

**What it means:** You have been approved to submit pull requests.

**What it unlocks:**

- PRs are no longer auto-closed by the vouch gate
- You can contribute code, docs, and tests

**Criteria:**

- Describe what you want to work on and why
- Write the request yourself (not AI-generated)

**How to request:** Open a
[Vouch Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=vouch-request)
discussion. A maintainer will review and comment `/vouch` if approved.

**Who approves:** Any single maintainer.

**Duration:** Permanent. A maintainer may revoke a vouch if the
contributor violates project norms (e.g., submitting AI-generated vouch
requests, repeated low-quality contributions).

### 2. Triage

**What it means:** You have GitHub `triage` collaborator access on the
repository.

**What it unlocks:**

- `/fs-triage` and `/fs-review` slash commands (observation agents)
- Ability to label, close, and reopen issues
- Automatic e2e CI triggering on your PRs (no maintainer label needed)
- Bypassing the vouch gate

**Criteria:**

- Already vouched
- At least 2 merged PRs or substantive reviews demonstrating
  familiarity with the project's conventions and review etiquette
- Responsive to feedback on your own PRs

**How to request:** Open a
[Role Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=role-request)
discussion. Title it "Role request: triage — \<your GitHub handle\>".
Link to your merged PRs or reviews.

**Who approves:** Any single maintainer.

**Duration:** Permanent unless revoked. Any maintainer may revoke access
if the contributor becomes inactive for an extended period or misuses
the role.

### 3. Write

**What it means:** You have GitHub `write` collaborator access on the
repository.

**What it unlocks:**

- `/fs-code` and `/fs-fix` slash commands (mutation agents)
- Push access to non-protected branches
- Bypassing the vouch gate

**Criteria:**

- Already at the triage tier
- Sustained contribution history: multiple merged PRs across different
  areas of the codebase, or deep expertise in a specific subsystem
- Demonstrated understanding of project conventions (commit messages,
  PR workflow, review etiquette)
- Trusted not to push directly to protected branches or bypass review

**How to request:** Open a
[Role Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=role-request)
discussion. Title it "Role request: write — \<your GitHub handle\>".
Link to your contribution history.

**Who approves:** Two maintainers must approve. A single maintainer
can request more discussion but cannot unilaterally grant write access.

**Duration:** Permanent unless revoked. Any maintainer may revoke
access if the contributor becomes inactive or misuses the role.

### 4. Maintainer

**What it means:** You have merge and approval rights and are a member
of the [@fullsend-ai/core](https://github.com/orgs/fullsend-ai/teams/core)
team.

**What it unlocks:**

- CODEOWNERS approval required to merge PRs
- Authorizing CI runs on PRs from non-maintainers
- Issue triage and backlog management
- All capabilities from previous tiers

**Criteria and process:** See [MAINTAINERS.md](MAINTAINERS.md) for the
full eligibility criteria, nomination process, and time commitment.

## Requesting a role

All role changes use **GitHub Discussions** as the request mechanism:

| Tier | Discussion category | Approval |
|------|---------------------|----------|
| Vouched | [Vouch Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=vouch-request) | 1 maintainer |
| Triage | [Role Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=role-request) | 1 maintainer |
| Write | [Role Request](https://github.com/fullsend-ai/fullsend/discussions/new?category=role-request) | 2 maintainers |
| Maintainer | Issue (see [MAINTAINERS.md](MAINTAINERS.md)) | Maintainer consensus |

## Prow/OWNERS repositories

Some repositories use Prow and `OWNERS` files as their primary
authority model. Contributors with approval rights via `OWNERS` may
only have `read` access at the GitHub collaborator level. Because
fullsend checks GitHub roles exclusively, slash commands from these
contributors fail silently — even though they are trusted approvers in
the Prow model. If you are in this situation, request the appropriate
tier through the process above so your GitHub collaborator role matches
your actual trust level.

## Revoking access

Any maintainer may revoke a collaborator's access at any tier. Common
reasons include:

- Extended inactivity (no contributions or reviews for 6+ months)
- Misuse of granted permissions
- Violation of project norms or [contributing guidelines](CONTRIBUTING.md)

Revocation is not punitive — it reflects current activity level. A
contributor whose access is revoked for inactivity can re-request it
through the normal process.
