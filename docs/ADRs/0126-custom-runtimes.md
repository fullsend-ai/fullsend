---
title: "126. Execution modes for harness entrypoints: agent-led, handoff, cli"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
topics:
  - runtime
  - harness
---

# 126. Execution modes for harness entrypoints: agent-led, handoff, cli

Date: 2026-10-01

## Status

Accepted

## Context

Today a [fullsend] harness always launches one of a closed set of built-in LLM-agent
runtimes (`claude`, `pi`, `codex`, `dummy`, `dummy-playback`, see
[ADR 0091](0091-per-agent-runtime-model-effort.md), `ValidRuntimes()`) as the
thing executing inside the sandbox. [ADR 0024](0024-harness-definitions.md)
states: "there is no user-supplied 'main script' inside the sandbox."

Users want other things to run as the sandbox entrypoint: a plain bash/python
script, a script that calls a model SDK directly, or a "meta-harness" script
that itself orchestrates multiple agent/model invocations from inside the
sandbox fullsend run already provisioned. The simplest possible version of
that last case, a bash script making several bounded, independent `claude`
calls with different skills, no agent loop owning the sequence:

```bash
#!/usr/bin/env bash
set -euo pipefail

claude --output-format json -p "/strategy-refine $*"
claude --output-format json -p "/strategy-review $*"
claude --output-format json -p "/strategy-push $*"
```

### Motivating examples

Two existing pipelines already do this outside fullsend:

- **`gitlab.com/redhat/rhel-ai/agentic-ci/strat-pipeline`** (`.gitlab-ci.yml`):
  each CI job discovers RFE IDs, locks them (`lock_issues.py`), loops over the
  resulting files calling `ci-scripts/run-claude.sh "/strategy-refine <key>"`
  once per item, pushes results, loops again for `/strategy-review`, unlocks
  on exit. The looping, locking, and sequencing are bash/Python; Claude runs
  only for bounded, single-purpose steps in between.

- **`github.com/opendatahub-io/architecture-context`** (`main.py` +
  `lib/phases/orchestration.py`): a `pipeline` command runs a fixed phase
  sequence (`discover-components`, `generate-architecture`,
  `generate-platform-architecture`, `generate-diagrams`); `_uses_claude()`
  decides per phase whether that phase calls the Claude SDK or Codex. Phase
  sequencing, resumability, and per-component skip logic are plain Python;
  the model is a subroutine of specific phases, not the driver.

strat-pipeline, and the similar RFE-creator and epic-creator pipelines, batch
many Jira tickets per run and commonly run for multiple hours, accumulating
many agent turns, hitting context compaction, and absorbing transient model
provider API failures along the way. [HalluHard](https://arxiv.org/abs/2602.01031)
(Fan et al., 2026) is a general multi-turn factual-grounding benchmark
(legal, research, medical, coding). It did not evaluate this workflow, so it
is not direct evidence about it. What it does establish is a mechanism worth
treating as a design risk here: hallucination rate rising with turn position
due to error propagation, with content grounding staying unreliable even for
the strongest tested configuration (Claude Opus 4.5 with web search, ~30%)
regardless of reasoning effort. A multi-hour, many-turn session is the kind
of regime that mechanism applies to. Deterministic batching/locking/
sequencing keeps each model call short and bounded instead of accumulating
into one long conversation; folding that orchestration into an agent-led
loop trades a bounded, independently-retryable call per work item for one
long session, whether that session's later turns are measurably less
reliable than its earlier ones for *this* workflow is the thing to go test,
not something this ADR can claim is already proven.

Context budget makes a related point from a different angle. In
architecture-context, `overlays/` alone is ~416KB (~43K words) of markdown.
On a 200K-token model, an agent-led session that reads just the overlays
before doing anything else has already spent a large share of its window,
leaving little room for ticket data, repo reads, tool output, and multi-turn
bookkeeping, making compaction close to inevitable. 1M-token models push the
threshold out, but processing multiple tickets per run, each re-reading
architecture-context content, still accumulates quickly. The pressure
doesn't go away, it just takes longer to arrive.

An agent-led or handoff prompt *can* be made to approximate this kind of
loop/lock/sequence logic. A proof of concept did exactly that for
strat-pipeline, with an agent calling strat-creator's skills and scripts
directly. That POC needed host-side evidence checks added to catch process
drift it otherwise would have missed, which is the risk this section is
about, not a reason to dismiss it: a POC needing extra verification
machinery to confirm it did the same thing every time is evidence for the
determinism/verification cost, not against it. The claim this ADR is making
is narrower than "impossible": reproducing deterministic orchestration as
agent behavior means the same inputs are no longer *guaranteed* to produce
the same control flow by construction, so additional verification of
orchestration choices is required that deterministic sequencing avoids.
Both designs still need verification of model outputs and successful
external effects. Even extensive verification raises confidence without
guaranteeing the agent's orchestration choices.

This is a choice about who/what owns the execution loop inside the sandbox,
better named as three **execution modes** than lumped under one "custom
runtime" concept.

## Decision

A harness entrypoint has one of three execution modes. Of these, only **cli**
is new. **agent-led** and **handoff** are already possible in the current
design and are named here only to give the three-way choice a vocabulary:

- **agent-led** (today's only mode, stays the default): a built-in runtime
  (`claude`, `pi`, `codex`, ...) runs as a full agent session, freely using
  tools inside the sandbox. `runtime:` selects which one, as today. No change.

- **handoff**: a built-in runtime still runs as the agent, `runtime:` still
  applies, same model/credential/auth plumbing as agent-led, but the agent's
  job is constrained to one action: invoke a defined script via the Bash tool
  and wait for it to exit. Already achievable today as an agent-definition +
  policy convention (an agent prompted to run one script and stop); no schema
  change needed. Naming it here is documentation, not a decision.

- **cli** (the thing this ADR adds): the entrypoint is a user-supplied script
  or binary, invoked directly. Covers both a plain deterministic script and a
  "meta-harness" script that itself orchestrates further agent/model
  invocations from inside the sandbox. Signaled by the harness setting
  `entrypoint:` instead of `agent:`, no separate `mode:` field; `agent:` and
  `entrypoint:` are mutually exclusive.

`runtime:` ([ADR 0091](0091-per-agent-runtime-model-effort.md)) keeps meaning
"which built-in runtime" in all three modes, but what it triggers differs.
In agent-led and handoff modes it determines both Bootstrap (credential/
config/hook wiring) and Run() (what actually gets launched). In cli mode, a
declared `runtime:` is meant to trigger only that runtime's Bootstrap, its
credentials, config, and sandbox hooks wired up, but not its Run(), so the
entrypoint script can shell out to an already-credentialed CLI. Each
`Bootstrap()` needs new work to support this agentless path (see "Where this
lives" below); this decision is about what `runtime:` should mean once that
exists, not a claim that it works today. This is deliberate:
`entrypoint:` and `runtime:` are *not* mutually exclusive, because a
meta-harness needs exactly this, a way to get one specific runtime
provisioned into the sandbox without that runtime owning the top-level
launch. Multiple simultaneous runtimes are out of scope for now (see
Consequences); a cli-mode harness may declare at most one `runtime:`, the
same cardinality agent-led and handoff already have today.

### Scope: one sandbox, not multi-sandbox orchestration

This is entirely about what runs *inside* the single sandbox a harness
already provisions, [ADR 0024](0024-harness-definitions.md)'s "one harness,
one entrypoint, one sandbox" is the invariant retained here. CLI mode
replaces the top-level agent with a script and can launch child agents
inside that sandbox. A cli-mode "meta-harness" script orchestrating further
agent/model invocations does so as subprocesses or SDK calls inside that
same sandbox; it does not invoke `fullsend run` again,
provision additional sandboxes, or coordinate separate harness invocations.
Sequencing across sandboxes (e.g. triage → code → review) stays a CI-layer
concern, same as today, this ADR does not touch that boundary.

### Where this lives: harness YAML, not config.yaml

`runtime:` ([ADR 0091](0091-per-agent-runtime-model-effort.md)) is a
`config.yaml` concept: it tunes, per-repo or per-agent, which built-in
LLM-agent CLI an already-defined harness launches. It is validated against
the closed `ValidRuntimes()` enum and carries no information about what
to run, only which of a fixed set of CLIs.

`entrypoint:` is not a runtime selection, it names a script or binary to
run, which is part of *what the harness is*, the same category as the
existing `agent:` field (and, like `agent:`, its presence alone signals the
mode, no separate `mode:` field). It belongs in the harness YAML
([ADR 0024](0024-harness-definitions.md)), not `config.yaml`, and
`ValidRuntimes()` gets no new entry for it.

Agent-led and handoff (today, unchanged, both use `agent:`; handoff differs
only in what the agent's `.md` prompts it to do, not in harness shape):

```yaml
# harness/triage.yaml
description: Triage incoming issues.
agent: agents/triage.md
policy: policies/readonly-with-web.yaml
```

cli mode (new, `entrypoint:` replaces `agent:`; no `runtime:` needed if the
script only calls a model SDK directly):

```yaml
# harness/classify.yaml
description: Classify an issue by calling a model SDK directly, no agent loop.
entrypoint: scripts/classify.py
policy: policies/readonly.yaml
timeout_minutes: 5
```

cli mode where the resolved runtime is *intended to be* Bootstrap-only
(codex's credentials and config get wired into the sandbox, but codex's
`Run()` is never invoked; the entrypoint shells out to it instead, see the
caveat below, this needs new code). `runtime:` is not a harness field, it
stays exactly where ADR 0091 put it, in `config.yaml`:

```yaml
# harness/codex-step.yaml
description: Run a bounded codex sub-task from a plain script.
entrypoint: scripts/run-codex-step.sh
policy: policies/readonly.yaml
timeout_minutes: 10
```

```yaml
# config.yaml
agents:
  - name: codex-step
    runtime: codex    # Bootstrap only, this harness has entrypoint:, not agent:
```

`config.yaml`'s explicit `runtime:` selection (repo-wide key or per-agent
`agents:` entry), including a `--runtime` CLI override, follows ADR 0091
precedence; it just means "Bootstrap this
runtime into the sandbox" for a cli-mode harness instead of "launch and run
this runtime." One resolution detail this inherits unchanged:
`ResolveForAgent`/`Resolve("")` currently default an empty runtime to
`"claude"` (`internal/runtime/registry.go`), there is no existing "no
runtime at all" outcome. An SDK-only entrypoint that wants zero Bootstrap
(the `classify.py` example above) needs cli mode to add an explicit
"no built-in runtime" branch distinct from today's implicit claude default,
which agent-led and handoff keep relying on. In CLI mode, an explicit
`--runtime` override takes precedence over per-agent and repo-wide runtime
settings. If none of those sources explicitly selects a runtime, no built-in
runtime is bootstrapped; CLI mode does not apply the implicit Claude default.

#### Two forms of `entrypoint:`

**A: repo-relative path, copied into the sandbox.** Paths resolve the same
way `agent:` already does: `Harness.ResolveRelativeTo`
(`internal/harness/harness.go:644`) resolves relative to the fullsend
directory (`absFullsendDir`, `internal/cli/run.go:731`, not the directory
containing the harness YAML file, so `entrypoint: scripts/classify.py` means
`<fullsend-dir>/scripts/classify.py`), and `ValidateFilesExist`
(`harness.go:837`) checks it exists on the host via `os.Stat`. Resolution and
existence-checking genuinely reuse `agent:`'s path. Delivery does not: the
`agent:` bootstrap uploads to a runtime-specific destination meant to be read
by that runtime (e.g. claude's `<config-dir>/agents/<name>`, per
`claude.go:88`), not a generic "run this" location. `entrypoint:` needs its
own convention, a sandbox path, how the executable bit/shebang or an
interpreter is determined, argv, and working directory, none of which
exists yet. "Reuses `agent:`'s path with zero new machinery" overstated this;
only the resolve/validate half is reuse, the delivery/execution half is new.

**B: bare command already on the image's `$PATH`.** Not possible today.
`ValidateFilesExist` calls `os.Stat(path)` unconditionally on any non-empty,
non-URL value, including absolute paths, there is no carve-out for "this
only exists inside the built sandbox image, not on the host validating the
harness." A harness author who baked a tool into their `image:` and wrote
`entrypoint: my-custom-tool` would fail harness validation on the host before
a sandbox is ever provisioned, because the host has no such file. Supporting
this needs a new rule distinguishing "resolve on the host filesystem" from
"resolve via the sandbox's `$PATH` at runtime", e.g. treating a bare token
with no path separator as the latter and skipping the host-side stat. This
ADR adopts form A only; form B is unsupported until a concrete need for
image-only binaries arises.

Each built-in runtime constructs its own launch invocation on top of its own
`Bootstrap()`, claude builds a `--agent '<name>'` flag (`internal/runtime/claude.go`),
pi translates agent selection its own way (`internal/runtime/pi_agent.go`),
codex has no `--agent` concept at all (`internal/runtime/codex_bootstrap.go`).
`entrypoint:` must not go through any runtime's `Run()`/launch-flag
construction, the runner execs it directly.

`Bootstrap()` is the harder part, and "only the launch step is skipped, not
the setup" is the intent rather than something that works today. Each
production agent runtime `Bootstrap()`, `ClaudeRuntime.Bootstrap`,
`CodexRuntime.Bootstrap`, `PiRuntime.Bootstrap`, currently starts by reading
`input.AgentPath()` and returns an error immediately if it's empty
(`claude.go:49-51` and the equivalent in the other two). An `entrypoint:`
harness has no `agent:`, so calling an unmodified `Bootstrap()` fails before
anything useful happens;
each also interleaves agent-definition setup (frontmatter validation, skill
injection) with the credential/config/hook wiring a cli-mode harness actually
wants. Getting "Bootstrap this runtime's credentials without an agent" needs
an explicit agentless path through each of the three, not a call to the
existing method with a flag skipped, and it needs testing per runtime, since
codex's path (config.toml + auth script + digest) looks nothing like
claude's or pi's.

### Telemetry: decoupling stream format from the entrypoint

Today "which binary runs" and "which stdout format the runner parses for
`RunMetrics`" are the same decision: `runtime: claude` both launches claude
and tells the runner to parse `--output-format stream-json`. cli mode breaks
that coupling, the entrypoint is an arbitrary script, so the runner has no
way to infer what, if anything, its stdout means.

Default: nothing. `DummyRuntime`/`DummyPlaybackRuntime` already run with no
stdout parsing and `RunMetrics` left empty (`internal/runtime/dummy.go`,
`dummy_playback.go`), cli mode follows that precedent by default.

But a cli-mode script might still want telemetry, e.g. a Python script that
prints claude-compatible `stream-json` events after calling a model SDK
directly. Propose a new harness field, independent of `entrypoint:`/
`runtime:`, naming which existing parser (if any) the runner applies to the
entrypoint's stdout:

```yaml
# harness/classify.yaml
entrypoint: scripts/classify.py
stream_format: claude    # parse stdout as claude's stream-json for RunMetrics
```

Valid values mirror the stream formats runtimes already parse (`claude`,
`codex`, `pi`); omitting the field means `none`.

Each format's parser is already a standalone function in `package runtime`,
decoupled from that runtime's own launch path, `parseClaudeStream(r
io.Reader, onEvent func(AgentEvent)) error` (`claude_progress.go`),
`parseCodexStream(...) (threadID string, err error)` (`codex_progress.go`),
`parsePiStream(...) (sessionID string, err error)` (`pi_progress.go`). Today
each is only called from its own runtime's `Run()`, but only because no
generic caller exists yet, not because of any structural coupling. A
`stream_format:` dispatcher for cli mode needs a thin per-format adapter for
the differing return signatures, not a refactor of the parsers themselves.

This is narrower than it might sound, and the illustrative script at the top
of this ADR shows why: it makes three separate `claude --output-format json`
calls (one JSON object per call, printed to stdout), not the single
`stream-json` NDJSON event stream `parseClaudeStream` expects. `stream_format:
claude` parses one clean stream-json stream from one process; it says nothing
about aggregating several separate child-process invocations, or about a
script's own log lines interleaved with child output, into one coherent
`RunMetrics` record. Picking a parser name is not the same as having an
aggregation contract, if per-call costs, turns, or transcripts across
multiple invocations are wanted, that needs its own structured-event format
for the script to emit, which this ADR does not define.

## Consequences

- Harnesses gain a third execution mode via `entrypoint:`, mutually exclusive
  with `agent:`, exactly one of the two is required.
- A cli-mode harness may still resolve a single `runtime:`
  ([ADR 0091](0091-per-agent-runtime-model-effort.md), via `config.yaml` as
  always), meaning only that runtime's `Bootstrap()` should run (credentials,
  config, sandbox hooks), not its `Run()`. This is not true yet: the Claude,
  Codex, and Pi `Bootstrap()` implementations require an agent path today and
  errors without one (`claude.go:49-51` and equivalents). Making this real
  needs an agentless Bootstrap path added to each of Claude, Codex, and Pi,
  tested separately per runtime since codex's credential/config shape has
  nothing in common with claude's or pi's. This decision specifies what
  `runtime:` + `entrypoint:` *should* mean; it is not a claim that it works
  without further implementation.
- Running *multiple* runtimes' `Bootstrap()` in one sandbox (e.g. a
  meta-harness that wants both codex and claude credentialed at once) is out
  of scope here, a cli-mode harness resolves at most one `runtime:`, the
  same cardinality agent-led/handoff already have. Each runtime's config
  directory is already collision-free (`/sandbox/claude-config`,
  `/sandbox/codex-config`, `/sandbox/pi-config`, `internal/sandbox/sandbox.go`),
  so nothing here rules multi-runtime out structurally; it's deferred to a
  future ADR because declaring and validating it (a plural field? interaction
  with `providers:`? per-runtime credential refresh while another runtime is
  also active?) is its own design problem.
- `entrypoint:` reuses `agent:`'s path-resolution and existence-check
  machinery (form A: `ResolveRelativeTo` against the fullsend directory,
  `ValidateFilesExist`), but not its delivery convention, `agent:` uploads
  to a runtime-specific destination meant for that runtime to read, which
  doesn't generalize to "an executable the sandbox runs directly." A sandbox
  path, executable-bit/interpreter handling, argv, and working directory for
  `entrypoint:` still need to be defined; this is new surface, not pure reuse.
  A bare `$PATH` command baked into the image (form B) is separately not
  supported; it would need its own `ValidateFilesExist` carve-out.
- cli-mode runs produce no telemetry by default, `RunMetrics` stays empty,
  matching the existing dummy-runtime precedent. A script can opt in via
  `stream_format:` if it emits one clean, single-process stream in a format
  the runner already knows how to parse (`claude`, `codex`, `pi`), this does
  not cover aggregating multiple child-process invocations or a script's own
  log output into one `RunMetrics` record; that needs its own contract, not
  specified here.
- The runner's full contract for cli-mode scripts (env vars, working
  directory, output/result file conventions, success/failure signaling) is
  left to the implementing PR rather than specified here.
- The existing agent-mode validation loop can re-run the runtime in the same
  sandbox after validation fails. CLI mode runs its entrypoint once by
  default. Explicitly setting `validation_loop.max_iterations` greater than
  one opts a CLI harness into retries after validation failure. Such scripts
  must preserve resumable, idempotent state because external effects can
  precede validation failure. A nonzero entrypoint exit, timeout, or launch
  failure terminates the run and does not trigger an automatic replay.
  Validation failure after a zero exit can trigger another invocation only
  when retry is explicitly enabled.
- CLI entrypoints do not automatically receive the harness's `model:` or
  `effort:` as child-CLI arguments. Scripts must pass those settings explicitly
  or use a documented runtime environment mechanism. Bootstrap-only execution
  must preserve the selected runtime's config-directory environment exports so
  child CLI invocations load the installed configuration, skills, and hooks.
  Those child CLIs still have their own tool-use loops and can execute the
  bootstrap-installed hooks. Direct SDK calls bypass runtime tool hooks.
  OpenShell's sandbox policy continues to apply in either case. Acceptance
  tests must verify hook loading and model propagation separately for each
  supported runtime.
- "handoff" is a prompt/policy convention (an agent prompted to run one
  script and stop), not an enforced constraint, nothing stops the model from
  invoking the Bash tool more than once. If "exactly one script, exactly
  once" needs to be guaranteed rather than requested, that requires runner or
  policy enforcement this ADR does not add.
- Nested-invocation constraints for meta-harness sub-invocations (rate/cost
  caps, recursion depth, credential scoping beyond the sandbox's own policy)
  are out of scope for this ADR; revisit in a future ADR if a concrete
  meta-harness pattern needs them. Worth noting for that future ADR: fullsend
  has no live turn or cost fencing today for *any* execution mode, only
  wall-clock `timeout_minutes` and `validation_loop.max_iterations`.
  `RunMetrics` fields like `NumTurns`/`TotalCostUSD` are recorded into
  `metrics.json` and OTEL spans for downstream judges to inspect after the
  fact, nothing in the runner checks them against a limit mid-run. A future
  fencing mechanism for meta-harness sub-invocations would be new
  infrastructure, not an extension of something that already exists.
