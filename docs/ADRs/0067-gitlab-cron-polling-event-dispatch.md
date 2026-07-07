---
title: "67. GitLab cron-polling event dispatch"
status: Accepted
relates_to:
  - agent-infrastructure
  - gitlab-implementation
  - security-threat-model
topics:
  - gitlab
  - forge
  - ci-cd
  - per-repo
  - polling
  - cron
  - dispatch
  - drivers
---

# 67. GitLab cron-polling event dispatch

Date: 2026-06-13

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Fullsend needs to detect and react to GitLab events — new issues, merge
requests, comments, and label changes — so that agent stages (triage, code,
review, fix, retro) can be dispatched automatically. On GitHub, native event
triggers (`pull_request_target`, `issues`, `issue_comment`) handle this within
GitHub Actions. GitLab has no equivalent for most event types.

GitLab's CI/CD pipeline trigger sources are: `push`, `merge_request_event`,
`schedule`, `trigger`, `web`, `api`, and `parent_pipeline`. Of these, only
`merge_request_event` maps to an agent-relevant event. Issue creation, comment
posting, and label changes have no native CI pipeline trigger. GitLab supports
per-repo installation mode only (no per-org); the pipeline runs inside the
enrolled project on the protected default branch.

See [ADR 0028](0028-gitlab-support.md) for the original GitLab
support architecture discussion. ADR 0028 documented a webhook bridge approach;
this ADR supersedes that direction based on the operational complexity analysis
in Options 1–3 below. [ADR 0045](0045-forge-portable-harness-schema.md)
defines the forge-portable harness schema that GitLab stage templates must
conform to.

## Options

### Option 1: Webhook bridge Cloud Function

Deploy a GCP Cloud Function that receives GitLab webhook POST requests,
validates the `X-GitLab-Token` header, and calls the Pipeline Trigger API to
dispatch agent stages.

**Rejected.** Requires external infrastructure (Cloud Function) that must be
deployed, monitored, and secured. Exposes a public HTTPS endpoint — an inbound
attack surface. Requires three credential types per project (bot PAT, webhook
secret, trigger token). Creates a complex deployment story for self-hosted
GitLab behind corporate firewalls (VPN peering, on-premise containers, or
Cloud Run + VPC Connector). The bridge cannot be eliminated even in a hybrid
model — if any event type uses webhooks, the full bridge must be deployed.

### Option 2: Webhook-only (all events via bridge)

Use the webhook bridge for all events, eliminating native CI triggers.

**Rejected.** Still requires the bridge with all its operational complexity.
The correct response to "if we need webhooks for some events, why not all?" is
to eliminate the bridge entirely, not to double down on it.

### Option 3: Native merge request (MR) events + webhook bridge for issues/comments

Use GitLab's native `merge_request_event` for MR events, keep the webhook
bridge only for issues and comments.

**Rejected.** Still requires the bridge Cloud Function. The bridge's
operational cost is dominated by deployment, monitoring, and credential
management — not by event type count.

### Option 4: Pure cron-polling (no native CI triggers)

Poll for all events including MR creation and updates.

**Rejected.** MR events have a viable native CI path (`merge_request_event` +
`include: local:`) with sub-minute latency and zero additional infrastructure.
Polling for MRs adds unnecessary latency to the most frequent, most
latency-sensitive operation (code review).

## Decision

GitLab event dispatch uses a **two-path model**:

1. **Native CI triggers for MR events.** MR creation, update, and reopen
   trigger pipelines via GitLab's `merge_request_event` pipeline source.
   MR merge does **not** fire `merge_request_event` — GitLab creates a
   `push` event on the target branch instead, so MR merge detection uses
   the cron-poller (see event routing table below). The dispatch template
   is loaded via `include: local:` from the protected default branch,
   ensuring untrusted MR branches cannot modify dispatch logic.

2. **Cron-polled events for everything else.** A scheduled pipeline runs every
   N minutes (5 minutes on Premium/Ultimate, 60 minutes on Free tier), queries
   the GitLab API for new issues, comments, and label changes since the last
   poll, and dispatches agent stages via parent-child pipelines.

No external infrastructure is required for event dispatch — no webhook bridge,
no webhook secrets, no trigger tokens.

### Relationship to the dispatch driver architecture

[ADR 0061](0061-harness-cel-dispatch.md) defines the dispatch pipeline:
`input driver → authorize → enumerate harnesses → CEL triggers → output driver`.
This ADR implements a **`gitlab-poll` input driver** for `fullsend poll` and a
**child-pipeline output driver** for GitLab CI, following the same composition
as other poll input drivers (e.g. `jira-poll`):

```
gitlab-poll input driver → per-event coordination → dispatch core → child-pipeline output driver
```

The `gitlab-poll` input driver discovers events and emits
[`NormalizedEvent`](../normative/normalized-event/v1/) values. The **dispatch
core** — authorization ([ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md))
and harness CEL `trigger` evaluation ([ADR 0061](0061-harness-cel-dispatch.md))
— is shared with `fullsend dispatch` and other poll input drivers. The poll
input driver does not duplicate trigger routing or authorization logic.

```
ENROLLED PROJECT                           GCP (optional, WIF mode only)
────────────────                           ────
.gitlab-ci.yml (root pipeline)             WIF pool/provider (validates GitLab OIDC)
.gitlab/ci/fullsend-dispatch.yml (MR routing)  Service Account (impersonated by jobs)
.gitlab/ci/fullsend-poll.yml (cron-poller)     Secret Manager:
.gitlab/ci/fullsend-triage.yml … retro.yml       - bot PAT per enrolled project
.fullsend/ (config workspace)

MR events (native CI):
  MR opened/updated/reopened → merge_request_event → fullsend-dispatch.yml → review stage

MR merge (cron):
  Pipeline schedule → fullsend-poll.yml → MR merged_at > watermark → retro stage

Issues, comments, labels (cron):
  Pipeline schedule (5 min) → fullsend-poll.yml → GitLab API → dispatch agent stage

Credentials (WIF mode):
  Pipeline job → OIDC token → GCP STS → WIF → SA → Secret Manager → bot PAT

Credentials (variable mode):
  Pipeline job → protected CI/CD variable FULLSEND_FORGE_TOKEN → bot PAT
```

### Credential model

A Maintainer-role project access token with `api` scope, created during
`fullsend admin install`. Maintainer role is required because the poller
updates CI/CD variables (watermark and label state persistence) via the
API, which requires Maintainer-level access. Two storage modes are
supported:

**Mode 1: OIDC/WIF (recommended).** The bot PAT is stored in GCP Secret
Manager and retrieved at runtime via GitLab OIDC → GCP WIF. No secrets
are stored as CI/CD variables. This is the recommended mode when GCP
infrastructure is available (e.g., projects already using Vertex AI for
inference).

**Mode 2: Protected CI/CD variable (fallback).** The bot PAT is stored
as a protected, masked CI/CD variable (`FULLSEND_FORGE_TOKEN`). No GCP
infrastructure required. This is the default mode for environments
without GCP access, including self-hosted GitLab instances with no cloud
dependency.

The install flow selects the mode automatically: if `--gcp-project` is
provided, OIDC/WIF is configured; otherwise, the CI/CD variable path is
used. A `FULLSEND_CREDENTIAL_MODE` protected variable (`wif` or
`variable`) tells pipeline templates which retrieval path to execute.

Key properties shared by both modes:

- **Single credential type.** One bot PAT per project handles all REST and
  GraphQL operations. No webhook secrets, trigger tokens, or mint service.
- **Bot identity.** The project access token creates a dedicated bot user,
  providing attributable identity equivalent to GitHub Apps.
- **GraphQL support.** Unlike `CI_JOB_TOKEN`, the bot PAT authenticates
  GraphQL — required for GitLab's Work Items API.

OIDC/WIF mode additionally provides:

- **`CI_DEBUG_TRACE` defense-in-depth.** GitLab logs all CI/CD variables
  at job initialization, *before* any script runs. In variable mode, a
  Maintainer enabling `CI_DEBUG_TRACE` exposes the PAT in job logs before
  the script-level guard can abort. In WIF mode, WIF configuration
  metadata (pool IDs, project numbers, service account emails) is logged
  but the PAT itself is not — it is retrieved later by `gcloud`, after the
  guard has already run. The metadata exposure is an accepted tradeoff:
  it reveals infrastructure topology but not credentials. This is the
  primary security difference between the two modes.
- **Cryptographic access control.** WIF attribute conditions restrict
  token retrieval to the enrolled project on protected branches
  (`assertion.project_id` + `assertion.ref_protected == "true"`).
- **Separation of administrative domains.** WIF configuration lives in
  GCP IAM, outside the GitLab Maintainer's control. A GitLab Maintainer
  cannot modify WIF attribute conditions without GCP IAM access.
- **No token mint.** Standard GCP WIF replaces the custom mint Cloud
  Function used for GitHub.

### Cron poller (`gitlab-poll` input driver)

The `gitlab-poll` input driver runs as `fullsend poll` inside the fullsend
container image, invoked by a scheduled pipeline on the protected default
branch. It reads a timestamp watermark, queries the GitLab API for events
since the last poll, emits a `NormalizedEvent` per detected change, passes
events to the dispatch core for authorization and harness CEL evaluation, and
advances the watermark. See the [companion implementation plan](../plans/gitlab-cron-polling-implementation.md)
for detailed pseudocode and numbered steps.

Change detection for labels uses client-side state diffing — the input driver
tracks previously-seen labels per issue and emits events only for newly-added
labels. This compensates for the lack of a `changes` object that webhook
payloads provide.

**Multi-frequency polling (Premium/Ultimate):** Two pipeline schedules — a
fast poll (every 5 minutes, slash commands only) and a slow poll (every 15
minutes, full event scan). On Free tier, a single hourly poll is the only
option. Each mode uses a separate watermark
(`FULLSEND_LAST_POLL_AT_FAST` / `FULLSEND_LAST_POLL_AT_FULL`) so that
fast polls do not advance past label/note events that only the full poll
handles. A consequence is that slash commands discovered by a fast poll
may be re-discovered by the next full poll. `resource_group`
serialization prevents concurrent execution, so a duplicate dispatch
queues behind the first. The second run executes the same stage
against already-processed state (the agent sees no new work) and
exits as a no-op, wasting one pipeline invocation's CI minutes.
This is an accepted tradeoff — the alternative (sharing a
processed-note-IDs set or cross-reading watermarks between modes)
adds state coupling that complicates the independent-schedule design.

### Event routing

The design goal is **functional event-type parity with GitHub** — users see the
same labels, slash commands, and stage dispatches regardless of forge (latency
differs: cron-polled events have 5–60 minute delay vs sub-second on GitHub).
Routing is performed by harness CEL `trigger` expressions
([ADR 0061](0061-harness-cel-dispatch.md)) evaluated in the dispatch core, not
by the `gitlab-poll` input driver. The table below documents how each detected
change maps to a `NormalizedEvent` and transport path, not trigger
configuration.

| Detected Change | Transport | Stage |
|---|---|---|
| Issue label `ready-to-code` added | Cron poll (label state diff) | code |
| Issue label `ready-for-review` added | Cron poll (label state diff) | review |
| Issue note starting with `/fs-{triage,code,review,fix,retro,prioritize}` | Cron poll (note body prefix) | corresponding stage |
| Issue note (non-command) on issue with `needs-info` label | Cron poll (label check); Reporter+ or issue author | triage |
| MR opened/updated/reopened | Native CI (`merge_request_event`) | review |
| MR merged | Cron poll (MR `merged_at` > watermark) | retro |
| MR note with `<!-- fullsend:changes-requested -->` | Cron poll (note body marker) | fix (same-project MRs only) |

Bot-authored comments are skipped to prevent re-triggering loops (exception:
the `changes-requested` marker from the review agent).

### Slash command latency

Slash commands (`/fs-*`) are the only latency-sensitive operation. Mitigations:

- **Labels as primary triggers.** Applying `ready-for-review` or
  `ready-to-code` labels is discoverable and visible. Labels on issues are
  detected via cron-poll (5–60 minute latency); labels on MRs can also be
  detected via native CI `merge_request_event` when applied alongside an
  MR update.
- **Multi-frequency polling** keeps slash command latency to 5 minutes on
  Premium/Ultimate.
- **Manual pipeline trigger** via the GitLab UI as a power-user escape hatch.
- **Off-system polling** via `fullsend poll` on a standalone VM or Kubernetes
  CronJob, at any desired interval. This reintroduces external infrastructure
  but is architecturally simpler than a webhook bridge — see
  [GitLab tier considerations](#gitlab-tier-considerations) below for details.

**MR note limitation (fast-poll):** GitLab's `merge_request_event` pipeline
source fires on MR creation, update, and reopen — not on merge, close, or
individual MR comments. Comment-based triggers on MRs (`/fs-fix`, `/fs-code`) must
therefore use the cron-poller. Within the cron-poller, these commands on MR
notes are only acted upon during the full-poll cycle (every 15 minutes on
Premium/Ultimate), not the fast poll. The fast-poll path does not fetch MR
source/target project IDs, so the fork MR protection check (deny-by-default
when unknown) blocks these stages. This adds up to 10 minutes of latency
beyond the fast-poll interval. Fetching MR details per note in fast-poll
would add API calls that defeat its lightweight purpose. In practice, fix
stages are typically triggered by the review bot's `changes-requested`
marker (which uses the full-poll path), not human slash commands.

**Quick Action risk:** GitLab may silently strip unrecognized `/`-prefixed
lines. If confirmed empirically, GitLab should use an alternative prefix
(`fs:triage` or `@fullsend triage`). [ADR 0042](0042-fs-prefix-for-slash-commands.md)
permits forge-specific syntax.

### GitLab tier considerations

| Feature | Free | Premium | Ultimate |
|---|---|---|---|
| Schedule minimum interval | 60 min | 5 min | 5 min |
| Project access tokens (SaaS) | Not available | Available | Available |
| CODEOWNERS enforcement | Not available | Available | Available |
| CI minutes (shared runners) | 400/month | 10,000/month | 50,000/month |
| Parent-child pipeline nesting | 2 levels | 2 levels | 2 levels |

**Pipeline nesting:** The cron-poller uses exactly 2 levels of
`trigger: include:` child pipeline nesting — the GitLab maximum. The poll
runs inline in the root scheduled pipeline (no child pipeline). Level 1:
the root pipeline triggers a dynamically generated dispatch child pipeline
(via `trigger: include: artifact:`). Level 2: the dispatch child pipeline
triggers per-stage child pipelines (via
`trigger: include: .gitlab/ci/fullsend-{stage}.yml`). This is at the
nesting ceiling — no additional `trigger: include:` levels can be added
without restructuring. See
[GitLab CI/CD pipeline nesting](https://docs.gitlab.com/ee/ci/pipelines/downstream_pipelines.html#nesting).

**Free tier** is functional but degraded: 60-minute poll interval, no project
access tokens on gitlab.com (must use personal access token), no CODEOWNERS
guardrails, and CI minute quota is insufficient for polling on shared runners.
Self-hosted runners are required. As an alternative, Free tier users can run
`fullsend poll` on an external scheduler (cron on a VM, Kubernetes CronJob,
etc.) at any desired interval. This reintroduces external infrastructure but
is architecturally simpler than a webhook bridge — the poller is entirely
outbound (no public endpoint, no inbound payload parsing) and uses the same
code path as the in-CI poller.

**Premium** (recommended minimum): 5-minute polling, project access tokens,
CODEOWNERS enforcement, adequate CI minutes for a single project.

`fullsend admin install` adapts poll frequency and interaction model to the
detected tier.

### Security model

The security model follows the project's threat priority order (external
injection > insider > drift > supply chain):

- **No inbound attack surface.** Polling is entirely outbound — no public
  endpoint, no webhook parser, no shared-secret authentication.
- **Protected branch enforcement.** `workflow:rules` require
  `$CI_COMMIT_REF_PROTECTED == "true"` for scheduled pipelines.
- **Protected CI/CD variables.** All fullsend CI/CD variables are marked
  protected — accessible only to pipelines on protected branches.
- **`CI_DEBUG_TRACE` guard.** Install-time validation and runtime abort if
  debug tracing is detected. In **variable mode**, this guard is the sole
  defense against PAT exposure via debug tracing — GitLab logs CI/CD
  variables at job init, before any script runs. In **WIF mode**, the guard
  is defense-in-depth — even if bypassed, the PAT is not in a CI/CD
  variable and is retrieved after the guard runs. **Known limitation:**
  install-time validation checks project-level and group-level variables but
  cannot query instance-level CI/CD variables (requires admin API access).
  On self-hosted GitLab instances where instance admins are outside the
  trusted team, WIF mode is recommended.
- **Event data sanitization.** Attacker-controlled content is base64-encoded
  before passing to child pipelines.
- **Fork MR protection.** Fix/code stages are skipped when
  `source_project_id != target_project_id`.
- **Slash command authorization.** Only users with Developer-level (30+)
  project access can trigger agent stages via `/fs-*` commands.
  Exception: non-command comments on issues with the `needs-info` label
  trigger triage with a reduced authorization gate — the commenter must
  have at least Reporter-level (20+) project access or be the issue
  author. This mirrors the GitHub path where ADR 0054 requires
  `author_association != NONE` or issue authorship for non-command
  triage triggers, preventing unauthenticated cost exposure on public
  projects.

**Security comparison of credential modes:**

| Threat vector | WIF mode | Variable mode |
|---|---|---|
| `CI_DEBUG_TRACE` by Maintainer | PAT not exposed (defense-in-depth) | PAT exposed at job init before script guard runs (guard limits further damage but cannot prevent initial exposure) |
| Maintainer marks branch as protected | WIF grants token (same risk) | Variable exposed (same risk) |
| GitLab database compromise | PAT not in GitLab (in Secret Manager) | PAT stored in GitLab |
| Admin domain separation | WIF config requires GCP IAM | All within GitLab RBAC |
| Audit trail | GCP Data Access logs | GitLab audit logs (Premium+) |

WIF mode is recommended for projects where the Maintainer pool extends
beyond trusted team members, or where compliance requires external
secret storage.

### Forge abstraction

[ADR 0005](0005-forge-abstraction-layer.md) requires new forges to implement
`forge.Client`. This ADR extends the forge interface with new methods (some GitLab-specific, some forge-neutral):

- `IsProtectedBranch` — maps to GitHub branch protection API and GitLab
  protected branches API
- `CreatePipelineSchedule` / `DeletePipelineSchedule` — GitLab-native; GitHub
  returns `ErrNotSupported`
- `UpdateVariable` — for poll watermark management

A new `ErrNotSupported` sentinel (complementing the existing forge
sentinel errors) allows forge
implementations to reject inapplicable operations. GitHub-only methods
(`ListOrgInstallations`, `GetAppClientID`) move to a `GitHubExtensions`
extension interface. This requires interface evolution beyond pure
implementation — adding methods to `forge.Client` and refactoring
GitHub-specific methods into an extension interface. This is anticipated
growth of the abstraction boundary, not a violation of
[ADR 0005](0005-forge-abstraction-layer.md)'s design; the changes to
`appsetup.go` and `admin.go` are limited to calling new forge-neutral
methods rather than adding forge-conditional logic.

## Consequences

**What becomes easier:**

- **No external infrastructure for event dispatch.** No Cloud Function, no
  webhook bridge. Self-hosted GitLab requires only outbound HTTPS.
- **Single credential per project.** One bot PAT, stored in either GCP
  Secret Manager (WIF mode) or as a protected CI/CD variable (variable
  mode). No webhook secrets, trigger tokens, or mint service changes.
- **Stronger event authenticity.** Events read directly from the GitLab API,
  not from potentially spoofed webhook payloads.
- **No event loss.** Polling reads from the source of truth. Webhooks can fail
  silently or auto-disable after 4 consecutive failures.
- **Simpler emergency shutdown.** Disable the pipeline schedule or revoke the
  bot PAT. No bridge to tear down.
- **MR review latency is unaffected.** Native `merge_request_event` provides
  sub-second triggering for the highest-frequency operation.
- **Tier-adaptive.** Works on all GitLab tiers with graceful degradation.
- **No GCP requirement.** Variable mode allows deployment on self-hosted
  GitLab with no cloud dependency. WIF mode reuses GCP infrastructure
  already provisioned for Vertex AI inference.

**What becomes harder or changes:**

- **Issue/comment event latency.** Up to 5 minutes on Premium, 60 minutes on
  Free. Acceptable for asynchronous agent operations, poor for interactive use
  on Free tier.
- **CI minute consumption.** Polling runs continuously. At 5-minute intervals:
  ~8,640 min/month on shared runners. Self-hosted runners are not billed.
- **State management.** The poller must track watermarks, deduplicate events
  across overlapping windows, and diff label state. This state is internal
  to the GitLab forge implementation and does not leak into the
  `forge.Client` interface, preserving the forge-neutral contract from
  [ADR 0005](0005-forge-abstraction-layer.md).
- **Slash command latency.** Up to 5 minutes vs sub-second with webhooks.
  Labels mitigate this for common operations.
- **Quick Action stripping.** GitLab may strip `/fs-*` commands from comments.
  Requires testing and potentially alternative syntax.
- **Per-repo only.** No centralized config or credential management across
  projects.
- **`api` scope is broad.** Narrower scopes are not available in GitLab today.

**Risks** (ordered by threat priority):

1. **YAML injection in child pipeline generation.** Attacker-controlled
   issue/MR content could break child pipeline YAML syntax. Mitigated by
   base64 encoding of event payloads passed to child pipelines.
2. **Prompt injection via polled events.** Attacker-controlled issue/MR
   content reaches the agent at inference time. This risk is identical
   across all forges and is handled by the existing agent harness security
   layer, not by the transport mechanism.
3. **Watermark tampering.** A Maintainer could skip or replay events by
   modifying the watermark variables. Mitigated by protected variable status
   and event deduplication.
4. **Schedule modification.** A Maintainer could retarget the schedule to a
   non-protected branch. In WIF mode, mitigated by WIF attribute conditions
   rejecting credential retrieval. In variable mode, mitigated by protected
   variable status (not exposed on non-protected branches).
5. **Missed events from API quirks.** The Notes API lacks `created_after`; the
   Events API `after` parameter is date-only. Mitigated by 30-second watermark
   overlap and dual-frequency polling as reconciliation.

**Comparison with GitHub:**

| Concern | GitHub | GitLab (this ADR) |
|---|---|---|
| Primary credential | App installation token via mint | Bot PAT (WIF or CI/CD variable) |
| MR/PR event dispatch | `pull_request_target` | `merge_request_event` |
| Issue/comment dispatch | Native events (sub-second) | Cron polling (5 min) |
| External infrastructure | Mint Cloud Function | None for event dispatch |
| Credential types | App key + installation token | Single bot PAT |

Detailed implementation guidance — including poller pseudocode, forge interface
changes, CI/CD template scaffolding, and install flow — is in the companion
document: [Implementation plan: GitLab cron-polling](../plans/gitlab-cron-polling-implementation.md).

## References

- [ADR 0002](0002-initial-fullsend-design.md) — initial fullsend design (webhook + dispatch service, label state machine)
- [ADR 0033](0033-per-repo-installation-mode.md) — per-repo installation model (the only supported mode for GitLab)
- [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md) — authorization on all dispatch paths (slash command ACL)
- [ADR 0061](0061-harness-cel-dispatch.md) — harness CEL triggers, dispatch drivers, and NormalizedEvent schema
- [ADR 0063](0063-polling-based-work-discovery.md) — polling-based work discovery via dispatch drivers (`fullsend poll`, input/output driver architecture)
- [NormalizedEvent v1](../normative/normalized-event/v1/)
- [Implementation plan: GitLab cron-polling](../plans/gitlab-cron-polling-implementation.md)
