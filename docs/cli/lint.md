---
sidebar_label: fullsend lint
---

# fullsend lint

Lint `.fullsend` configuration and agent harness definitions. `fullsend lint` loads
`config.yaml` (layered over `config.base.yaml`), resolves and composes every enabled
agent's harness exactly as [`fullsend run`](run.md) does, and reports deprecations and
misconfigurations as diagnostics. It changes no configuration or forge state, but
resolving harnesses uses run's fetch machinery and can update the local cache and
audit log under `.fullsend-cache/`. `--offline` restricts network fetches, not
those writes.

## Usage

```bash
fullsend lint [agent-name] [flags]
```

With no argument, `fullsend lint` lints every enabled agent. With an agent name, only
that registered agent is resolved, composed, and linted; see
[Linting a single agent](#linting-a-single-agent).

## Flags

| Flag | Description |
|------|-------------|
| `--fullsend-dir` | Path to the `.fullsend` configuration directory (default `.fullsend`; when omitted and `.fullsend` does not exist in the current directory, the command stops and names the flag) |
| `--forge` | Forge platform to use (`github`, `gitlab`). When omitted, resolved from `forge:` in `config.yaml`, then `GITHUB_ACTIONS`/`GITLAB_CI` |
| `--offline` | Reject network fetches; only use cached remote resources |
| `--strict` | Exit non-zero on warning-level diagnostics (deprecations), not just error-level ones |

## Resolution and composition

Each enabled `agents:` entry is resolved and composed through the same machinery as
`fullsend run`:

- Local `source:` paths must stay inside the configuration directory; URL sources are
  fetched from the allowlisted hosts with caching.
- Override-only entries (settings only, `source:` unset) resolve their harness from the
  [agents repository](https://github.com/fullsend-ai/agents), exactly as run does —
  linting a built-in agent lints the fetched upstream harness. Under `--offline` these
  use the agents revision most recently cached by an online run in the same `.fullsend`
  directory. Run that online run with the same forge (`--forge` or CI auto-detection)
  as the offline run; otherwise the forge-specific files are not cached and the
  offline run fails.
- Harnesses are composed with `base:` chains, CEL-guarded overlays, and the forge
  section for the effective platform, so diagnostics see the harness as it would
  execute. Without a platform (`--forge` omitted, none in config, no CI env), the forge
  section is left unconsumed and its fields are linted in place.

Diagnostics for an override-only entry come from the upstream harness, not from your
`config.yaml`, so you cannot fix them in this repository.

Disabled agents (`enabled: false`) are skipped. Overlay `when` expressions evaluate
against an empty event with `runtime.forge` and `config` available, like other CLI
flows without event context.

## Linting a single agent

`fullsend lint <agent-name>` runs the per-agent steps (resolution, composition, path
and file validation, and diagnostics) for the named agent only. Use it while working
on one agent, so you do not have to fetch other agents or wait for them to be
checked. The name must match the name of an `agents:` entry, ignoring case.

- `config.yaml` is still validated. An invalid file (YAML that does not parse, an
  unsupported `version`, or an invalid setting such as an unknown `runtime`) fails the
  command, as it does without a name.
- A name that no `agents:` entry registers fails with an error listing the
  registered agents that can be linted.
- A disabled agent (`enabled: false`, including a later disable entry for the same
  name) fails with an error saying that the agent is disabled.
- Errors in the selected agent's own `agents:` entries or harness are error-level and
  fail the command.
- Invalid `agents:` entries for other agents do not stop the selected agent from being
  linted. Instead, one warning on the `agents` field gives their count and suggests
  running `fullsend lint` to inspect them. Like other warnings, it fails the command
  only with `--strict`. Only the other agents' `agents:` entries are checked, and their
  harnesses are not resolved, so a problem that only resolution finds (for example a
  missing harness file) appears only when you run `fullsend lint` without a name.

Run `fullsend lint` without a name, for example in CI, to check every agent.

## Path and file validation

After composition, lint applies run's path resolution and file checks: relative paths
must stay inside the configuration directory, referenced local files must exist, and a
`policy:` is required. A `${VAR}` reference to a variable run always refuses (OIDC
credential and provider-only keys) fails lint; other variables are assumed to be set at
run time. As in run, a harness's `allowed_remote_resources` must be covered
by the `allowed_remote_resources` in `config.yaml`, and every URL reference must match
one of the two lists. Local OpenShell profiles and provider definitions are parsed as
run parses them, so a missing or malformed one fails lint. `${VAR}` paths (expanded at
run time) are skipped, and URL references are checked against the allowlists but not
fetched — lint validates what is on disk.

## Diagnostics

Each diagnostic prints as `<agent>: <severity>: <field>: <message>`; config-level
diagnostics use `config` as the context. Values that arrive
via base, overlay, or forge composition are reported at the merged top-level fields
(`env.runner`, `env.sandbox`, `runner_env`); unconsumed forge sections report their own
position (`forge[<name>].…`).

| Field | Severity | Message |
|-------|----------|---------|
| `runner_env` | warning | `runner_env` is deprecated; use `env.runner` instead ([ADR 0055](../ADRs/0055-unified-env-var-delivery.md)) |
| `env.sandbox` | warning | `env.sandbox` coexists with a `host_files` entry delivering `.env.d/` files; `env.sandbox` values take precedence on key collision |
| `allowed_remote_resources` | warning | `allowed_remote_resources` is declared without `allow_runtime_fetch: true`; the implicit runtime fetch service start is deprecated — add `allow_runtime_fetch: true` to silence it |
| `forge` | warning | `forge` is deprecated; use overlays with CEL `when` expressions instead ([ADR 0088](../ADRs/0088-cel-guarded-overlays.md)) |
| `validation_loop.preflight_check` | error | The value looks like a bare script path (for example `scripts/common-preflight.sh`) rather than a self-contained command; `preflight_check` runs verbatim as a `sh -c` command on the host and is never fetched or resolved like `pre_script`/`post_script` — if a fetched harness script was intended, use `pre_script`; otherwise inline the command (for example `python3 -c 'import jsonschema'`) |
| `runner_env`, `env.runner`, `env.sandbox`, `host_files[<n>].src`, `forge[<name>].…` | warning | An env entry named `GITHUB_ISSUE_URL`, or a value or `host_files` source referencing it (`$GITHUB_ISSUE_URL` / `${GITHUB_ISSUE_URL}`), which is deprecated; use `FULLSEND_WORK_ITEM_URL` instead |
| `agents` | warning | `fullsend lint <agent-name>` only: `<n> other agents have lint errors; run fullsend lint to inspect them` — other agents' `agents[]` entries fail validation (see [Linting a single agent](#linting-a-single-agent)) |
| `config.yaml` | error | Semantic validation of `config.yaml` fails — for example an unsupported `version`, an unknown `runtime`, or an `agents[]` entry whose `trigger` does not compile or does not return a boolean. Merged `agents[]` entries and model aliases are validated, including those inherited from `config.base.yaml`; other inherited settings (such as `version` or `runtime`) are not. Errors are labelled `config.yaml` even when the entry comes from the base file. When an `agents[]` entry is invalid, no agent harness is resolved, because every agent would fail with the same error. With an agent name, only the selected agent's `agents[]` entries are checked here; other agents' entries produce the `agents` warning above |

## Exit codes

| Condition | Exit code |
|-----------|-----------|
| No diagnostics, or warnings only without `--strict` | 0 |
| Warning-level diagnostics with `--strict` | 1 |
| Any error-level diagnostic | 1 |
| `fullsend lint <agent-name>` with a name that is not registered or is disabled | 1 |
| `config.yaml` missing or invalid, an agent harness that fails to resolve, or a harness that fails composition, validation, or file checks (for example an invalid `trigger` expression or a missing `policy:`) | 1 |

All agents (or, with a name, the selected agent) are linted before the command exits.
A harness that fails to resolve, load, or validate does not stop the run — the failure
is collected, the remaining agents are still linted, and every error is reported
together at the end.

## Related

- [CEL Triggers Reference](../guides/user/cel-triggers-reference.md) for writing `trigger` expressions
- [Checking a trigger before you merge](../guides/user/cel-triggers-reference.md#checking-a-trigger-before-you-merge) for passing and failing `trigger` examples and a CI job
- [Bring Your Own Agent](../guides/user/bring-your-own-agent.md) for harness and `config.yaml` layout
- [CLI internals](../guides/dev/cli-internals.md) for the full command tree
