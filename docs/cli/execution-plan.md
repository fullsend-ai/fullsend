---
sidebar_label: execution-plan
---

# fullsend execution-plan

Advance a bounded group of source-bound executions once. This experimental
command persists attempts before dispatch and reconciles existing attempts
before scheduling more work. A trusted host driver supplies authorization and
execution. Fullsend supplies plan identity, concurrency limits, retry accounting
and aggregate completion.

```sh
fullsend execution-plan \
  --plan /srv/fullsend/plan.json \
  --state-dir /srv/fullsend/state \
  --driver /opt/fullsend/plan-driver
```

| Flag | Required | Meaning |
| --- | --- | --- |
| `--plan` | Yes | Reviewed immutable plan JSON, within 4 MiB |
| `--state-dir` | Yes | Private persistent directory for checkpoints and locks |
| `--driver` | Yes | Absolute path to an operator-controlled executable |
| `--driver-timeout` | No | Timeout per call; default `30s`, maximum `5m` |

The command prints `{"complete":false,"state":{...}}` or a completed state.
A successful exit means reconciliation finished. Check `complete` to determine
whether all required executions succeeded. Authorization blockers, exhausted
attempts and outstanding work keep completion false. Errors exit nonzero;
previous checkpoints remain available for recovery.

State and stdout contain plan inputs, domain context and execution receipts.
Keep them in restricted storage. Supply credentials through the driver's
protected environment, never through plan fields.

## Plan input

```json
{
  "version": 1,
  "id": "advisory-123",
  "request": "advisory-123",
  "authorization": "reviewed-installation-grant",
  "harness": "assessment",
  "control_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "deadline": "2099-01-01T00:00:00Z",
  "max_parallel": 2,
  "max_attempts": 2,
  "targets": [
    {
      "id": "release-1",
      "forge": "github",
      "host": "github.com",
      "repository": "example/project",
      "ref": "refs/heads/release-1",
      "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  ]
}
```

Replace illustrative identities and SHAs with reviewed real inputs. The
`authorization` field names a grant the driver verifies; writing a value here
cannot grant repository access. `control_sha` identifies trusted configuration
separately from the source under assessment.

Each target has a stable ID within the plan and an exact source SHA. `forge`
accepts `github`, `gitlab` or `forgejo`; `host` distinguishes installations.
GitLab repository paths may include subgroups. `ref` is a full branch ref.
Optional `context` carries at most 64 KiB of JSON domain data. Drivers treat it
as data, with no authority to choose credentials or executable commands.

Initial limits allow 1–256 targets, parallelism of 1–32 and 1–10 attempts per
target. These ceilings bound this experimental contract's work and state;
revisit them with measured installation needs before expanding the contract.
The deadline prevents new attempts while existing work remains reconcilable.
A failed attempt consumes its budget; an uncertain attempt consumes both an
attempt and a concurrency slot.

A plan is immutable, including its targets, order, deadline and limits. Reusing
its ID with changed input fails. Supersession and migration of consumed budgets
are not implemented. Preserve existing state; changing IDs to bypass limits is
not a recovery procedure.

## Storage and execution scope

The file store uses a context-aware OS lock and atomic private checkpoints.
It supports processes sharing one persistent filesystem with working `flock`
semantics. The parent of `--state-dir` must already exist durably. Protect the directory
and all its ancestors from untrusted writes.
Separate CI machines need a shared, fenced storage adapter. Copying state files
between jobs does not provide distributed coordination.

Native forge launch adapters, automatic source checkout and integration with
existing dispatch workflows are not included. Forge names identify targets;
they do not establish that an installed driver supports those targets. The
operator must install a driver that verifies authorization, source checkout
and trusted configuration for every enabled forge.

See [building a plan driver](../guides/dev/execution-plan-driver.md) for the
protocol and CVE workflow migration boundary.
