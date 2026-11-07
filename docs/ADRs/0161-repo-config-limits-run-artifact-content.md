---
title: "161. Repo config limits what a run artifact publishes"
status: Accepted
relates_to:
  - security-threat-model
  - operational-observability
topics:
  - security
  - observability
  - configuration
---

# 161. Repo config limits what a run artifact publishes

Date: 2026-11-06

Amends: [ADR 0021](0021-jsonl-reasoning-trace-exposure.md)

## Status

Accepted

## Context

Every agent run uploads its whole `output/` directory as the run artifact:
`fullsend-<agent>` on GitHub Actions, and job artifacts on GitLab CI. Each run
directory holds one `iteration-<N>/` directory per iteration, with the raw
conversation stream (`output.jsonl`), `transcripts/` and the agent's output
files. Next to them sit run-level files: `metrics.json`,
`run-telemetry.jsonl`, `eval-measurements.jsonl` and
`security/findings.jsonl`. The telemetry file carries conversation content
when content capture is on
([ADR 0050](0050-distributed-tracing-instrumentation.md)). Nothing skips the
upload or narrows what it holds.

[ADR 0021](0021-jsonl-reasoning-trace-exposure.md) keeps transcripts by
default and relies on owner-scoped access. It lets an agent's own YAML opt out
when the agent reads protected sources beyond the target repo. That opt-out
was never built. Two facts change where the switch should live:

- On a public repo, any signed-in user can download the artifact until it
  expires, so access is not owner-scoped.
- Whether a run reads private data depends on the repo's sources and
  providers, not on the agent. A GitHub-triggered run with a read-only Jira
  provider attached can read private tracker text. A Jira-triggered run always
  does.

The output scan masks secret literals. It does not remove personal data,
customer names or private business text
([#9122](https://github.com/fullsend-ai/fullsend/issues/9122)).

This ADR aims to let a repo owner stop publishing conversation content in the
run artifact for runs that read private data.

## Decision

We add an `artifacts` setting to `config.yaml` that limits what a run artifact
publishes, so a public repo can use private sources without publishing what
its agents read.

```yaml
artifacts:
  content: all          # all (default) | metadata | none
  by_source:
    jira: metadata      # key: the run's event.source.system
  by_provider:
    jira-ro: metadata   # key: an attached provider's name (example name)
```

| Level | Artifact holds |
|---|---|
| `all` | Everything in `output/`, as today. This is the default. |
| `metadata` | Only the run-level files: `metrics.json`, `eval-measurements.jsonl`, `security/findings.jsonl`, and `run-telemetry.jsonl` with conversation content removed. Every `iteration-<N>/` directory and any other file is left out. |
| `none` | No artifact is uploaded. |

- **It lives in `config.yaml`, not in agent YAML.** The setting applies the
  same way to every agent, so
  [ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md) puts it in
  `config.yaml`. This replaces ADR 0021's per-agent opt-out.
- **The strictest level wins.** The runner picks one level per run: the
  strictest of `content`, the `by_source` entry for the run's
  `event.source.system`, and the `by_provider` entry for each attached
  provider. `by_provider` keys match a provider's configured name. When the
  source is missing or not listed, only `content` and the provider entries
  count. `none` is stricter than `metadata`, which is stricter than `all`.
- **Keys are open strings.** A new source or provider name works as a key
  without a schema change.
- **Layers merge per key.** Across `config.base.yaml` and `config.yaml`, a set
  `content` overrides, and `by_source` and `by_provider` merge per key.
  Lowering an effective level toward `all` is a relaxation under the
  [ADR 0122](0122-declarative-repo-configuration.md) safety gate, so it needs
  an explicit declaration.
- **Metadata is an allowlist.** `metadata` copies only the files named above.
  A file added to `output/` later stays out until this list names it.
- **Filtering happens after local use, and fails closed.** The runner leaves
  `output/` untouched, so `fullsend eval-measure` and post-scripts read the
  full run. A step after them builds a staged copy, and only that copy is
  uploaded. If the level cannot be resolved, the staged copy is empty and the
  raw `output/` is never uploaded.
- **Content capture is unchanged.** Exporting over OTLP to a backend the repo
  owner controls still works under every level, as in ADR 0050.

The rest of ADR 0021 stands: full transcripts by default, and credential
scanning as an invariant check. Artifact retention is out of scope.

## Forge scope

- **GitHub:** the existing upload step uploads the staged copy, and is skipped
  for `none`.
- **GitLab:** the job artifact path points at the staged copy.
- **Forgejo:** no run workflow yet; it uses the same staged copy when one
  lands.

All forges read the same `config.yaml` setting.

## Consequences

- A repo can withhold conversation content for Jira-triggered runs or runs
  with a private-data provider attached, and keep `all` elsewhere.
- The retro agent, session resumption and follow-up workflows that download
  the artifact get no transcripts under `metadata` and no artifact under
  `none`, which the user guide must document.
- When the setting is unset, the artifact matches today's.
- Changing the level is a `config.yaml` change, so it goes through the repo's
  normal review and CODEOWNERS.
