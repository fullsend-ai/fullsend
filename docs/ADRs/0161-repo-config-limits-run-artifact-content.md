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
- Whether a run reads private data depends on where the run started, not on
  which agent runs. A repo can take work from an issue tracker that is not
  public, and a run started there reads that tracker's issue. A harness gives
  the sandbox access to such a tracker through an overlay on the run's source
  ([ADR 0088](0088-cel-guarded-overlays.md)). Jira is the first such tracker:
  the shipped code and triage harnesses attach read-only Jira access only when
  `event.source.system == "jira"`. Others may follow.

The output scan masks secret literals. It does not remove personal data,
customer names or private business text
([#9122](https://github.com/fullsend-ai/fullsend/issues/9122)).

This ADR aims to let a repo owner stop publishing conversation content in the
run artifact for runs started from a source that is not public.


## Options

- **Key by provider as well as source**, for example `jira-ro`, the
  read-only Jira provider. For the shipped agents, that provider key selects
  the same runs as the `jira` source, under a second, internal name. A custom agent that reads
  a private system on runs from every source can set `content` for the whole
  repo instead. Rejected.
- **A tracker-specific switch**, such as `jira.artifacts`. Every new tracker
  would need its own key and code. Rejected.

## Decision

We add an `artifacts` setting to `config.yaml` that limits what a run artifact
publishes for each run source, so a public repo can take work from issue
trackers that are not public without publishing what its agents read.

```yaml
artifacts:
  content: all          # every run: all (default) | metadata | none
  by_source:
    jira: metadata      # runs started from a Jira issue
```

- `content` applies to every run.
- `by_source` applies to runs started from one source. Each key names where
  the work came from: `github` (a GitHub issue or PR), `gitlab` (a GitLab
  issue or MR), `jira` (a Jira issue), `manual` (a run you start by hand) or
  `schedule` (a scheduled run). It is the same value CEL triggers see as
  `event.source.system`. An adapter for another tracker adds its own source
  name, and that name works as a key with no change to this setting.

| Level | Artifact holds |
|---|---|
| `all` | Everything in `output/`, as today. This is the default. |
| `metadata` | Only the run-level files: `metrics.json`, `eval-measurements.jsonl`, and `run-telemetry.jsonl` with conversation content removed. Every `iteration-<N>/` directory, `security/findings.jsonl` and any other file is left out. |
| `none` | No artifact is uploaded. |

- **It lives in `config.yaml`, not in agent YAML.** The setting applies the
  same way to every agent, so
  [ADR 0080](0080-config-yaml-vs-agent-env-var-scope.md) puts it in
  `config.yaml`. This replaces ADR 0021's per-agent opt-out.
- **The stricter level wins.** The runner picks one level per run: the
  stricter of `content` and the `by_source` entry for where the run started.
  A source with no entry gets the `content` level. `none` is stricter than
  `metadata`, which is stricter than `all`.
- **Keys are open strings.** Config validation does not limit keys to
  today's sources, so a new source needs no schema change.
- **Layers merge per key.** Across `config.base.yaml` and `config.yaml`, an
  explicit `content` overrides, and `by_source` merges per key. Lowering an
  effective level toward `all` is a relaxation under the
  [ADR 0122](0122-declarative-repo-configuration.md) safety gate, so it needs
  an explicit declaration.
- **Metadata is an allowlist.** `metadata` copies only the files named above.
  A file added to `output/` later stays excluded until this list names it.
  `security/findings.jsonl` stays out: a finding's `detail` can quote tool
  output, and a copy with that field stripped would break the file's hash
  chain. It stays on the runner for local checks.
- **Filtering happens after local use, and fails closed.** The runner leaves
  `output/` untouched, so `fullsend eval-measure` and post-scripts read the
  full run. A step after them builds a staged copy, and the runner uploads
  only that copy. If level resolution fails, the staged copy is empty and the
  runner never uploads the raw `output/`.
- **Content capture is unchanged.** Exporting over OTLP to a backend the repo
  owner controls still works under every level, as in ADR 0050.

The rest of ADR 0021 stands: full transcripts by default, and credential
scanning as an invariant check. Artifact retention is out of scope.

## Forge scope

- **GitHub:** the existing upload step uploads the staged copy and skips the
  upload for `none`.
- **GitLab:** the job artifact path points at the staged copy.
- **Forgejo:** no run workflow yet; it uses the same staged copy when one
  lands.

All forges read the same `config.yaml` setting.

## Consequences

- A repo can withhold conversation content for runs from any source that is
  not public (Jira today, other trackers as adapters land) and keep `all` for
  the rest.
- A custom agent that reads private data on runs from every source is covered
  only by `content`, which applies to the whole repo.
- The retro agent, session resumption and follow-up workflows that download
  the artifact get no transcripts under `metadata` and no artifact under
  `none`, which the user guide must document.
- When the setting is unset, the artifact is the same as today.
- Changing the level is a `config.yaml` change, so it goes through the repo's
  normal review and CODEOWNERS.
