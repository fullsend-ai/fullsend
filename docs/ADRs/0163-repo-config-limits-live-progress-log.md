---
title: "163. Repo config limits what the live progress log shows"
status: Accepted
relates_to:
  - security-threat-model
  - operational-observability
topics:
  - security
  - observability
  - configuration
---

# 163. Repo config limits what the live progress log shows

Date: 2026-11-09

## Status

Accepted

## Context

While an agent runs, `fullsend run` prints its progress to the CI job log. The
log shows:

- the model's reasoning and replies, in full;
- one line per tool call: the tool name and a short summary, such as the
  first 120 characters of a command;
- task and status progress, whose labels and details can carry text the agent
  wrote;
- error messages from the agent's transcript, and the output of the
  validation script, which can quote the agent's output files;
- fullsend's own lines: heartbeat, token counts, cost and the result.

On GitHub Actions, tool lines and progress lines also become notice
annotations, and transcript errors become error annotations, on the run
summary page.

Tool results are not printed, but reasoning and replies often quote them. On a
public repo, anyone can read the job log and its annotations until log
retention expires. The output scan masks secret literals only. It does not
remove personal data, customer names or private business text
([#9123](https://github.com/fullsend-ai/fullsend/issues/9123)).

Suppressing an agent's extracted transcripts
([ADR 0021](0021-jsonl-reasoning-trace-exposure.md)) does not cover the job
log. A run started from an issue tracker that is not public still prints the
issue's text, and anything the agent quotes from it, to a public job log.

This ADR aims to let a repo owner stop the live log from showing model- and
tool-written text for runs started from a source that is not public.

## Decision

We add a `progress` setting to `config.yaml` that limits what the live
progress log shows for each run source, so a run's log on a public repo does
not echo text from a private source.

```yaml
progress:
  verbosity: full       # every run: full (default) | tools | quiet
  by_source:
    jira: quiet         # example: runs started from a Jira issue
```

| Level | The log shows |
|---|---|
| `full` | Everything, as today. This is the default. |
| `tools` | Tool lines, progress, token counts and the result. Reasoning and replies are left out. |
| `quiet` | Only lines fullsend writes from its own fields: heartbeat, event kinds and states, iteration and token counts, cost, error types, validation passed or failed, and the result. Leaves out reasoning, replies, tool lines, task labels, status details, error message text, validation output, and the annotations that carry them. |

- **`by_source` keys name where the run started.** Each key is the value
  Common Expression Language (CEL) triggers see as `event.source.system`, such as `github`, `gitlab`, `jira`,
  `manual` or `schedule`. Keys are open strings, so an adapter for another
  tracker adds its source name with no schema change.
- **The stricter level wins.** The runner uses the stricter of `verbosity` and
  the `by_source` entry for the run's source. A source with no entry gets
  `verbosity`. `quiet` is stricter than `tools`, which is stricter than
  `full`.
- **Layers merge per key.** Across `config.base.yaml` and `config.yaml`, an
  explicit `verbosity` overrides, and `by_source` merges per key.
  Lowering an effective level toward `full` is a relaxation under the
  [ADR 0122](0122-declarative-repo-configuration.md) safety gate, so it needs
  an explicit declaration.
- **One setting for every runtime.** Claude Code, Codex, OpenCode and pi runs
  print through the same renderer and runner loop, so the level applies to
  all of them.
- **Only the printed log changes.** Telemetry, content capture
  ([ADR 0050](0050-distributed-tracing-instrumentation.md)), validation
  feedback to the next iteration and the run artifact still receive
  everything.
- **It fails closed.** If the level cannot be resolved, the run logs at
  `quiet`.
- **`quiet` keeps a stuck or failed run visible.** The heartbeat, error types,
  the validation result and the result line still print.
- **Per-agent levels are out of scope.** This ADR decides the repo-level
  setting only.
- **Pre-scripts and post-scripts are out of scope.** They are the repo owner's
  own code.

## Forge scope

- **GitHub:** the level applies to the job log and to its annotations.
- **GitLab:** the level applies to the job log. GitLab has no annotations.
- **Forgejo:** no run workflow yet; the same level applies when one lands.

All forges read the same `config.yaml` setting.

## Consequences

- A repo can keep text from a source that is not public out of its public job
  logs and keep `full` for other sources.
- Under `quiet`, someone debugging a run sees no reasoning, tool lines or
  error text in the log; they need OpenTelemetry (OTLP) export to a backend the repo owner
  controls, or the run artifact.
- When the setting is unset, the log is the same as today.
- Changing the level is a `config.yaml` change, so it goes through the repo's
  normal review and CODEOWNERS.
