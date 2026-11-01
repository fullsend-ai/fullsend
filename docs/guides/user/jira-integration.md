# Jira Integration

> **Pre-alpha.** This feature is under active development and not ready for general use. If you're reading this, you're probably helping build it — thank you. Expect rough edges, breaking changes, and missing pieces.

Connect fullsend to a Jira project so that Jira issue activity — comments, label changes — triggers the same agents that run on GitHub and GitLab.

## Setup overview

1. **Create a Jira service account** and an API token. Store the credentials as GitHub Actions secrets. See [Credential setup](#credential-setup).
2. **Grant it the minimum Jira permissions** in the target project, usually
   through the **Developers** project role. The poller needs Browse
   Projects, Edit Issues, Add Comments, and Edit Own Comments to operate on
   issues and write the entity properties it uses for lock and checkpoint
   coordination. Then choose how Jira users are authorized:
   - **Recommended:** list who may trigger agents under a `jira` provider in
     `.fullsend/config.yaml`. The service account then also needs the
     Browse users and groups global permission, but only if the lists name
     groups.
   - **Project roles:** with no `jira` entry, every Jira event is authorized by
     the actor's Jira project role; with an entry that sets no lists, slash
     commands are. Reading project roles needs the **Administrators** project
     role (Administer Projects). With `labels: project_roles`, label-added
     events are too, and with no lists every event is. See
     [Keeping project roles](#keeping-project-roles-as-today).

   With no `jira` entry, label-added events also go through project roles, so
   the poller needs Administer Projects. A `jira` entry (default
   `labels: jira_editors`) authorizes them by Edit Issues instead. See
   [Actor role resolution](#actor-role-resolution).
3. **Install fullsend** in the GitHub repository that will run the agents (`fullsend github setup`). See [Configuring GitHub](../getting-started/configuring-github.md) and [Prerequisites](#prerequisites).
4. **Copy the Jira poller workflow** into `.github/workflows/fullsend-poll-jira.yml`. See [Scheduled workflow](#scheduled-workflow).

   For a GitLab project, steps 3 and 4 differ: install fullsend in the project with `fullsend repos install` and run the poller from a scheduled GitLab CI job with `--target-forge gitlab`. See [GitLab runtime](#gitlab-runtime).
5. **Set the Jira project key** (replace `PROJ`) and, if you want a narrower candidate set, a custom `--jql` expression in that workflow. See [Custom JQL](#custom-jql).
6. **Check which agents handle Jira events.** The poller dispatches an agent
   only when its harness `trigger:` CEL expression matches the Jira event, and
   produces zero dispatches for a harness with no trigger, its own or
   inherited. The built-in triage and code harnesses ship triggers that match
   Jira events, so they need no extra configuration:

   | Built-in agent | Jira events its trigger matches | From |
   |---|---|---|
   | triage | A `/fs-triage` comment from a human, the `ready-for-triage` label being added, or a human opening or editing an issue | The agents release that follows v0.49.0 |
   | code | A `/fs-code` comment from a human, or the `ready-to-code` label being added | The agents release that follows v0.52.0 ([agents#1842](https://github.com/fullsend-ai/agents/pull/1842)) |

   The actor still needs the role the agent requires. See
   [Actor role resolution](#actor-role-resolution). The default prioritize
   trigger also matches a `/fs-prioritize` comment on a Jira issue, but
   prioritize has no Jira support yet
   ([#2266](https://github.com/fullsend-ai/fullsend/issues/2266)). Review,
   fix and retro do not run from Jira events: see [How it works](#how-it-works).

   If your built-in agents are pinned to an earlier agents release, or for a
   custom agent, add a `trigger:` to a harness YAML file registered via
   `config.yaml`'s `agents[].source` (see [Configuring agent
   behavior](customizing-agents.md#configuration-with-base-composition)). To
   use the built-in triggers instead, move to the release listed above. Re-pin
   a harness registered with `source:` or composed with `base:` with
   `fullsend agent update <name>`. The
   poller reads only the harness `trigger:`. A `trigger` on an `agents:` entry
   in `config.yaml` does not affect it: that field decides routing for the
   built-in review, retro, triage and prioritize agents on GitHub only
   ([Choosing when review runs](customizing-agents.md#choosing-when-review-runs),
   [Choosing when retro runs](customizing-agents.md#choosing-when-retro-runs),
   [Choosing when triage and prioritize
   run](customizing-agents.md#choosing-when-triage-and-prioritize-run)).
   See [CEL Triggers Reference](cel-triggers-reference.md).

   > [!WARNING]
   > A harness you registered with `source:` that points at a default
   > harness, or composes it with `base:`, and sets no trigger of its own
   > inherits the default trigger when you re-pin its ref to a release that
   > ships one. For code (from the agents release that follows v0.52.0), a
   > `/fs-code` comment or an added `ready-to-code` label on a Jira issue then
   > dispatches it. For triage (from the agents release that follows
   > v0.49.0), every Jira issue that a person with triage access or above
   > opens or edits dispatches triage, not only
   > `/fs-triage`, and on GitHub an entry registered under another name, such
   > as `my-triage`, is dispatched on every issue a person opens or edits.
   > Set your own trigger to keep a narrower one.
   > On Jira, an issue opener or editor gets triage access or above from the
   > project roles when there is no `jira` entry (or `labels: project_roles`
   > with no lists), or from the `jira` authorization lists when there is
   > one. Any other `jira` entry that sets no `write` or `triage` list leaves
   > opened and edited events unresolved, so they do not dispatch. See [Actor role resolution](#actor-role-resolution).

## How it works

A scheduled GitHub Actions workflow runs `fullsend poll --input-driver jira-poll` on a cron. For a GitLab project, a scheduled GitLab CI job runs it with `--target-forge gitlab` instead (see [GitLab runtime](#gitlab-runtime)). Each cycle:

1. Queries Jira for recently updated issues in your project.
2. Detects new comments and label changes since the last poll.
3. Converts each change to a [NormalizedEvent](../../normative/normalized-event/v1/) — the forge-neutral event struct that all input drivers (GitHub, GitLab, Jira) produce.
4. Routes through the standard agent routing rules.
5. Writes dispatch records that trigger agent workflows, or, for a GitLab target, starts the agent pipelines directly.

For architectural details on the polling protocol, see [Architecture](../../architecture.md#agent-dispatch-and-coordination-layer).

The same conventions work across forges:

| Jira action | Agent triggered |
|---|---|
| Comment starting with `/fs-triage` | triage |
| Comment starting with `/fs-code` | code |
| Label `ready-to-code` added | code |

A slash command is only recognized as the **first token of the comment's first
line**. The rest of that line is recorded in the normalized event as
`transition.comment.instruction`. Later lines are not part of it. No built-in
agent that runs from Jira reads that field: only the fix agent receives an
instruction today, parsed from the `/fs-fix` comment on the pull or merge
request, and fix does not run from Jira events. The built-in triage and code
agents instead read the Jira issue and its comments through the read-only Jira
provider (`jira-ro`). So the whole comment, including any lines after the
command, reaches them as part of the issue context, together with every other
comment on the issue. A custom agent's `trigger` can test
`event.transition.comment.instruction`.
A command buried mid-sentence ("please /fs-triage this") or on a later line does not trigger. Slash commands follow the `/fs-{agent}` pattern for stages that can legitimately run against a bare Jira issue. `review`, `fix`, and `retro` are **not** among them: per the [jira-poll adapter spec](../../normative/normalized-event/v1/jira-poll-adapter.md#state), those stages are change-proposal-scoped (they act on an existing PR) and harness CEL triggers for them MUST require `entity.kind == 'change_proposal'` — which a Jira issue comment alone never has. A `/fs-review` comment on a Jira issue is not expected to dispatch anything.

Comments and changelog entries from the **authenticated poller account** are classified as `actor.kind: bot` and do not dispatch agents. The poller learns that identity from Jira's `/myself` endpoint during the auth preflight, so a regular Atlassian service account (plain display name, `accountType: atlassian`) is recognized without renaming it or adding display-name heuristics. This is the invariant that a mutation performed by fullsend — applying `needs-info`, posting a triage comment, writing a run-status update — cannot trigger the same agent again. Human comments, including `/fs-triage`, still dispatch. Filtered self-authored events still advance the per-issue checkpoint, so they are not reconsidered on the next poll.

The poller also classifies an actor as a bot, and drops its events, when
either of these holds:

- The actor's Jira account type is `app`.
- The actor's display name, compared case-insensitively, ends in `bot` or
  `automation` as a separate word, or starts with `Automation for `. A
  separate word follows a space, hyphen, underscore or `[`, and may be
  followed by a closing `]`. `Release Bot`, `ci-bot`, `[automation]` and
  Atlassian's `Automation for Jira` match. `Dependabot` and `Marbot` do not.
  A person whose display name matches is treated as a bot too, so their
  comments and label changes do not dispatch.

### Use a dedicated account for the poller

Give the poller a Jira account of its own, such as an Atlassian service
account, rather than a person's API token. The poller and the built-in agents'
Jira writes both authenticate as `JIRA_USER_EMAIL` / `JIRA_TOKEN`, and the
poller treats that account as a bot. If the token belongs to a person:

- That person's own comments and label changes never dispatch agents,
  including their `/fs-triage` and `/fs-code` commands.
- Jira history and comments cannot tell what the person did from what
  fullsend did, because both appear under the person's name.
- The poller runs with every Jira permission the person holds, not only the
  ones it needs, and stops working if the person leaves or loses access.

### Labels present when an issue is created

A label-added trigger, such as the code agent's `ready-to-code`, does not fire
for an issue that was created with the label already applied. The poller
builds `label_changed` events from the issue's changelog, and Jira records no
changelog entry for labels set at creation. On an issue's first poll, the
poller instead emits a single `opened` event with the reporter as the actor
and the issue's current labels in `state.labels`. It does this only when the
issue was created within the 24-hour backfill window (see
[Poll coordination](#poll-coordination)). The default triage trigger matches
that `opened` event. The default code trigger does not.

To start the code agent on such an issue, do one of the following:

1. Comment `/fs-code` on the issue.
2. Remove the `ready-to-code` label, then add it again.

The person who comments or re-adds the label needs the role the code agent
requires, as for any other trigger. See
[Actor role resolution](#actor-role-resolution).

## Event semantics — input only

The `event_type` field in dispatch records (e.g. `"comment_added"`, `"label_changed"`) describes the Jira-side activity that **triggered** the dispatch — it is an **input** event, not a description of what the agent will do. When you see `event_type: "comment_added"` in `dispatches.json`, it means "a comment was added to a Jira issue, and that comment matched a routing rule." It says nothing about the agent's output.

For a Jira-sourced run, `fullsend run` exports the tracker-independent `FULLSEND_WORK_ITEM_SOURCE` (`jira`), `FULLSEND_WORK_ITEM_KEY` (for example `PROJ-123`), and `FULLSEND_WORK_ITEM_URL` variables from the normalized event, replacing any inherited values, and clears `ISSUE_NUMBER` because a Jira key has no GitHub or GitLab issue number. The run fails closed, rather than guessing an identity, when the dispatch payload declares Jira but its key or URL is missing or malformed. Harness scripts that handle Jira work items should read the `FULLSEND_WORK_ITEM_*` variables instead of `ISSUE_NUMBER`.

### Where built-in agent results appear

Starting with the agents release that follows v0.52.0
([agents#1843](https://github.com/fullsend-ai/agents/pull/1843)), the built-in
triage and code agents write their results to the Jira issue that started the
run. This works for both GitHub and GitLab code-host targets. Run-status
notifications (start, completion, and orphan reconciliation) are routed through
`tracker.Client` to the originating tracker, so the Jira issue also receives
run-status updates.

| Stage | Written to the Jira issue | Stays on the code host |
|---|---|---|
| triage | The triage comment, label and component updates, and close actions as Jira transitions | No code artifacts |
| code | A short code-stage update with the URL of the created or updated PR (GitHub target) or MR (GitLab target) | Branch, commits, PR or MR, review, and merge |

Review, fix, and retro act on pull or merge request events on the code host and
do not write to Jira.

The post-scripts write to the issue at `FULLSEND_WORK_ITEM_URL`. Triage stops
before any write if `FULLSEND_WORK_ITEM_KEY` does not match that URL, or if an
explicit `FULLSEND_TRACKER` or `ISSUE_URL` points somewhere else. Code stops if
`FULLSEND_TRACKER` and `FULLSEND_WORK_ITEM_SOURCE` are both set and disagree.

- **Transitions:** triage closes issues with the Jira transition names set by
  `JIRA_DUPLICATE_TRANSITION`, `JIRA_NOT_PLANNED_TRANSITION`, and
  `JIRA_SPLIT_TRANSITION` in the triage harness's Jira overlay (by default
  `Duplicate`, `Won't Do`, and `Obsolete`). Override them in a harness if your
  Jira workflow uses different names. Tracker-neutral transition discovery is
  tracked in [#6704](https://github.com/fullsend-ai/fullsend/issues/6704).
- **Updates Jira cannot apply:** triage lists them under **Not applied in
  Jira** in its comment instead of dropping them. This covers a label add for
  a label not used anywhere on the Jira site (triage does not create new
  labels), and component changes skipped because the issue's current
  components could not be read.
- **Retries:** comments are not duplicated. The code-stage update and the
  sticky triage comments use fixed markers. Other triage comments use a marker
  scoped to the CI run (`GITHUB_RUN_ID` or `CI_PIPELINE_ID`). A re-run of the
  same workflow or pipeline edits its earlier comment, while a new triage run
  posts a new one.
- **Write failures:** a failed Jira write fails the step, so it is never
  reported as a success. For triage this covers label adds and removals,
  component updates, comments, and transitions. For code the step fails even
  though the PR or MR already exists.
- **Bot loops:** the writes authenticate as `JIRA_USER_EMAIL` / `JIRA_TOKEN`,
  the same account the poller classifies as `actor.kind: bot`, so they do not
  dispatch agents again. See [How it works](#how-it-works).
- **Credentials:** the triage and code harnesses pass `JIRA_USER_EMAIL` and
  `JIRA_TOKEN` to the runner-side post-scripts only. The sandbox gets the
  `jira-ro` provider's placeholder, not the real token.

When the code agent makes no PR or MR, or the run fails, nothing beyond the
run-status update is written to Jira.

Custom agents can use `fullsend issues post-comment --tracker jira` to post
results back to Jira, and `fullsend issues create --tracker jira` to create
Jira issues and sub-tasks. These commands use the `tracker.Client` Jira
implementation
([#5989](https://github.com/fullsend-ai/fullsend/issues/5989)). See
[Issue commands](issues-commands.md) for usage.

## Prerequisites

- A GitHub repo with fullsend installed (`fullsend github setup` completed).
- A Jira Cloud instance. **Jira Data Center is not currently supported** — the client is hard-wired to Cloud-only APIs (REST v3, cursor-based search pagination, `groupId`-based group lookup), so requests against a Data Center instance will fail. Tracked as future work.
- A Jira API token ([Create API token](https://id.atlassian.com/manage-profile/security/api-tokens))
  for a dedicated service account with the
  [minimum Jira permissions](#minimum-jira-permissions). That is usually the
  **Developers** project role. The **Administrators** role is needed only
  when project roles are used: with no `jira` entry, for slash commands when
  the `jira` entry sets no lists, or for `labels: project_roles`.

## Credential setup

1. Open your GitHub repo's **Settings > Secrets and variables > Actions**.
2. Add the following secrets and variables:

| Secret / variable | Value |
|---|---|
| `JIRA_TOKEN` | Your Jira API token |
| `JIRA_USER_EMAIL` | Email associated with the token |
| `JIRA_BASE_URL` | Jira instance URL, e.g. `https://myteam.atlassian.net` |

`fullsend run` reads `JIRA_TOKEN` and `JIRA_USER_EMAIL` for its own Jira calls
but does not pass them on to host-side pre/post/validation scripts by default. A
harness whose scripts call Jira must declare them under `env.runner` (for
example `JIRA_TOKEN: "${JIRA_TOKEN}"`). The built-in triage and code harnesses
already do this in their Jira overlays, so their post-scripts can write results
to the Jira issue. See
[Harness reference § `env.runner`](../../reference/harness-reference.md#field-details).

### Sandbox credentials and network access

The fullsend binary ships an OpenShell credential provider (`atlassian-cloud`) and network profile (`fullsend-atlassian-cloud`) that grant sandboxed agents access to Jira Cloud. List the bare name `atlassian-cloud` under a harness's `providers:` and `fullsend run` uses the built-in definition and imports its profile; nothing is written to the `.fullsend` directory:

- **Provider** (`atlassian-cloud`) — declares the `atlassian-cloud` credential provider using the `fullsend-atlassian-cloud` profile type. Jira Cloud Basic auth requires a base64-encoded `email:api-token` string; because OpenShell does not yet support composite credential injection, provide a single pre-encoded token (generate with `printf 'you@example.com:your-api-token' | base64`).
- **Profile** (`fullsend-atlassian-cloud`) — allows outbound HTTPS to `*.atlassian.net:443` with read-write access and endpoint enforcement. Permitted binaries are `curl` and `node`.

The wildcard host (`*.atlassian.net`) permits egress to any Atlassian Cloud tenant. Per-tenant scoping is not yet supported by OpenShell; this is an accepted risk for now.

## Repo configuration

The Jira poller produces the same
[NormalizedEvents](../../normative/normalized-event/v1/) that GitHub and GitLab
do, so routing works the same way. From the agents release that follows
v0.52.0, the built-in triage and code harnesses ship `trigger` expressions
that match Jira events. A custom agent, or a built-in agent pinned to an
earlier release, needs its own matching `trigger`. See
[Setup overview](#setup-overview) step 6 for which events each built-in
trigger matches. For Jira-triggered runs,
`fullsend run` exports `FULLSEND_WORK_ITEM_SOURCE`, `FULLSEND_WORK_ITEM_KEY`,
and `FULLSEND_WORK_ITEM_URL`, clears `ISSUE_NUMBER`, and fails closed on a
malformed Jira identity. The built-in triage and code agents write their results
to the Jira issue, while branches, commits, PRs or MRs, review, and merge stay
on the code host. See
[Where built-in agent results appear](#where-built-in-agent-results-appear) for
details.

If your repo already has a `.fullsend/config.yaml` from `fullsend github setup`
and uses the built-in triage and code agents from the agents release that
follows v0.52.0 or later, you need no harness changes to receive dispatches.
Otherwise, add a harness override with a Jira `trigger` expression for each
agent that should handle Jira events (see step 6 and
[Configuring agent behavior](customizing-agents.md#configuration-with-base-composition)).

## Scheduled workflow

1. Create `.github/workflows/fullsend-poll-jira.yml` with the following content:

```yaml
name: fullsend Jira poll

on:
  schedule:
    - cron: "*/5 * * * *"  # every 5 minutes
  workflow_dispatch: {}     # allow manual runs

permissions:
  actions: write
  contents: write
  id-token: write
  issues: write
  packages: read
  pull-requests: write

jobs:
  poll:
    runs-on: ubuntu-24.04
    concurrency:
      group: fullsend-jira-poll
      cancel-in-progress: false
    outputs:
      matrix: ${{ steps.build-matrix.outputs.matrix }}
    steps:
      - uses: actions/checkout@v4

      - name: Install fullsend
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          gh release download --repo fullsend-ai/fullsend -p 'fullsend_*_linux_amd64.tar.gz' -O - | tar xz
          sudo mv fullsend /usr/local/bin/

      - name: Poll Jira
        env:
          JIRA_TOKEN: ${{ secrets.JIRA_TOKEN }}
          JIRA_USER_EMAIL: ${{ secrets.JIRA_USER_EMAIL }}
          JIRA_BASE_URL: ${{ vars.JIRA_BASE_URL }}
          TARGET_REPO: ${{ github.repository }}
        run: |
          fullsend poll \
            --input-driver jira-poll \
            --jira-url "${JIRA_BASE_URL}" \
            --jira-project PROJ \
            --target-repo "${TARGET_REPO}" \
            --output dispatches.json \
            --fullsend-dir .fullsend

      - name: Build dispatch matrix
        id: build-matrix
        run: |
          set -euo pipefail
          if ! jq -e 'length > 0' dispatches.json > /dev/null 2>&1; then
            echo "No dispatches."
            echo 'matrix={"include":[]}' >> "${GITHUB_OUTPUT}"
            exit 0
          fi
          MATRIX=$(jq -c '{include: .}' dispatches.json)
          DELIM="MATRIX_$(openssl rand -hex 8)"
          {
            echo "matrix<<${DELIM}"
            printf '%s' "${MATRIX}"
            echo
            echo "${DELIM}"
          } >> "${GITHUB_OUTPUT}"

  harness:
    name: Harness
    needs: poll
    uses: fullsend-ai/fullsend/.github/workflows/reusable-dispatch.yml@main
    with:
      matrix: ${{ needs.poll.outputs.matrix }}
      mint_url: ${{ vars.FULLSEND_MINT_URL }}
      gcp_region: ${{ vars.FULLSEND_GCP_REGION }}
      jira_base_url: ${{ vars.JIRA_BASE_URL }}
    secrets:
      FULLSEND_GCP_WIF_PROVIDER: ${{ secrets.FULLSEND_GCP_WIF_PROVIDER }}
      FULLSEND_GCP_PROJECT_ID: ${{ secrets.FULLSEND_GCP_PROJECT_ID }}
      OTEL_EXPORTER_OTLP_TRACES_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_TRACES_HEADERS }}
      OTEL_EXPORTER_OTLP_HEADERS: ${{ secrets.OTEL_EXPORTER_OTLP_HEADERS }}
      JIRA_TOKEN: ${{ secrets.JIRA_TOKEN }}
      JIRA_USER_EMAIL: ${{ secrets.JIRA_USER_EMAIL }}
```

2. Replace `PROJ` with your Jira project key.
3. Commit and push the workflow file.

**How this works:** The `poll` job queries Jira and builds a dispatch matrix in
the format expected by `reusable-dispatch.yml`. When the `matrix` input is
provided, `reusable-dispatch.yml` skips its routing and dispatch steps and
goes directly to running harness agents with the pre-computed matrix. Because
the agent stages still run inside `reusable-dispatch.yml`, dispatch and the
agents always come from the same fullsend version, and custom pollers can
reuse fullsend's harness infrastructure. See
[Custom Poller Example](custom-poller-example.md) for more details on this
pattern.

**`concurrency.cancel-in-progress: false`** ensures overlapping poll cycles queue rather than cancel each other, which is the primary defense against concurrent runs — the GitHub Actions concurrency group means only one poll cycle for this workflow ever runs at a time in the common case. The poller's own Jira entity-property locking is a secondary guard for cases outside that group (e.g. a manually triggered run overlapping a scheduled one): it re-checks for a live lock immediately before writing, narrowing the race to the jitter window between that check and the write, but Jira entity properties have no compare-and-swap, so a lock created in that narrow window can still be clobbered by a genuinely concurrent poller. Treat the GHA concurrency group as the real safety mechanism, not the lock.

### Custom JQL

By default the poller searches for all non-done issues in the project, ordered by most recently updated. To narrow the scope, use `--jql`:

```bash
fullsend poll \
  --input-driver jira-poll \
  --jira-url "${JIRA_BASE_URL}" \
  --jira-project PROJ \
  --jql 'project = PROJ AND labels = "fullsend" AND statusCategory != Done ORDER BY updated DESC' \
  --target-repo "${{ github.repository }}" \
  --output dispatches.json \
  --fullsend-dir .fullsend
```

**`--jira-project` is required for any project using slash commands whose roles come from Jira project roles.** Without it the poller cannot resolve Jira project roles, so all actors default to the `external` role and every `/fs-*` command silently fails the role gate. When the `jira` authorization entry sets `write` or `triage` lists and does not use `labels: project_roles`, roles come from those lists instead and `--jira-project` is not needed for role resolution. Otherwise, provide `--jira-project` alongside `--jql` unless you are certain your routing rules do not depend on actor roles.

Technically `--jql` can be used without `--jira-project`. That is safe in a read-only polling setup with no slash-command-based dispatch, or when the `jira` authorization entry sets `write` or `triage` lists without `labels: project_roles`, so that actor roles come from the lists and the project roles are never read (with `labels: project_roles`, label-added events still read them).

Custom JQL must be a **bounded query**: Jira's enhanced search endpoint rejects queries without a search restriction (e.g. a bare `ORDER BY updated DESC`) with a 400 on every cycle. Always include at least a `project = ...` or similar restriction, as the examples above do.

## GitLab runtime

Jira work items can also dispatch agents in a GitLab project. Run the poller
with `--target-forge gitlab` from a CI job in that project. The poller does
not write a matrix for a downstream step. It starts each matched agent
directly, through the same protected pipeline route the GitLab poller uses
for issues and merge requests.

### Requirements

- The project is installed with `fullsend repos install` from a release that
  includes Jira dispatch. Older scaffolds limit the `resource_key` pipeline
  input to GitLab issue and merge request keys. The poller reads the committed
  `.gitlab/ci/fullsend-pipeline.yml` before each dispatch and fails with
  `does not accept Jira work items; rerun repos install` when the installed
  scaffold predates Jira dispatch.
- The poll job runs in a scheduled pipeline on the protected default branch,
  so the protected `FULLSEND_GITLAB_POLLER_TOKEN` and
  `FULLSEND_DISPATCH_SECRET` variables that `repos install` provisions are
  available to it. The poller refuses to dispatch without
  `FULLSEND_DISPATCH_SECRET`, because the agent pipeline rejects unsigned
  dispatches.
- `JIRA_TOKEN`, `JIRA_USER_EMAIL`, and `JIRA_BASE_URL` are set as protected,
  masked CI/CD variables. The agent jobs need them as well, to post run status
  and the triage and code results to the Jira issue.

`fullsend` does not yet create the schedule or the Jira variables for you.
That setup is tracked in
[#8906](https://github.com/fullsend-ai/fullsend/issues/8906).

### Running the poller

Use the identity the scaffold's `pin-ci-job-identity.sh` pins from the job
record, as `run-poll-job.sh` does for the GitLab poller, rather than
`CI_PROJECT_PATH` and `CI_SERVER_URL`. Pipeline variables can override those
two.

```bash
export CI_COMMIT_REF_NAME="${FULLSEND_PINNED_REF}"
fullsend poll \
  --input-driver jira-poll \
  --target-forge gitlab \
  --jira-url "${JIRA_BASE_URL}" \
  --jira-project PROJ \
  --project "${FULLSEND_PINNED_PROJECT_PATH}" \
  --gitlab-url "${FULLSEND_PINNED_GITLAB_URL}" \
  --fullsend-dir .fullsend
```

| Flag | GitLab target behavior |
|---|---|
| `--target-forge gitlab` | Dispatch to a GitLab project. The default, `github`, keeps the GitHub Actions flow above |
| `--project` | Full project path, including every nested group (for example `group/subgroup/project`). Defaults to `CI_PROJECT_PATH` |
| `--gitlab-url` | Instance URL. Must be `https`. A self-managed instance served under a path prefix keeps the prefix |
| `--output` | Optional. When set, the dispatch records are also written to the file |
| `--target-repo` | Rejected, because it names a GitHub repository |

The command uses the Poller role credential and the dispatch route the
scaffold selected in `FULLSEND_GITLAB_DISPATCH_ROUTE`: the Poller API route,
or the `CI_JOB_TOKEN` child-pipeline route.

### What the agent pipeline receives

Each dispatch is an agent pipeline on the pinned default branch, with these
typed pipeline inputs signed by the `FULLSEND_DISPATCH_SECRET` HMAC:

| Input | Value |
|---|---|
| `stage` | The matched agent |
| `event_type` | `jira_` plus the event's `source.raw_type`, for example `jira_comment` |
| `resource_key` | `jira-` plus the Jira key, for example `jira-PROJ-123` |
| `originating_url` | The Jira browse URL |
| `repo_full_name` | The full GitLab project path |
| `is_fork` | `false` |
| event payload | `{"type": ..., "_normalized_event": ...}`. The comment body and instruction are shortened, ending in `[…truncated]`, when needed to fit GitLab's pipeline input limits |

`actor_id` and `status_iid` are empty: a Jira actor has no GitLab user ID, and
a Jira issue has no GitLab IID.

The agent job runs the same checks as for any other dispatch: identity pin,
pipeline creator, HMAC, protected default branch, kill switch, and role
credential selection. For a `jira_*` event type it then:

- Skips the GitLab Developer-access check on the actor, because the poller
  already authorized the actor against its Jira project role. See
  [Jira membership is not GitHub membership](#jira-membership-is-not-github-membership).
- Passes the normalized event to `fullsend run --event-file`, so the run gets
  the `FULLSEND_WORK_ITEM_*` variables and posts run status to the Jira issue.
  A payload without a Jira normalized event fails the job.
- Refuses the `review` and `fix` stages, which need a merge request.

### Failures

The poller fails closed. An invalid `--project` or `--gitlab-url` stops the
command before any Jira call. A dispatch also fails, and creates no pipeline,
when:

- the event targets a different project
- the Jira key or URL is malformed
- the project has no fullsend pipeline wrapper, or its scaffold predates Jira
  dispatch
- GitLab rejects the pipeline, for example because the Poller credential
  cannot create pipelines in the project

When any dispatch for a Jira issue fails, that issue's `lastCheck` is not
advanced and the cycle exits non-zero. The next cycle retries the issue, which
can dispatch again any of its records that already succeeded.

GitLab targets keep their lock and checkpoint properties in their own
namespace, `fullsend.poll.gitlab.{instance}.{group path}.{project}.*`, where
`{instance}` is the host and any path prefix of `--gitlab-url`. A GitLab
project never shares Jira coordination state with a GitHub repository of the
same name, or with a project of the same path on another GitLab instance.

## Local testing

You can run `fullsend poll` locally to verify your Jira connection and inspect dispatch output before deploying the scheduled workflow.

### Required environment variables

Set the same credentials you would configure as GitHub Actions secrets:

```bash
export JIRA_TOKEN="your-jira-api-token"
export JIRA_USER_EMAIL="you@example.com"
export JIRA_BASE_URL="https://myteam.atlassian.net"
```

### Running the poller

From the repo root, use `go run` (per [AGENTS.md](../../../AGENTS.md) convention — do not use a `fullsend` binary from `$PATH` or another checkout):

```bash
go run ./cmd/fullsend poll \
  --input-driver jira-poll \
  --jira-url "${JIRA_BASE_URL}" \
  --jira-project PROJ \
  --target-repo owner/repo \
  --output dispatches.json \
  --fullsend-dir .fullsend
```

Replace `PROJ` with your Jira project key and `owner/repo` with the GitHub repository slug where agent workflows run.

> **Note:** `--jira-project` is effectively required when your project uses slash commands (`/fs-triage`, `/fs-code`, etc.) and roles come from Jira project roles. Without it, the poller cannot resolve Jira project roles and all actors default to `external`, which silently fails the role gate for write-gated commands. It is not needed for role resolution when the `jira` authorization entry sets `write` or `triage` lists, because the lists decide the role. See [#6089](https://github.com/fullsend-ai/fullsend/issues/6089) for details.

### Inspecting output

The poller writes dispatch records to the `--output` path. Inspect them with `jq`:

```bash
# Pretty-print all dispatch records
jq '.' dispatches.json

# Count dispatches
jq 'length' dispatches.json

# Show agent and event type for each dispatch
jq '.[] | {agent, event_type, status_number}' dispatches.json
```

Key fields in each dispatch record:

| Field | Description |
|---|---|
| `agent` | Agent to dispatch (e.g., `triage`, `code`) |
| `event_type` | What triggered the dispatch (e.g., `comment_added`, `label_changed`) |
| `event_payload` | JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — inspect with `jq -r '.event_payload \| fromjson' dispatches.json` |
| `source_repo` | GitHub repo slug where the agent runs |
| `status_repo` | GitHub repo slug for status tracking |
| `status_number` | Entity identifier as a string (Jira numeric issue ID) |

### Dry-run tip

To inspect what the poller *would* dispatch without triggering any agent workflows, run the `fullsend poll` command above and stop there — do not run the "Dispatch agent workflows" step from the [scheduled workflow](#scheduled-workflow). The poll step writes `dispatches.json` and advances Jira checkpoints, but no workflows are dispatched until the separate dispatch script reads that file and calls `gh workflow run`.

If you want to avoid advancing Jira checkpoints during testing, poll against a dedicated test project or use a narrowly scoped `--jql` that selects only test issues.

## Dispatch record format

> **Implementation detail.** Once Jira support is stabilized, users will not need to know this dispatch format — it will be an internal detail between the poll job and the `fullsend run` workflows. This section is documented now because the integration is pre-alpha and operators may need to debug or inspect dispatch records directly.

The poll step writes an array of dispatch records to `dispatches.json`. Each record is an execution ref compatible with the `workflow_call` shim (`reusable-dispatch.yml`). Fields:

| Field | Type | Description |
|---|---|---|
| `agent` | string | Agent to run (e.g., `"triage"`, `"code"`). Determined by CEL trigger evaluation. |
| `role` | string | Harness role for this agent. |
| `event_type` | string | Jira event type that triggered the dispatch (e.g., `"comment_added"`, `"label_changed"`, `"opened"`, `"reopened"`, `"edited"`, `"closed"`). |
| `event_payload` | string | JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/). Example below. |
| `source_repo` | string | GitHub repo slug where the agent workflow runs. |
| `trigger_source` | string | (Optional) Trigger source identifier. |
| `status_repo` | string | GitHub repo slug for status tracking. |
| `status_number` | string | Entity identifier as a string (Jira's numeric issue ID). |

### `event_payload` example

The `event_payload` field is a JSON-encoded [NormalizedEvent](../../normative/normalized-event/v1/) — the same forge-neutral struct that GitHub and GitLab input drivers produce. An example for a `/fs-triage` comment on `PROJ-101`:

```json
{
  "repo": "acme/platform",
  "entity": {
    "kind": "work_item",
    "id": 10042,
    "key": "PROJ-101",
    "url": "https://myteam.atlassian.net/browse/PROJ-101"
  },
  "transition": {
    "kind": "comment_added",
    "comment": {
      "body": "/fs-triage please triage this bug",
      "command": "/fs-triage",
      "instruction": "please triage this bug"
    }
  },
  "actor": {
    "id": "5f7c012e0b2a4c001f3d9876",
    "kind": "human",
    "role": "write",
    "is_entity_author": false
  },
  "state": {
    "labels": ["bug", "fullsend"]
  },
  "source": {
    "system": "jira",
    "raw_type": "comment",
    "raw_action": "created"
  }
}
```

The `entity.key` field is required when `source.system` is `"jira"` — it carries the human-readable Jira key (e.g., `PROJ-101`). See the [NormalizedEvent spec](../../normative/normalized-event/v1/) for the full schema.

## Actor role resolution

The `jira` provider under `authorization:` in `.fullsend/config.yaml` is
opt-in
([ADR 0153](../../ADRs/0153-authorize-jira-poll-actors-without-project-administration.md)).
With no `jira` entry, the poller resolves every event, label-added events
included, through the Jira project roles (see
[Project role mapping](#project-role-mapping)), which needs Administer
Projects. To authorize actors without that permission, add a `jira` entry:

```yaml
authorization:
  - provider: jira
    write:            # Fullsend role write
      groups: [example-team-developers]
      accounts: ["5f7c012e0b2a4c001f3d9876"]   # Jira account IDs
    triage:           # Fullsend role triage
      groups: [example-team]
      accounts: []
    labels: jira_editors  # jira_editors (default) | allowlist | project_roles
```

- **Label added.** When a `jira` entry exists, by default
  (`labels: jira_editors`), any human who adds a label can trigger the agents
  whose trigger matches it. With no `jira` entry, a label-added event takes
  the project-role rule described below instead. Jira only lets users with the Edit Issues
  permission change labels, so this works like GitHub, where applying a
  label needs triage access. Edit Issues is often granted more widely than
  the Developers role. Set `labels: allowlist` to check label authors
  against the lists below, or `labels: project_roles` to keep the
  project-role rule described below.
- **Slash commands and other activity, with lists.** When the `jira` entry
  sets a `write` or `triage` list, the lists decide, and Jira project roles
  are not read. A list left empty (for example after removing its last
  entry) still counts as set, so everyone is denied; remove the `write` and
  `triage` keys entirely to stop using the lists (see the next bullet for
  what then decides). An actor listed under `write` (by account ID,
  or through a listed group) can run every agent. An actor listed only under
  `triage` can run `/fs-triage` and custom agents whose harness
  sets `role: triage` or `role: review`. Everyone else is denied. This also
  applies to issues being opened or edited.
- **Slash commands without lists.** Slash commands are authorized by the
  actor's Jira project role, as in the table below. When a `jira` entry exists
  but sets no lists and does not set `labels: project_roles`, issues being
  opened, edited, closed, or reopened, plain comments, and label removals are
  not authorized for any agent. With no `jira` entry, or with
  `labels: project_roles` and no lists, every event takes the project-role
  rule.

### Keeping project roles as today

To keep authorizing every Jira event through project roles, as with no
`jira` entry, write the two-line entry:

```yaml
authorization:
  - provider: jira
    labels: project_roles
```

It needs no lists. If you add a `write` or `triage` list to it, the lists
decide every event except label-added ones.

Group entries match a Jira group's name or group ID and are checked with
Jira's user-groups API, once per actor per poll cycle. Account entries are
compared with the actor's account ID directly. To find an account ID, open
the user's Jira profile: the ID is the last part of its URL.

### Minimum Jira permissions

| Permission | When the poller account needs it |
|---|---|
| Browse Projects, Edit Issues, Add Comments, Edit Own Comments | Always |
| Browse users and groups (global permission) | When the `jira` lists name groups, or when project-role resolution finds a project role granted to a group (the poller then calls `GET /user/groups` for the actor) |
| Administer Projects (the **Administrators** project role) | Only when project roles are used: no `jira` entry, `labels: project_roles`, or slash commands when the `jira` entry sets no lists |

The poller reads the project role list only in a cycle that has an event
needing it. A setup whose `jira` entry uses the lists without
`labels: project_roles`, or only label triggers under `jira_editors`, never
needs Administer Projects. With no `jira` entry,
or with `labels: project_roles` and no lists, every event needs the role list.

### Project role mapping

> **Known limitation.** Roles are resolved by Jira project **role name**, not by actual granted permissions. This is intentional for the MVP — see below.

When project roles are used, the poller maps each event actor to a dispatch authorization role (`read`, `write`, `admin`) by looking up their Jira project role membership and matching on the role's **name**:

| Jira project role name (case-insensitive) | Dispatch role |
|---|---|
| `Administrators` | `admin` |
| `Developers` | `write` |
| anything else (including custom role names) | `read` |

This does **not** check the project's permission scheme, so it can be wrong in both directions:

- An org with a custom role literally named `Developers` that has *not* been granted edit permissions will be over-privileged for write-gated dispatch (e.g. `/fs-code`).
- An org using differently-named roles (e.g. `Contributors`, `Engineering`) for people who *do* have edit access will be silently downgraded to `read`, and their slash commands will be ignored.

If your project uses Jira's default role names ("Administrators"/"Developers") with their default permissions, this works as expected. If you use custom role names, expect actors to resolve to `read` regardless of their real permissions until real permission-scheme resolution is implemented (tracked as future work, not planned for the MVP).

### Jira membership is not GitHub membership

> **Known limitation.** No cross-system identity check is performed between Jira and GitHub. The Jira project is the entire authorization boundary for Jira-sourced events.

The role resolved above feeds directly into the dispatch authorization gate (see [Architecture](../../architecture.md#agent-dispatch-and-coordination-layer)) — there is no separate check against the target GitHub repo's actual collaborators. This means anyone holding a Jira project role that maps to `write` (Jira's default "Developers" role, by default) can trigger write-gated slash commands like `/fs-code` against your repo, **even if that person has no GitHub access to it at all**.

The same holds for a [GitLab target](#gitlab-runtime): the agent job skips its GitLab Developer-access check for Jira dispatches, so project membership in GitLab is not checked either.

If your Jira project's membership is broader than your GitHub repo's collaborator list — which is common, since the two are usually administered separately — treat that gap as real: anyone in that gap can use their Jira membership alone to induce agent-proposed changes to your repository. Before enabling this driver, check who holds `write`-mapped roles in the target Jira project and make sure you're comfortable with each of them being able to do that.

The same applies to the `jira` lists and to label triggers. Under a `jira`
entry's default `labels: jira_editors`, anyone with Edit Issues on the project can
add a label such as `ready-to-code` and dispatch the agent whose trigger
matches it. Use `labels: allowlist` if that group is broader than you want.

## Poll coordination

The poller uses Jira entity properties for distributed lock coordination and checkpoint tracking, following a write-then-verify protocol. Each cycle randomly selects up to N (default 5) issues from the candidate set for processing, spreading load across cycles. Two properties are stored on each processed issue:

- **Lock** (`fullsend.poll.{owner}.{repo}.lock`) — prevents concurrent pollers from *detecting changes on* the same issue simultaneously. The lock covers the change-detection window only: it is released when an issue finishes processing, before the dispatch records are written and consumed by the downstream dispatch step, and it is not renewed while an issue is being processed — a cycle that stalls longer than the stale threshold (15 minutes) can have its lock reclaimed by a concurrent poller, which may produce duplicate dispatches. Lock ownership through dispatch scheduling is part of the same tracked follow-up as dispatch confirmation.
- **Last check** (`fullsend.poll.{owner}.{repo}.lastCheck`) — tracks the timestamp of the most recent processed change. Only changes newer than this timestamp trigger agent dispatch.

These properties are namespaced per target repo, so multiple repos can poll the same Jira project without interference. GitLab targets use a separate `fullsend.poll.gitlab.*` namespace that also includes the GitLab instance (see [GitLab runtime](#gitlab-runtime)). The properties are visible in Jira's issue properties API but do not appear in the issue UI.

> **Security note — coordination state is tamperable by any project editor.** Writing an issue entity property requires only Jira's **Edit Issues** permission, which is typically far broader than the project roles this driver maps to `write`. That means anyone who can edit a polled issue can write its `lastCheck` and lock properties. The driver treats both as untrusted: a `lastCheck` dated in the future is ignored (it would otherwise suppress all detection on the issue), and one rewound before the backfill window is clamped to the window's start, so a rewind can replay at most one backfill window of history rather than the whole issue — but within that window it can still re-surface old comments. Per-issue dispatch is capped to bound the blast radius. Even so, **treat Edit-Issues on a polled project as part of the trust boundary**: prefer scoping the poll to a project whose editors you trust, and remember that the authorization gate keys on the *comment's* Jira role, so who can edit issues (and comments) matters as much as who holds `write`.

Three edge cases of checkpoint tracking are worth knowing:

- **First enable on an existing backlog.** Every issue starts with an unset `lastCheck`, so on the first poll of an issue the poller has no checkpoint to filter by. Activity is instead bounded by a fixed 24-hour backfill window (not currently configurable): an `opened` event is emitted only for issues created within the window, and only comments and changelog entries from within the window produce events. Backlog issues with no activity (including comment edits) inside the window are silently checkpointed without dispatching. If you want the initial rollout scoped even tighter, narrow the candidate set with a custom `--jql`.
- **Resuming after a gap longer than 24 hours permanently drops events in the
  gap.** The same 24-hour backfill window that bounds a first poll also bounds
  resumption. When the poller reads a stored `lastCheck` older than
  `now − 24h`, it clamps the checkpoint forward to `now − 24h` — the clamping
  is a security control (it prevents a rewound `lastCheck` from replaying an
  issue's entire history), but it cannot distinguish a malicious rewind from a
  legitimate long stop. Any Jira activity between the true checkpoint and the
  clamped value is silently skipped. The cycle succeeds, the only evidence is
  a `WARNING: lastCheck for <KEY> ("<timestamp>") predates the backfill window; clamping to <floor>`
  line in the run log — treat this as an alert, not noise. Because detection
  is checkpoint-based, ordinary cron jitter and occasional skipped ticks are
  harmless (schedule accuracy affects latency, not correctness); the guarantee
  simply ends at 24 hours. A realistic trigger: **GitHub disables scheduled
  workflows after 60 days of repository inactivity**, which stops the poller
  with no error anywhere. After a long gap, the starvation limit (M = 50, see
  below) compounds the problem — many issues may have accumulated new activity
  simultaneously, and there is no rotation across cycles to reach issues
  beyond the top M. If your deployment cannot tolerate the 24-hour bound, the
  scheduler is pluggable: an external cron or Kubernetes CronJob that runs
  independently of GitHub Actions avoids the auto-disable risk.
- **Comment edits count as new activity, and are attributed to the editor.** The poller filters comments on the later of their created and updated timestamps, so editing a comment (for example, adding a slash command to an old comment) is detected on the next cycle. Jira bumps a comment's updated timestamp on modifications other than body edits too (such as visibility changes), and any such bump counts as activity. The flip side: modifying a comment whose slash command was already dispatched makes it look new again and can re-dispatch it. An edit-detected comment is attributed to the account that last modified it (Jira's `updateAuthor`), not the original author — so a user who edits someone else's comment to inject a command is authorized on *their own* role, not the original author's.

Because each cron run is a fresh process, per-cycle Jira API cost is worth sizing for: a cycle loads the project's role structure (a role-list call plus one detail call per role) only when some selected issue has an event that needs project-role resolution (no `jira` entry, `labels: project_roles`, or a slash command when the `jira` entry sets no lists), then, in such a cycle, makes one additional call for each distinct actor below the highest role priority the first time they're seen that cycle, to check their group memberships (cached per actor for the rest of the cycle, but not across cron runs). Cost scales with the number of distinct commenting actors per cycle rather than the size of the groups backing a role.

Two more scaling limits:

- **Issues beyond the top M candidates can be starved.** Each cycle's JQL search returns at most M (default 50) candidates, ordered by `updated DESC`. If a project has more than M issues with in-scope activity at once, whichever issues aren't in that top-M window this cycle are simply never selected — there's no rotation across cycles to eventually reach them. This is expected to self-resolve for most projects (an issue with new activity moves toward the front of `updated DESC` on its own), but a project that consistently has more than M simultaneously-active issues will have some starved indefinitely. Narrow the default `--jql` (e.g. scope to a label or a subset of issue types) if this applies to you.
- **Comments are re-listed in full, oldest-first, every cycle.** The Jira REST API has no way to filter comments server-side by date, so `ListComments` always paginates from the start of an issue's comment history; the poller then filters client-side by timestamp. For issues with very long comment histories, this means re-fetching (and re-decoding) old comments every cycle just to discard them. Listings are capped at 10,000 entries per issue; an issue beyond that cap has its newest activity invisible to the poller (a WARNING is logged when the cap is hit). Each API response is bounded to 10MB, but the accumulated per-issue comment history is bounded only by the page cap — a per-issue byte budget is a tracked follow-up; against real Jira Cloud (which caps individual comments) the realistic worst case is tens of MB per issue.
- **Sustained Jira errors can stall a cycle.** Each Jira call retries up to 5 times with exponential backoff, honoring `Retry-After` (capped at 5 minutes). Under a sustained 429 or outage a single cycle can therefore run long; the workflow's concurrency group queues subsequent cron runs rather than stacking them, and a failed cycle simply retries from its checkpoints on the next run.

## Multi-repo polling

Multiple GitHub repos can poll the same Jira project. Each repo runs its own poll workflow and gets an isolated view of coordination state — no cross-repo configuration is needed. Before adding pollers, use Jira Components or labels to segment your project so each repo's `--jql` filter only matches issues relevant to it — this avoids redundant API calls and keeps each poller's candidate window focused.

### Should each repo have its own poll workflow?

Yes. Each repo should contain its own `.github/workflows/fullsend-poll-jira.yml`. The `--target-repo` flag passed to `fullsend poll` (defaulting to `${{ github.repository }}`) controls two things:

1. **Entity property namespace.** Lock and checkpoint properties are keyed as `fullsend.poll.{owner}.{repo}.*`, so repos `acme/frontend` and `acme/backend` polling the same Jira project write to separate property keys on each issue and never interfere with each other.
2. **GHA concurrency group.** The workflow's `concurrency.group` is scoped to the workflow file, which lives in the repo — so each repo's poll cycles queue independently.

This means each repo discovers and dispatches changes on its own schedule, with its own credentials and its own `--jql` filter. There is no shared state between repos.

### How entity property locks namespace across repos

The poller stores two properties per issue, both namespaced by the target repo's `{owner}/{repo}` slug:

| Property | Key pattern |
|---|---|
| Lock | `fullsend.poll.{owner}.{repo}.lock` |
| Checkpoint | `fullsend.poll.{owner}.{repo}.lastCheck` |

When `acme/frontend` and `acme/backend` both poll issue `PROJ-42`:

- `acme/frontend` reads and writes `fullsend.poll.acme.frontend.lock` and `fullsend.poll.acme.frontend.lastCheck`.
- `acme/backend` reads and writes `fullsend.poll.acme.backend.lock` and `fullsend.poll.acme.backend.lastCheck`.

The two repos lock independently, checkpoint independently, and can process the same Jira comment — each dispatching to its own agent workflows. This is intentional: the same `/fs-triage` comment on a Jira issue may be relevant to multiple repos if their harness triggers match.

### 50-candidate cap with multiple repos

Each repo's poller runs its own JQL query and gets its own top-M (default 50) candidate window. The cap is per-poller, not global — three repos polling the same project each see up to 50 candidates per cycle, not 50 shared across all three.

However, more pollers against the same Jira project means more API calls per cycle. Each poller independently reads entity properties for lock filtering, fetches changelogs, and paginates comments. For large multi-repo deployments, consider:

- **Narrowing `--jql` per repo** to reduce candidate overlap. Use Jira Components or labels to segment the project (see the recommendation above), then scope each repo's query — e.g., `component = Frontend` or `labels = frontend` — so it skips issues that belong to other repos.
- **Staggering cron schedules** so pollers from different repos don't hit the Jira API simultaneously. For example, offset each repo's schedule by one or two minutes.
- **Monitoring Jira rate limits.** Jira Cloud uses points-based quotas, and multiple pollers multiply the cost. Watch for `429` responses in workflow logs — the poller retries with backoff, but sustained rate limiting slows all repos' cycles.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| 401 on all Jira API calls | Invalid token | Regenerate the API token and update the `JIRA_TOKEN` secret |
| 200 on `/myself` but 403 on issue search | Org restricts personal API tokens for project data | Ask your Atlassian org admin to allow API token access for project data |
| No dispatches produced | No changes since last poll | Check the `lastCheck` entity property on the issue — the poller only dispatches for changes newer than this timestamp |
| Agent comments or label changes re-dispatch in a loop | A different Jira account posted the agent output than the one the poller authenticates as | The poller classifies its own `/myself` account as `actor.kind: bot` and filters those events. Confirm `JIRA_USER_EMAIL` / `JIRA_TOKEN` are the same account that posts comments and labels. Human follow-up, including `/fs-triage`, still dispatches |
| `load project roles for PROJ: ... 401 You cannot edit the configuration of this project` | The poller read Jira project roles without Administer Projects. That happens with no `jira` entry in `.fullsend/config.yaml`, with `labels: project_roles`, or on a slash command when the entry sets no lists | Add a `jira` entry with `write`/`triage` lists so project roles are not read, or grant the service account the **Administrators** project role. See [Minimum Jira permissions](#minimum-jira-permissions) |
| `resolve jira list groups: ... get user groups` errors | The `jira` lists name groups, or project-role resolution found a role granted to a group, but the service account lacks the Browse users and groups global permission | Grant Browse users and groups, or list account IDs instead of groups |
| Slash command ignored (with `jira` lists) | Actor is not in the `write` list (or the `triage` list for triage-level agents) | Add the actor's account ID or one of their groups to the right list — see [Actor role resolution](#actor-role-resolution) |
| Slash command ignored (without `jira` lists) | Actor lacks `write` role in Jira project | The actor must be a member of a Jira project role named exactly "Developers" or "Administrators" — see [Project role mapping](#project-role-mapping) if you use custom role names |
| Issue opened or edited on Jira never dispatches triage | A `jira` entry is present but sets no `write` or `triage` list and not `labels: project_roles`, so opened and edited events are unresolved (with no `jira` entry, or with `labels: project_roles` and no lists, they resolve through project roles) | Add `write`/`triage` lists to the entry, or set `labels: project_roles`. See [Actor role resolution](#actor-role-resolution) |
| Label added but nothing dispatched | There is no `jira` entry, or it sets `labels: allowlist` or `labels: project_roles`, and the label author is not authorized by it | Add the author to the lists, or add a `jira` entry with the default `labels: jira_editors` |
| Slash commands silently ignored when using `--jql` | `--jira-project` not provided and no `jira` lists — all actors resolve to `external` and fail the role gate | Add `--jira-project PROJ` alongside `--jql` in your workflow file, or configure the `jira` lists |
| Duplicate dispatches | `lastCheck` was cleared or missing | The poller treats a missing `lastCheck` as "never polled" and processes all recent changes. This is self-correcting — the next cycle advances `lastCheck` past the duplicates |
| Old comment or label change on a newly polled issue never dispatches | Activity predates the first-poll backfill window | On an issue's first poll, activity older than the 24-hour backfill window is permanently skipped (the checkpoint advances past it). To pick up older activity, scope the initial `--jql` to recently updated issues and widen it gradually, or have someone re-trigger by commenting again |
| Activity during a long outage or disabled workflow is missing after resume | Poller gap exceeded the 24-hour backfill window | The same 24-hour window that bounds a first poll also bounds resumption — any `lastCheck` older than 24 hours is clamped forward. Check the run log for `predates the backfill window` — if present, events in the gap are permanently lost. Have affected users re-trigger by commenting again. See [Poll coordination](#poll-coordination) |
| GitLab Jira dispatch fails with `does not accept Jira work items` | The project's scaffold predates Jira dispatch | Rerun `fullsend repos install` for the project and merge the scaffold update. See [GitLab runtime](#gitlab-runtime) |
| Triage or code results do not appear on the Jira issue | The built-in agents are pinned to a release that predates Jira result writes | Use built-in agents from the agents release that follows v0.52.0, or later. Re-pin a harness registered with `source:` or composed with `base:` with `fullsend agent update <name>`. See [Where built-in agent results appear](#where-built-in-agent-results-appear) |
| Code step fails after the PR or MR was created | The Jira write of the code-stage update failed | Check the step log for the Jira error, and confirm `JIRA_USER_EMAIL`, `JIRA_TOKEN`, and `JIRA_BASE_URL` reach the agent job and the account can comment on the issue. A re-run edits the existing update rather than posting a second one |

## See also

- [CEL Triggers Reference](cel-triggers-reference.md) — NormalizedEvent fields and routing rules
- [Bring Your Own Agent](bring-your-own-agent.md) — adding custom agents
- [Configuring agent behavior](customizing-agents.md) — harness configuration
- [Issue commands](issues-commands.md) — `fullsend issues get`, `create`, and `post-comment`, including Jira
