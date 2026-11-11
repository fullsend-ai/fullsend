# Run Artifacts and Private Data

Limit what an agent run publishes in its CI artifact, so a public repository
can take work from a private issue tracker without publishing what the agent
read there.

**First step:** if your repository is public and takes work from Jira (or any
tracker that is not public), add this to `.fullsend/config.yaml`:

```yaml
artifacts:
  by_source:
    jira: metadata      # runs started from a Jira issue
```

The rest of this page explains the levels, what each one keeps and removes,
and what changes for workflows that read the artifact. The field reference is
[`artifacts`](../../reference/config-reference.md#artifacts).

## Prerequisites

- A repository with fullsend installed, and write access to its
  `.fullsend/config.yaml`.
- A fullsend release that ships `fullsend stage-artifacts`. See
  [Upgrading](#upgrading).

## Why the artifact matters

Every agent run uploads a run artifact: `fullsend-<agent>` on GitHub Actions,
the job artifact on GitLab CI. By default it holds the run's whole `output/`
directory:

- the raw conversation stream and transcripts of every iteration;
- the agent's output files;
- `run-telemetry.jsonl`, with conversation content when content capture is on;
- `security/findings.jsonl`.

Tool results in those files can quote what the agent read, such as the text
of a private Jira issue. On a public repository, any signed-in user can
download the artifact until it expires. The output scan masks secret
literals; it does not remove personal data, customer names or other private
business text.

## Choose a level

| Level | Choose it when | Artifact holds |
|---|---|---|
| `all` (default) | The run reads only public data. | Everything in `output/`. |
| `metadata` | You want run health (cost, turns, traces) without the conversation. | `metrics.json`, `eval-measurements.jsonl` and a stripped `run-telemetry.jsonl` per run directory. |
| `none` | Even metrics, span names and identifiers must stay private. | Nothing; no artifact is uploaded. |

Set the level in one of two places:

- `artifacts.content` applies to every run.
- `artifacts.by_source.<source>` applies to runs started from one source.

A run gets the stricter of the two. `none` is stricter than `metadata`, which
is stricter than `all`, so a `by_source` entry can tighten a run but never
loosen `content`.

Source keys must be lowercase (`jira`, not `Jira`). Config validation rejects
any other form, and so does staging at the end of a run: the run then uploads
no artifact.

## `by_source` looks only at where the run started

A run started from a GitHub issue, pull request or comment uses the `github`
level, even if its harness gives the agent Jira access on that run. The
shipped code and triage harnesses attach read-only Jira access only to
Jira-started runs, so `by_source.jira` covers them. Manual and scheduled runs
have no source and get the strictest level you configured (see below).

A custom agent that reads a private system on runs from every source is not
covered by `by_source`. Set a stricter `content` for the whole repository
instead:

```yaml
artifacts:
  content: metadata
```

### How the run's source is found

1. A run with a valid normalized event uses its `source.system`: `github`,
   `gitlab` or `jira` today. Case does not matter (`Jira` matches `jira`).
2. A GitHub Actions run started by a native GitHub issue, pull request,
   comment or review event, with no normalized event in any payload, is
   `github`. The standard GitHub stage jobs work this way.
3. Everything else is unknown: manual and scheduled runs, a dispatch without
   a valid normalized event, a dispatch payload that names a source it cannot
   back (its event is invalid, or it names a different source), and a GitLab
   event too large to pass along. A run with an unknown source gets the
   strictest of `content` and every `by_source` entry. With
   `by_source: {jira: none}`, such a run uploads nothing.

The job log contains the rule that applied. A GitHub comment run logs:

```text
Run artifact level all: run source "github" (native issue_comment event)
```

When an unknown source makes the level stricter than `content` alone, the
line is a warning, and on GitHub Actions also a warning annotation. A
scheduled run with `by_source: {jira: none}` logs:

```text
Run artifact level none: run source unknown (no valid normalized event); using the strictest of artifacts.content and every artifacts.by_source entry
```

## What `metadata` keeps and removes

**Removed:**

- every `iteration-<N>/` directory: conversation stream, transcripts and the
  agent's output files;
- `security/findings.jsonl`;
- every conversation-content attribute and event in `run-telemetry.jsonl`;
- free error text in telemetry: span status messages and the message and
  stack trace of exception events (the status code and exception type stay);
- pre-script text: the skip reason a pre-script prints is removed from the
  telemetry, and every skipped row in `eval-measurements.jsonl` gets a fixed
  explanation instead of its own (whether the run was skipped stays);
- any file not on the metadata list, including files added in later releases.

A telemetry or measurements file that cannot be parsed is left out of the
artifact.

**Still published:**

- `metrics.json`, copied as written: counts, token use and costs, model
  fallback reasons and runner paths;
- eval measurements (`eval-measurements.jsonl`);
- telemetry span names and timings;
- identifiers in span attributes, such as the work item id, the branch name,
  the repository URL and the harness path.

If an identifier such as a branch name is itself private, use `none`.

## What later workflows lose

The retro agent, session resumption and
[follow-up workflows](chaining-follow-up-workflows.md) download the run
artifact. Under `metadata` they get no transcripts, agent results or security
findings. Under `none` they get no artifact at all. A follow-up that reads an
agent's result needs `all` for the runs it consumes.

## Keep conversation content privately

The withheld files exist only on the runner and are gone when the job ends;
fullsend keeps no private copy. To keep conversation content, export traces
over OTLP to a backend you control
([How to emit traces](how-to-emit-traces.md)). OTLP export works at every
level.

## The "full transcript" warning

When a run starts from an external tracker (any source other than `github`,
`gitlab`, `forgejo`, `manual` or `schedule`), uploads at `all`, and the
repository has made no choice for that source, the job log shows:

```text
Run started from jira: the run artifact publishes the full transcript (artifacts level "all"). If jira holds data that is not public, set artifacts.by_source.jira to metadata or none in config.yaml; set it to all to silence this warning.
```

On GitHub Actions it is also a warning annotation on the run.

The warning is skipped when the repository is private: `repository.visibility`
is `private` in the GitHub Actions event (or, without that field,
`repository.private` is true), or `CI_PROJECT_VISIBILITY` is `private` on
GitLab CI. An `internal` repository or project still warns on both forges,
because every member of the enterprise or signed-in user of the instance can
read it. When visibility is unknown, it warns.

To silence it, make an explicit choice in `.fullsend/config.yaml`, either for
the source or for every run:

```yaml
artifacts:
  by_source:
    jira: all           # or metadata / none
```

```yaml
artifacts:
  content: all          # explicit, so no warning for any source
```

## Upgrading

- **GitLab artifact path.** After you re-sync the `.fullsend` scaffold, the
  agent job uploads `fullsend-artifact/` instead of `output/`. Anything that
  downloads the job artifact by path must look under `fullsend-artifact/`.
  Add `fullsend-artifact/` to your `.gitignore` next to `output/`.
- **GitHub Actions `version` input.** With this action, a fullsend CLI pinned
  through the `version` input to an older release has no `stage-artifacts`
  command. The run then uploads no run artifact, and the job log shows a
  warning that the installed CLI predates `stage-artifacts`. Set `version` to
  this release or later. If the CLI failed to install, the warning says it is
  not installed instead.
- **GitLab jobs killed mid-run.** Staging runs at the end of the job script.
  A job that is cancelled or times out before then uploads no run artifact.
