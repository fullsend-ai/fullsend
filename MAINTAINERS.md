# Becoming a Maintainer

Maintainers have merge and approval rights on this repository. They are
members of the [@fullsend-ai/maintainers](https://github.com/orgs/fullsend-ai/teams/maintainers)
team referenced in [CODEOWNERS](CODEOWNERS).

Maintainer is the final role on the [contributor ladder](LADDER.md).
See that document for the full path from first-time contributor to
Maintainer, including the intermediate roles (Vouched, Triage, Write).

## What maintainers do that contributors do not

Contributors submit pull requests and participate in discussions.
Maintainers carry additional responsibilities:

- **Review and approve PRs.** Maintainers provide the CODEOWNERS approval
  required to merge. This means understanding not just whether code works,
  but whether it fits the project's direction and conventions.
- **Triage issues.** Help prioritize incoming issues, label them, and
  identify duplicates.
- **Maintain project health.** Keep the backlog healthy, close stale
  issues, and ensure CI stays green.
- **Uphold standards.** Enforce the project's
  [contributing guidelines](CONTRIBUTING.md),
  [commit conventions](COMMITS.md), and review etiquette.

## Eligibility

You should have a demonstrated track record of contributions — merged
PRs, substantive PR reviews, or both — in
[this repository](https://github.com/fullsend-ai/fullsend) or other
public repositories of the
[fullsend-ai](https://github.com/fullsend-ai) organization. At least
one merged change or substantive review must be in this repository.
Drive-by fixes are appreciated but do not on their own establish the
familiarity needed for a maintainer role.

## Process

1. **File a nomination issue** using the
   [Maintainer Nomination](https://github.com/fullsend-ai/org/issues/new?template=maintainer-request.yml)
   template in [fullsend-ai/org](https://github.com/fullsend-ai/org).
   In the issue, link to merged PRs you have authored or
   substantive reviews you have provided that demonstrate your
   familiarity with the project.
2. **Current maintainers discuss.** The maintainers will review your
   contributions and reach a group consensus. Approval is not a single
   maintainer's decision — it requires agreement among the existing
   maintainer team.
3. **Decision within one week.** A maintainer will follow up on the
   issue with the outcome. If approved, you will be added to the
   `@fullsend-ai/maintainers` team and gain merge permissions.

## Time commitment

Maintainership is a commitment to ongoing participation, not a one-time
title. While there is no fixed hour-per-week requirement, maintainers
are expected to remain active — reviewing PRs, responding to issues, and
staying current with project direction. If you are unable to participate
for an extended period, let the team know so coverage can be adjusted.

## Inactivity and removal

Maintainer permissions are elevated access. Permissions held by people
who no longer take part in the project add security risk and leave the
list of code owners out of date. This section describes how inactive
maintainers are removed. Removal for inactivity is not punitive.

### Inactivity threshold

A maintainer is considered inactive after **6 months** with no
maintainer activity in any public repository of the
[fullsend-ai](https://github.com/fullsend-ai) organization. Maintainer
activity includes:

- Reviewing pull requests
- Authoring or driving merged changes
- Triaging issues (labeling, commenting, closing duplicates)
- Taking part in issue, pull request, or discussion threads about
  project direction

This matches the inactivity reason for revoking access in the
[contributor ladder](LADDER.md#revoking-access).

A planned leave announced to the team in advance does not count toward
the threshold. Tell the team when the leave starts and roughly when you
expect to return.

### Prior notification

A maintainer is never removed for inactivity without notice first. Any
maintainer who notices that another maintainer has passed the threshold
may start the process:

1. **Open a notification issue** in
   [fullsend-ai/org](https://github.com/fullsend-ai/org). Mention the
   inactive maintainer by GitHub username, state the date of their last
   maintainer activity, and link to this section.
2. **Contact the maintainer directly** as well, through any channel the
   team normally uses to reach them, and note in the issue that you did.

### Grace period

The maintainer has **30 days** from the date of the notification issue
to respond on it. During the grace period, the maintainer can:

- Confirm that they intend to stay active. The issue is closed and no
  change is made.
- Declare a planned leave, with an expected return date. The issue is
  closed and no change is made.
- Step down voluntarily, as described in [Stepping down](#stepping-down).

If the maintainer does not respond within the grace period, the
removal goes ahead.

### Removal

Removal uses the same GitHub team structure as onboarding:

1. A maintainer opens a pull request against
   [peribolos.yaml](https://github.com/fullsend-ai/org/blob/main/peribolos.yaml)
   in [fullsend-ai/org](https://github.com/fullsend-ai/org) that removes
   the username from the maintainer team. The pull request links the
   notification issue.
2. A different maintainer approves and merges it. The notification
   issue is then closed.

Code ownership follows team membership. [CODEOWNERS](CODEOWNERS) names
the maintainer team, not individual users, so removing someone from the
team also removes their code owner approval. No per-user CODEOWNERS
edit is needed. If CODEOWNERS ever lists individual maintainers, the
removal pull request must also remove the username there.

### Returning

A former maintainer removed for inactivity is welcome to return. File a
new nomination through the normal [Process](#process).

## Team meetings and communication

Becoming a maintainer does not automatically include an invitation to
internal team meetings or private communication channels (such as
Slack). These spaces are managed separately. As the project's
maintainer community grows, we intend to establish public channels for
maintainer coordination — but that infrastructure is not yet in place.

## Stepping down

If you no longer wish to serve as a maintainer, open an issue or let an
existing maintainer know. You will be removed from the `@fullsend-ai/maintainers`
team. Stepping down is normal and carries no stigma — contributions are
always welcome regardless of maintainer status.
