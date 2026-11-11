# Run Artifacts and Private Data

Limit what an agent run publishes in its CI artifact, so a public repository
can take work from a private issue tracker without publishing what the agent
read there.

Every agent run uploads a run artifact: `fullsend-<agent>` on GitHub Actions,
the job artifact on GitLab CI. By default it holds the whole conversation,
which can quote a private Jira issue. On a public repository, any signed-in
user can download it. [Background](#background-what-the-artifact-holds) has
the details.

## Before you start

- A repository with fullsend installed, and write access to its
  `.fullsend/config.yaml`.
- A fullsend release that has the `fullsend stage-artifacts` command. See
  [Upgrading](#upgrading).

## Stop Jira text from reaching the artifact

These steps use Jira. For runs started somewhere else, use that system's
source name (`github` or `gitlab`) in place of `jira`.

1. Open `.fullsend/config.yaml` in your repository and add:

   ```yaml
   artifacts:
     by_source:
       jira: metadata      # runs started from a Jira issue
   ```

   `metadata` keeps run health (cost, turns, traces) and removes the
   conversation. Use `none` to upload nothing. See
   [Choose a level](#choose-a-level).

2. Commit the change to your default branch.

3. Start an agent run from a Jira issue the way you normally do (see
   [Jira integration](jira-integration.md)).

4. Open the run's job log. On GitHub Actions, look in the
   **Stage fullsend artifacts** step; on GitLab CI, look near the end of the
   agent job log. You should see:

   ```text
   Run artifact level metadata: run source "jira" from the normalized event
   ```

   The normalized event is the record fullsend keeps of what started the
   run. The line confirms the run was recognized as a Jira run and got
   `metadata`.

5. Download the run artifact and unpack it. Each run directory holds only
   these files:

   ```text
   fs-cod-abc123/metrics.json
   fs-cod-abc123/run-telemetry.jsonl
   ```

   `eval-measurements.jsonl` is there too when the run was scored. There is
   no `iteration-<N>/` directory and no `security/findings.jsonl`.

The log line and file list above come from running
`fullsend stage-artifacts` locally on a sample run directory with this
config and a Jira event. If you see something else, go to
[Troubleshooting](#troubleshooting).

## Choose a level

A **level** says how much of the run's `output/` directory goes into the
artifact.

| Level | Choose it when | Artifact holds |
|---|---|---|
| `all` (default) | The run reads only public data. | Everything in `output/`. |
| `metadata` | You want run health (cost, turns, traces) without the conversation. | `metrics.json`, `eval-measurements.jsonl` and a stripped `run-telemetry.jsonl` per run directory. |
| `none` | Even metrics, span names and identifiers must stay private. | Nothing; no artifact is uploaded. |

Set the level in one of two places:

- `artifacts.content` applies to every run.
- `artifacts.by_source.<source>` applies to runs started from one **source**:
  the system where the work began. Today that is `github`, `gitlab` or
  `jira`.

A run gets the stricter of the two. `none` is stricter than `metadata`, which
is stricter than `all`. A `by_source` entry can tighten a run but never
loosen `content`.

Write source keys in lowercase: `jira`, not `Jira`.

> [!NOTE]
> **Manual and scheduled runs get your strictest level.** They have no
> source, so fullsend cannot tell which entry applies and uses the strictest
> of `content` and every `by_source` entry. With `by_source: {jira: none}`,
> a scheduled run uploads nothing. No `by_source` key can exempt these runs
> today.

## `by_source` looks only at where the run started

A run started from a GitHub issue, pull request or comment uses the `github`
level, even if its harness gives the agent Jira access on that run. The
shipped code and triage harnesses attach read-only Jira access only to
Jira-started runs, so `by_source.jira` covers them.

A custom agent that reads a private system on runs from every source is not
covered by `by_source`. Set a stricter `content` for the whole repository
instead:

```yaml
artifacts:
  content: metadata
```

### How the run's source is found

fullsend describes what started a run in a normalized event: one JSON record
with the same shape for every tracker and forge. Its `source.system` field
names the source.

1. A run with a valid normalized event uses its `source.system`, lowercased
   first, so an event that says `Jira` still matches your `jira` key.
2. A GitHub Actions run started directly by a GitHub issue, pull request,
   comment or review event is `github`. The standard GitHub stage jobs work
   this way.
3. Everything else is unknown and gets the strictest level you configured:
   - manual and scheduled runs;
   - a dispatch with no valid normalized event;
   - a dispatch whose payload names a source that its event does not back
     up;
   - a GitLab event too large to pass along.

The job log says which rule applied. A GitHub comment run logs:

```text
Run artifact level all: run source "github" (native issue_comment event)
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

## Troubleshooting

Find the message in the job log. On GitHub Actions the warnings also show as
annotations on the run.

| Message | What it means | What to do |
|---|---|---|
| `Run started from jira: the run artifact publishes the full transcript (artifacts level "all"). If jira holds data that is not public, set artifacts.by_source.jira to metadata or none in config.yaml; set it to all to silence this warning.` | A run from an external tracker uploaded everything, and your config makes no choice for that source. | Set `artifacts.by_source.jira` to `metadata` or `none`. To keep full transcripts on purpose, set it to `all`. See [the full transcript warning](#the-full-transcript-warning). |
| `Run artifact level none: run source unknown (no valid normalized event); using the strictest of artifacts.content and every artifacts.by_source entry` | The run has no source (manual, scheduled, or a dispatch without a valid event), so it got your strictest level. The level in the message is the one applied. | Nothing, if that level is what you want. Otherwise loosen the strictest `by_source` entry; no key exempts these runs. |
| `invalid artifacts.by_source key "Jira": source keys are lowercase with no surrounding spaces; use "jira"` | A source key is not lowercase. The run uploads no artifact. | Rename the key as the message says. |
| `invalid artifacts.content "meta": must be one of all, metadata, none` | A level is misspelled. The run uploads no artifact. | Use `all`, `metadata` or `none`. |
| `Run artifact level could not be resolved; no run artifact is uploaded` (on GitLab: `WARNING: run artifact level could not be resolved; no run artifact is uploaded`) | The config or the event could not be read. The line above it in the log gives the cause. | Fix the cause named in the log. |
| `The installed fullsend CLI predates stage-artifacts, so no run artifact is uploaded. Set the version input to at least the fullsend release that ships this action.` | The workflow pins an older fullsend CLI through the action's `version` input. | Raise `version`. See [Upgrading](#upgrading). |
| `The fullsend CLI is not installed (check the install step), so no run artifact is uploaded.` | The install step failed earlier in the job. | Fix the install step. |
| No artifact, no warning | The level is `none`, or on GitLab the job was cancelled or timed out before the end of the job script. | Check the `Run artifact level` line in the log. |

When fullsend cannot work out the level, it uploads nothing rather than the
raw `output/`.

### The full transcript warning

The warning appears when all of these are true:

- the run started from an external tracker: any source other than `github`,
  `gitlab`, `forgejo`, `manual` or `schedule`;
- the run uploads at `all`;
- your config sets neither `artifacts.content` nor a `by_source` entry for
  that source;
- the repository is not known to be private. An `internal` repository or
  project still warns, because every member of the enterprise or signed-in
  user of the instance can read it. When visibility is unknown, it warns.

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
- **GitHub Actions `version` input.** If your workflow pins the fullsend CLI
  through the fullsend action's `version` input, set it to this release or
  later. An older CLI has no `stage-artifacts` command, so the run uploads no
  run artifact and logs the "predates stage-artifacts" warning.
- **GitLab jobs killed mid-run.** The artifact is prepared at the end of the
  job script. A job that is cancelled or times out before then uploads no
  run artifact.

## Background: what the artifact holds

By default the run artifact holds the run's whole `output/` directory:

- the raw conversation stream and transcripts of every iteration;
- the agent's output files;
- `run-telemetry.jsonl`, with conversation content when content capture is on;
- `security/findings.jsonl`.

Tool results in those files can quote what the agent read, such as the text
of a private Jira issue. On a public repository, any signed-in user can
download the artifact until it expires. The output scan masks secret
literals; it does not remove personal data, customer names or other private
business text.

`fullsend run` leaves `output/` untouched. At the end of the job,
`fullsend stage-artifacts` builds a filtered copy at the level you set, and
CI uploads only that copy. The field reference is
[`artifacts`](../../reference/config-reference.md#artifacts).
