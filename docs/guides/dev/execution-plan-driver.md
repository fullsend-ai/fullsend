---
title: Build an execution plan driver
---

# Build an execution plan driver

Fullsend owns a plan's durable attempts and accounting. A driver connects those
attempts to an execution backend. Domain mapping and assessment remain outside
the coordinator. See the [command reference](../../cli/execution-plan.md) for
plan fields, flags, limits and current storage restrictions.

## Prerequisites

- An operator-controlled executable, working directory and environment.
- A protected persistent state directory shared by all local reconcilers.
- A source-bound single-target worker and authoritative backend lookup.
- Reviewed authorization policy covering each target and requested harness.

Keep driver code outside the source checkout being assessed. A target or model
response cannot select the driver, its credentials or trusted configuration.
Go implementations of forge operations must use `forge.Client`.

## Implement the protocol

1. Read exactly one JSON request from stdin. Fullsend invokes the executable
   directly with no arguments or shell expansion. Each request contains
   `version: 1`, `operation`, the complete `plan`, and either `target` for
   authorization or `attempt` for submission and lookup.
2. Handle `authorize` by verifying the current grant, target access, harness
   permission and trusted control revision. Return `{"authorized":true}` only
   after those checks. Errors and missing approval hold the target with an
   authorization blocker. Recheck policy inside the launch boundary when the
   backend cannot atomically preserve the prior decision.
3. Handle `submit` by launching one exact attempt. Verify repository, branch
   and SHA against the admitted target. Keep trusted configuration at
   `plan.control_sha` separate from the assessed source. The attempt ID is the
   correlation key. Record it where authoritative backend lookup can recover
   an accepted launch after its acknowledgement is lost.
4. Handle `lookup` by observing that exact attempt without creating work. An
   absent receipt means the outcome remains unknown. An empty search result
   cannot authorize a second submission. Return a terminal receipt only when
   the external run has stopped consuming execution capacity.
5. Print one JSON response to stdout, within 1 MiB. Fullsend rejects unknown
   response fields and trailing JSON. Driver stderr is discarded to avoid
   retaining credentials from external diagnostics. Honor process cancellation.

For submit and lookup, the response contains an optional `receipt`:

```json
{
  "authorized": false,
  "receipt": {
    "attempt_id": "the-exact-request-attempt-id",
    "plan_digest": "the-exact-request-plan-digest",
    "target_id": "release-1",
    "source_sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "run_id": "authoritative-backend-run-id",
    "state": "running"
  }
}
```

Receipt states are `running`, `succeeded` and `failed`. The `authorized` field
is used only by authorization calls. For an unresolved lookup, return
`{"authorized":false}`. An accepted attempt's backend run ID cannot change.

Fullsend persists an `uncertain` attempt before calling submit. A timeout,
process loss, invalid receipt or lost response leaves the attempt reserved.
Later reconciliation calls lookup rather than submit. A confirmed failure can
start a new numbered attempt within the original budget. A successful attempt
is retained and not rerun. Each invocation launches at most one new attempt per
target.

## Keep findings separate from execution

A successful run means the driver accepted a completed worker result. Domain
consumers still validate assessment evidence and interpret affected,
unaffected or inconclusive findings. Fullsend plan completion grants no coding,
publication, merge or tracker-closure authority. Drivers must not return success
merely because CI exited zero or a tracker exists.

## Adapt a CVE workflow

1. Resolve reviewed release mappings into targets. Put public-advisory identity,
   component and evidence-bundle descriptors in domain context. Retain disclosure
   and destination policy in the trusted CVE integration.
2. Translate an admitted manifest into one immutable execution plan. The
   coordinator owns dispatch, attempt budgets, concurrency and completion.
3. Connect the driver to a single-target assessment harness. Validate its
   source-bound assessment receipt before reporting execution success. Keep
   Jira investigation creation and human coding approval in the CVE workflow.
4. Prove lost-acknowledgement recovery and branch isolation before enabling
   recurring execution. Keep existing journal records and consumed budgets.

> **Planned:** Native CI launch adapters, a shared fenced journal, and budget-
> preserving supersession remain integration work tracked with
> [#9260](https://github.com/fullsend-ai/fullsend/issues/9260). This draft
> coordinator does not migrate an existing CVE installation or its journal.

The coordinator contract covers targets on GitHub, GitLab and Forgejo. Each
installed driver must explicitly support its selected forge and host or deny
execution. An installation that runs the controller on GitLab and assesses
GitHub source does not need to use the same forge for both roles.
