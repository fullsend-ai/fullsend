# Platform Capability Matrix

Fullsend works with three developer platforms: GitHub, GitLab, and Jira. They
do not all play the same role, and fullsend does not support every feature on
every platform. This page records what fullsend supports on each platform, which
differences are intentional, which are known gaps, and how well automated tests
cover each one.

Use this page to decide whether a GitLab or Jira omission in a pull request is
an accepted platform difference, a known gap, or a code path that no test
covers.

This is a living document. The tables reflect the code at commit `c095e16`
(November 2026). When you change platform support, update the matching row in
the same pull request. See [Keeping this page current](#keeping-this-page-current).

## Platform layers

Fullsend separates two layers:

- The **forge** hosts the code. It owns repositories, branches, change proposals
  (pull requests on GitHub, merge requests on GitLab), code review, and the CI
  runner that executes agents. GitHub and GitLab are forges.
- The **tracker** holds work items. It is where people find work, run slash
  commands, apply labels, and read status comments. GitHub issues, GitLab
  issues, and Jira are trackers.

The `forge` field in `.fullsend/config.yaml` names the forge. The `tracker`
mapping names at most one tracker: `github`, `gitlab`, or `jira` (see the
[Config Reference](config-reference.md)). A repository hosted on GitHub or
GitLab can track its work in Jira.

| Forge | Tracker | Supported |
|---|---|---|
| GitHub | GitHub issues | Yes |
| GitLab | GitLab issues | Yes |
| GitHub | Jira | Yes. The Jira poll runs as a GitHub Actions workflow and dispatches agents through the GitHub dispatch workflow. |
| GitLab | Jira | Yes. The Jira poll runs as a GitLab schedule and starts agent pipelines directly. |
| Forgejo | Any | No. `forge.Client` has no Forgejo implementation. |

### Responsibilities by layer

| Responsibility | Layer | GitHub | GitLab | Jira |
|---|---|---|---|---|
| Repositories, branches, commits | Forge | Yes | Yes | Not applicable |
| Change proposals and code review | Forge | Pull requests | Merge requests | Not applicable |
| Agent execution | Forge | GitHub Actions | GitLab CI pipelines | Runs on the target forge's runner |
| Work discovery | Tracker | Issues | Issues | Work items selected by a JQL query |
| Slash commands on work items | Tracker | Issue comments | Issue comments | Work item comments |
| Slash commands on change proposals | Forge | Pull request comments | Merge request comments | Not applicable |
| Labels that start agents | Tracker and forge | Issues and pull requests | Issues and merge requests | Work items |
| Status comments | The system the event came from | Yes | Yes | Yes |

## Agent and stage applicability

Triage, code, and prioritize act on work items. Review, fix, and retro act on
change proposals, so they cannot run from a Jira event. Scribe is opt-in, and
you supply its workflow (see [Scribe](../agents/scribe.md)).

| Agent | Acts on | GitHub | GitLab | Jira work item |
|---|---|---|---|---|
| Triage | Work item | Yes: `/fs-triage`, issue opened or edited, `ready-for-triage` label | Partial: `/fs-triage` and issue opened. No edited trigger and no `ready-for-triage` label. | Yes, through harness triggers |
| Code | Work item | Yes: `/fs-code`, `ready-to-code` label | Yes: `/fs-code`, `ready-to-code` label | Yes, through harness triggers |
| Review | Change proposal | Yes: pull request opened, synchronized, or ready for review; `ready-for-review` label; `/fs-review` | Yes: merge request opened or synchronized; `ready-for-review` label; `/fs-review` | No. The GitLab agent job refuses it, and Jira has no change proposals. |
| Fix | Change proposal | Yes: changes-requested review, `/fs-fix` | Partial: same-project merge requests only, and no `fullsend-fix` opt-in | No |
| Retro | Change proposal | Yes: pull request closed, `/fs-retro` | Yes: merge request merged or closed, `/fs-retro` | No |
| Prioritize | Work item | Yes: `/fs-prioritize` and a scheduled workflow | Partial: `/fs-prioritize` routes, but there is no scheduler | No |
| Scribe | Work item | Your own workflow | Your own workflow | Can create Jira work items, but Jira events do not dispatch it |

Routing evidence: GitHub routes in
[`reusable-dispatch.yml`](https://github.com/fullsend-ai/fullsend/blob/main/.github/workflows/reusable-dispatch.yml),
GitLab in
[`internal/dispatch/router.go`](https://github.com/fullsend-ai/fullsend/blob/main/internal/dispatch/router.go),
and Jira in
[`internal/jiradispatch/`](https://github.com/fullsend-ai/fullsend/tree/main/internal/jiradispatch).
The built-in Jira trigger expressions live in the
[`fullsend-ai/agents`](https://github.com/fullsend-ai/agents) repository.

## Event intake and dispatch

| Aspect | GitHub | GitLab | Jira |
|---|---|---|---|
| How events arrive | Native GitHub Actions events through the shim workflow | Scheduled poll pipelines, plus a webhook fast path ([ADR 0125](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)). Fullsend re-reads webhook events from the API. | Scheduled JQL poll (`fullsend poll --input-driver jira-poll`) |
| How fullsend picks an agent | Stage routing in the dispatch workflow, with optional CEL triggers | Built-in router in Go. CEL triggers are not used yet ([#6343](https://github.com/fullsend-ai/fullsend/issues/6343)). | CEL triggers from the harness definitions |
| Slash commands | `/fs-triage`, `/fs-code`, `/fs-review`, `/fs-fix`, `/fs-retro`, `/fs-prioritize`, `/fs-fix-stop` | Any `/fs-<agent>` for a registered agent. `/fs-fix-stop` has no handler. Commands in threaded merge request replies are missed ([#7825](https://github.com/fullsend-ai/fullsend/issues/7825)). | `/fs-*` as the first word of a comment's first line |
| Labels from bots | Start agents | Start agents | Ignored ([#9389](https://github.com/fullsend-ai/fullsend/issues/9389)) |
| Status changes | Not used | Not used | Jira status transitions start nothing. `fullsend issues transition` only writes them. |
| Who may dispatch | Repository permission, [OWNERS files](../guides/user/owners-file-authorization.md), and `authorization` config | Project member access level, mapped to a fullsend role. Triage and review need the triage role. Other stages need write. | Jira project role or the `jira` allowlist |

## Status reporting

| Capability | GitHub | GitLab | Jira |
|---|---|---|---|
| Start and completion status comments | Yes | Yes | Yes, on the Jira work item |
| Status reactions | Yes | No. The GitLab client returns `ErrNotSupported`, and config accepts the setting without a warning ([#5998](https://github.com/fullsend-ai/fullsend/issues/5998)). | No |
| Reports of failures before the agent starts | Yes | No ([#9256](https://github.com/fullsend-ai/fullsend/issues/9256)) | No ([#9256](https://github.com/fullsend-ai/fullsend/issues/9256)) |
| Optional run label | Not applicable | Not applicable | Yes (`tracker.jira.run_label`) |

## Intentional platform differences

These differences follow from how each platform works. Do not treat them as
gaps, and do not ask for one-to-one parity in reviews.

| Area | GitHub | GitLab | Jira | Why they differ |
|---|---|---|---|---|
| Agent credentials | The token mint swaps a GitHub Actions OIDC token for a short-lived token from a per-role GitHub App | A role registry (Poller, Analyst, Coder, and custom roles) backed by project access tokens or service accounts. `repos install --rotate-gitlab-roles` rotates them. See [GitLab Role Credentials](../contributing/gitlab-role-credentials.md). | API token (Basic or Bearer) or OAuth 2.0 client credentials | Each platform has its own identity model. GitLab has no equivalent of GitHub App installation tokens, so the mint does not serve GitLab. See [How fullsend authenticates](../guides/user/how-fullsend-authenticates.md). |
| Event intake | Native workflow events | Poll and webhook hybrid | Poll only | GitLab CI has no issue or comment events ([ADR 0067](../ADRs/0067-gitlab-cron-polling-event-dispatch.md)). Fullsend has no Jira webhook receiver, so it reads Jira changes by polling. |
| Dispatch authorization | Repository permission and OWNERS files | Native member roles | Jira project roles or allowlist | Each system decides who may act using its own membership model. Jira membership does not imply forge membership. |
| Starting a run | `DispatchWorkflow` | `CreatePipeline` | Uses the target forge | The forges expose different CI APIs. `forge.Client` methods for the other forge return `ErrNotSupported`. |
| Agents that need change proposals | Run | Run | Do not run | Jira has no pull or merge requests, so review, fix, and retro run only on forge events. |
| Setup | `fullsend repos install`, plus mint enrollment | `fullsend repos install` with a GitLab token | `tracker.jira` config, with secrets from `repos install --jira-*` | Each platform needs different credentials and CI resources. See [Configuring GitHub](../guides/getting-started/configuring-github.md), [Configuring GitLab](../guides/getting-started/configuring-gitlab.md), and [Jira Integration](../guides/user/jira-integration.md). |

## Known parity gaps

These are real gaps where fullsend should behave the same across platforms but
does not yet. Each tracking issue was open when this page was last updated.

### GitLab

| Gap | Tracking issue |
|---|---|
| The fix agent does not enforce the `fullsend-fix` opt-in label on human-authored merge requests | [#7421](https://github.com/fullsend-ai/fullsend/issues/7421) |
| The fix agent supports only same-project merge requests, not forks or cross-project merge requests | [#7814](https://github.com/fullsend-ai/fullsend/issues/7814) |
| The poller misses `/fs-*` commands posted as threaded merge request replies | [#7825](https://github.com/fullsend-ai/fullsend/issues/7825) |
| The poll path uses a built-in router instead of CEL triggers | [#6343](https://github.com/fullsend-ai/fullsend/issues/6343) |
| Bot identity authorization from ADR 0107 is not enforced before routing | [#9065](https://github.com/fullsend-ai/fullsend/issues/9065) |
| The repository command-scanning policy is never applied to merge request runs | [#9493](https://github.com/fullsend-ai/fullsend/issues/9493) |
| Status reactions are accepted in config but do nothing | [#5998](https://github.com/fullsend-ai/fullsend/issues/5998) |
| Failures before the agent starts are not reported | [#9256](https://github.com/fullsend-ai/fullsend/issues/9256) |
| The pre-script relay works only on GitHub | [#8078](https://github.com/fullsend-ai/fullsend/issues/8078) |
| `FULLSEND_WORK_ITEM_URL` is not exported | [#8818](https://github.com/fullsend-ai/fullsend/issues/8818) |
| Uninstall leaves the GCP workload identity provider and IAM binding behind | [#9268](https://github.com/fullsend-ai/fullsend/issues/9268) |
| Webhook access to protected branches fails on GitLab Free | [#9343](https://github.com/fullsend-ai/fullsend/issues/9343) |
| Console output is redacted less thoroughly than traces, and GitLab has no `::add-mask::` equivalent | [#9012](https://github.com/fullsend-ai/fullsend/issues/9012) |
| Harness passthrough variables have no automated check across the GitHub and GitLab templates | [#6275](https://github.com/fullsend-ai/fullsend/issues/6275) |
| `/fs-fix-stop` has no handler, so the `fullsend-no-fix` label must be applied by hand | None yet |
| Prioritize has no scheduled run | None yet |

### Jira

| Gap | Tracking issue |
|---|---|
| Prioritize does not support Jira work items | [#2266](https://github.com/fullsend-ai/fullsend/issues/2266) |
| Retro does not support Jira work items | [#2267](https://github.com/fullsend-ai/fullsend/issues/2267) |
| Review flags pull requests that cite a Jira key instead of a forge issue as missing authorization | [#2076](https://github.com/fullsend-ai/fullsend/issues/2076) |
| Labels added by a bot do not start agents, so agents cannot hand off through labels | [#9389](https://github.com/fullsend-ai/fullsend/issues/9389) |
| The poll dispatches nothing unless agents are registered in config, and the installer registers none | [#9503](https://github.com/fullsend-ai/fullsend/issues/9503) |
| The linked Jira work item stays open when its pull request merges | [#9535](https://github.com/fullsend-ai/fullsend/issues/9535) |
| Failures before the agent starts are not reported | [#9256](https://github.com/fullsend-ai/fullsend/issues/9256) |
| Jira Data Center is not supported. Fullsend uses Jira Cloud APIs only. | None yet |

[#9452](https://github.com/fullsend-ai/fullsend/issues/9452) and
[#9453](https://github.com/fullsend-ai/fullsend/issues/9453) propose ADRs that
commit fullsend to GitLab and Jira parity.

## Test verification and CI coverage

Automated tests protect GitHub far more than GitLab or Jira. A capability with
only unit tests can break in a real installation while CI still passes.

### Coverage levels

From weakest to strongest:

1. **None:** no automated test.
2. **Unit:** Go tests, often against an `httptest` fake server.
3. **Scaffold check:** Go tests or linters that check the CI templates fullsend
   installs, without running them on the real CI system.
4. **Mock integration:** a behaviour scenario or script test that runs real
   fullsend code against a mock platform API.
5. **Live behaviour:** a [behaviour test](../guides/dev/behaviour-testing.md)
   that runs against the real platform. The behaviour job in
   [`e2e.yml`](https://github.com/fullsend-ai/fullsend/blob/main/.github/workflows/e2e.yml)
   runs on authorized pull requests, in the merge queue, and on pushes to
   `main`.

No platform has a scheduled end-to-end run.

### Coverage by capability

| Capability | GitHub | GitLab | Jira |
|---|---|---|---|
| Platform API client | Unit | Unit | Unit |
| Event intake and routing | Live behaviour | Unit, plus mock integration for Jira-to-GitLab dispatch | Mock integration (`jiramock` scenarios that run inside the GitHub behaviour job) |
| CI templates | Scaffold check: actionlint and Go tests | Scaffold check: Go content tests, job scripts run against a mock GitLab API, and shellcheck. No GitLab CI linter. | Scaffold check for the GitHub poll workflow and the GitLab poll schedule |
| Installation and setup | Live behaviour | Unit | Unit |
| Credentials | Live behaviour (mint scenarios) | Unit | Unit |
| Full agent run: dispatch, workflow, post-script, and resulting forge state | Live behaviour | None in CI. Maintainers have validated it by hand on GitLab.com Free and self-managed GitLab EE. | None |

The GitLab behaviour drivers exist in
[`pkg/behaviourtest/drivers/`](https://github.com/fullsend-ai/fullsend/tree/main/pkg/behaviourtest/drivers),
but no CI job selects them. See
[Behaviour test drivers](../guides/dev/behaviour-drivers.md). Related open
issues:

- [#9452](https://github.com/fullsend-ai/fullsend/issues/9452): GitLab
  behaviour tests do not gate pull requests or the merge queue.
- [#9017](https://github.com/fullsend-ai/fullsend/issues/9017): the role-bot
  behaviour fixture works only on GitHub.
- [#6893](https://github.com/fullsend-ai/fullsend/issues/6893): no test checks
  GitLab default stages and trace export in the same run.
- [#6275](https://github.com/fullsend-ai/fullsend/issues/6275): no automated
  check for harness passthrough variables across forges.

## Keeping this page current

Update this page in the same pull request when you:

- add, remove, or change a capability on any platform
- close a gap listed above: remove its row, or move it to another table
- add or remove test coverage that changes a coverage level
- find a new gap: file an issue and add a row

Before you cite a tracking issue, check that it is still open.

## Related documents

- [ADR 0028: GitLab support](../ADRs/0028-gitlab-support.md)
- [ADR 0067: GitLab cron polling event dispatch](../ADRs/0067-gitlab-cron-polling-event-dispatch.md)
- [ADR 0125: GitLab hybrid webhook and poller dispatch](../ADRs/0125-gitlab-hybrid-webhook-poller-dispatch.md)
- [Forge Abstraction](../contributing/forge-abstraction.md)
- [GitLab implementation](../problems/gitlab-implementation.md) and
  [Platform nativeness](../problems/platform-nativeness.md) problem documents
- [Configuring GitLab](../guides/getting-started/configuring-gitlab.md)
- [Jira Integration](../guides/user/jira-integration.md)
