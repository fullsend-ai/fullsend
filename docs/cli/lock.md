---
sidebar_label: fullsend lock
---

# fullsend lock

Pin remote dependencies to `lock.yaml`.

## Usage

```bash
fullsend lock [agent-name]
```

## Existing lock file

If `lock.yaml` exists but cannot be parsed or declares an unsupported version,
the command exits without writing and leaves the file unchanged.

## Stale entries and harness diagnostics

An entry is stale when the harness file or the `harness:` block of the agent's
`agents:` entry has changed.

When it composes a harness (a new or stale entry, or `--update`), it also
prints the harness diagnostics, including
[reserved `env.sandbox` keys](run.md#reserved-envsandbox-keys). A current entry
is not composed, so its keys are not checked, and `lock` composes without event
context, so keys from event-guarded overlays are reported only by
`fullsend run`.

## Remote resource allowlist

A harness whose runtime fetch service can start (`allow_runtime_fetch: true`,
URL-sourced skill or plugin directories, or a bare, deprecated
`allowed_remote_resources` declaration) must have every harness-level
`allowed_remote_resources` prefix covered by the config-level allowlist —
`config.yaml`'s `allowed_remote_resources` plus the built-in default prefixes
(the defaults are included when the list is omitted or non-empty; an explicit
`allowed_remote_resources: []` stays deny-all and excludes them).

`lock` fails naming an uncovered prefix, and fails closed when no repository
config (`config.yaml` or `config.base.yaml`) exists. With a `harness:` block on
the agent's entry, the prefixes checked are the ones the block sets.
