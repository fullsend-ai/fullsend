---
title: "127. Custom entrypoint scripts as a harness's launch command"
status: Accepted
relates_to:
  - agent-architecture
  - agent-infrastructure
  - security-threat-model
topics:
  - runtime
  - harness
---

# 127. Custom entrypoint scripts as a harness's launch command

Date: 2026-10-01

## Status

Accepted

This decision amends [ADR 0024](0024-harness-definitions.md)'s requirement
for one agent and no user-supplied main script per harness invocation. It
retains one sandbox per invocation and permits a bounded departure from
[ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md)'s
per-step isolation for workflows that choose in-sandbox sequencing.

## Context

Today a [fullsend] harness always launches one of a closed set of built-in LLM-agent
runtimes (`claude`, `pi`, `codex`, `dummy`, `dummy-playback`, see
[ADR 0091](0091-per-agent-runtime-model-effort.md), `ValidRuntimes()`) as the
thing executing inside the sandbox. [ADR 0024](0024-harness-definitions.md)
states: "there is no user-supplied 'main script' inside the sandbox."

Users want other things to run as the sandbox entrypoint: a plain bash/python
script, a script that calls a model SDK directly, or a "meta-harness" script
that itself orchestrates multiple agent/model invocations from inside the
sandbox fullsend run already provisioned.

### Motivating examples

The simplest version of that last case is a bash script making several
bounded, independent `claude` calls with different skills, no agent loop
owning the sequence:

```bash
#!/usr/bin/env bash
set -euo pipefail

claude --output-format json -p "/strategy-refine $*"
claude --output-format json -p "/strategy-review $*"
claude --output-format json -p "/strategy-push $*"
```

This illustrates sequencing. Inside fullsend, obtaining the configured hooks
and launch checks requires the supported child-invocation path described below.

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
provider API failures along the way.
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) records
an experiment where an LLM coordinator skipped or reordered steps despite
stronger prompting, and chose deterministic scripts to own sequencing.
That is the direct motivation here. Multi-turn error propagation is also
reported by [HalluHard](https://arxiv.org/abs/2602.01031) (Fan et al., 2026),
but it does not establish the performance of these workflows. Deterministic
batching, locking, and sequencing can keep model calls bounded; their effect
on output quality in these pipelines remains to be measured.

Context budget makes a related point from a different angle. In
architecture-context, `overlays/` alone is ~416KB (~43K words) of markdown.
On a 200K-token model, an agent-led session that reads just the overlays
before doing anything else has already spent a large share of its window,
leaving little room for ticket data, repo reads, tool output, and multi-turn
bookkeeping, making compaction close to inevitable. 1M-token models push the
threshold out, but processing multiple tickets per run, each re-reading
architecture-context content, still accumulates quickly. The pressure
doesn't go away, it just takes longer to arrive.

### Workarounds

An agent-led prompt *can* be made to approximate this kind of
loop/lock/sequence logic, including a "handoff" variant where the prompt
constrains the agent to invoking one defined script via the Bash tool and
stopping — already possible today as an agent-definition and policy
convention, no schema change needed. A proof of concept did exactly that for
strat-pipeline, with an agent calling strat-creator's skills and scripts
directly. That POC needed host-side evidence checks added to catch process
drift it otherwise would have missed. Reproducing deterministic orchestration
as agent behavior requires verification that the model followed the intended
control flow. Scripted sequencing makes those orchestration choices explicit
and testable without invoking a model.
Both designs still need verification of model outputs and successful
external effects. Even extensive verification raises confidence without
guaranteeing the agent's orchestration choices.

This is a choice about who/what owns the execution loop inside the sandbox.
Today that is always a built-in agent runtime; this ADR adds a second way to
launch it, a user-supplied script instead of an agent.

## Options

**Keep deterministic orchestration in native CI.** Existing scripts continue
to run in GitHub Actions, GitLab CI, or another CI system without a new
fullsend launch path. This preserves established workflows, but leaves their
execution outside fullsend's managed sandbox path. It need not involve
multiple `fullsend run` calls. The agent-led handoff workaround can bring a
script into an existing sandbox, with the additional orchestration
verification described above.

**Add a script-executing runtime adapter.** A new runtime could execute an
arbitrary user-supplied script while preserving the existing runtime
abstraction; it need not impose a single fullsend-authored workflow. However,
selecting that adapter through `runtime:` would consume the same selector
used to choose which agent CLI to provision. Supporting both choices would
require another provisioning declaration or equivalent machinery. This is
feasible, but it obscures their independence.

**Add `entrypoint:` alongside optional runtime provisioning (chosen).** The
harness declares its top-level command independently of the built-in runtime
it wants provisioned. This directly represents scripts that call an agent CLI
without handing that CLI control of the whole workflow. It adds a launch
path and requires explicit rules for composition and for runtime setup that
currently occurs during launch.

Multi-sandbox orchestration is a separate scope: it retains per-step sandbox
isolation but requires coordination across harness invocations. This decision
does not replace that approach or design its configuration.

## Decision

Introduce **cli**: a new harness mapping, `entrypoint:`, that overrides the
top-level command the sandbox launches with a user-supplied script or
binary and its arguments. `entrypoint.command` is an argv array, and optional
`entrypoint.stream_format` selects its stdout parser. The command is invoked
directly instead of a built-in agent runtime. This sits
alongside the existing **agent-led** launch (today's only option, stays the
default, unchanged by this ADR): a built-in runtime (`claude`, `pi`,
`codex`, ...) runs as a full agent session, freely using tools inside the
sandbox, selected via `runtime:`.

- **agent-led**: `agent:` names an agent definition; the selected built-in
  runtime runs it as a full agent session. No change.

- **cli** (new): `entrypoint.command` names a script or binary and its
  arguments; the runner execs it directly, no agent loop involved. From the
  runner's point of view there is
  only the script — whether it is a plain deterministic script or itself
  orchestrates further agent/model invocations under the hood is outside the
  runner's (and fullsend's) purview. `agent:` and `entrypoint:` are mutually
  exclusive; there is no separate `mode:` field, whichever one is set is
  the signal.

`runtime:` ([ADR 0091](0091-per-agent-runtime-model-effort.md)) keeps meaning
"which built-in runtime" for both, but what it triggers differs. For
agent-led it determines both Bootstrap (credential/config/hook wiring) and
Run() (what actually gets launched). For cli, a declared `runtime:` is meant
to trigger only that runtime's Bootstrap, its credentials, config, and
sandbox hooks wired up, but not its Run(), so the entrypoint script can
shell out to an already-credentialed CLI through a supported invocation path.
Fullsend must provide that path with the hook activation and applicable
integrity checks currently coupled to `Run()`. The script author must use it
to obtain those guarantees; executing a CLI binary directly is not sufficient.
Each `Bootstrap()` needs new work to support this agentless path (see
"Runtime provisioning and child invocations" below); this
decision is about what `runtime:` should mean once that exists, not a claim
that it works today. This is deliberate: `entrypoint:` and `runtime:` are
*not* mutually exclusive, because a meta-harness needs exactly this, a way
to get one specific runtime provisioned into the sandbox without that
runtime owning the top-level launch. Multiple simultaneous runtimes are out
of scope for now (see Consequences); a cli harness may declare at most one
`runtime:`, the same cardinality agent-led already has today.

### Scope: one sandbox, not multi-sandbox orchestration

This is entirely about what runs *inside* the single sandbox a harness
already provisions. [ADR 0024](0024-harness-definitions.md) states "One
harness, one agent, one sandbox." This decision changes the one-agent
requirement to one top-level launch command, while retaining one sandbox per
harness invocation. CLI mode replaces the top-level agent with a script and
can launch child agents inside that sandbox. A cli-mode "meta-harness" script
orchestrating further agent/model invocations does so as subprocesses or SDK
calls inside that same sandbox; it does not invoke `fullsend run` again,
provision additional sandboxes, or coordinate separate harness invocations.
Sequencing across sandboxes (e.g. triage → code → review) stays a CI-layer
concern, as in [ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md).
Here the script sequences subprocesses within one invocation; it does not
take over dispatch of separate harnesses.

This is a bounded departure from
[ADR 0020](0020-composable-single-responsibility-agents-with-individual-sandboxes.md),
which considered internal child agents sharing a sandbox and chose separate
sandboxes with policies tailored to each step. A meta-harness combining steps
with different permission needs must provide a policy covering their combined
needs; children do not receive per-step sandbox isolation. Harness authors
choose that trade-off deliberately. Workflows requiring per-step isolation
should continue to use separate harness invocations.

[ADR 0016](0016-unidirectional-control-flow.md)'s control-flow rule remains:
the script and its children cannot change higher-layer configuration or
constraints. Sequencing children inside the provisioned sandbox does not
permit them to expand its policy or reconfigure infrastructure.

### Where this lives: harness YAML, not config.yaml

`runtime:` ([ADR 0091](0091-per-agent-runtime-model-effort.md)) is a
`config.yaml` concept: it tunes, per-repo or per-agent, which built-in
LLM-agent CLI an already-defined harness launches. It is validated against
the closed `ValidRuntimes()` enum and carries no information about what
to run, only which of a fixed set of CLIs.

`entrypoint:` describes a script or binary to run, its arguments, and its
optional stdout format. This is part of *what the harness is*, the same
category as the
existing `agent:` field (and, like `agent:`, its presence alone signals
which launch path to use, no separate `mode:` field). It belongs in the
harness YAML ([ADR 0024](0024-harness-definitions.md)), not `config.yaml`,
and `ValidRuntimes()` gets no new entry for it.

Agent-led (today, unchanged, uses `agent:`):

```yaml
# harness/triage.yaml
description: Triage incoming issues.
agent: agents/triage.md
policy: policies/readonly-with-web.yaml
```

cli mode (new, `entrypoint:` replaces `agent:`; no `runtime:` needed if the
script only calls a model SDK directly). This illustrates launch selection;
the harness author must supply SDK authentication and a policy/profile that
permits the SDK process to reach its provider:

```yaml
# harness/classify.yaml
description: Classify an issue by calling a model SDK directly, no agent loop.
entrypoint:
  command: ["scripts/classify.py"]
policy: policies/classify-sdk.yaml  # Author-supplied SDK egress policy.
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
entrypoint:
  command: ["scripts/run-codex-step.sh"]
policy: policies/readonly.yaml
timeout_minutes: 10
```

```yaml
# config.yaml
agents:
  - name: codex-step
    source: harness/codex-step.yaml
    runtime: codex    # Bootstrap only, this harness has entrypoint:, not agent:
```

The local `source:` registers this custom harness. A name-only settings entry
is valid for a built-in agent, or as an overlay on a parent configuration
that already registers the custom harness's source; creating a local harness
file alone does not make a name-only entry valid.

`config.yaml`'s explicit `runtime:` selection (repo-wide key or per-agent
`agents:` entry), including flag and environment overrides, follows ADR 0091
precedence; it just means "Bootstrap this
runtime into the sandbox" for a cli-mode harness instead of "launch and run
this runtime." One resolution detail this inherits unchanged:
`ResolveForAgent`/`Resolve("")` currently default an empty runtime to
`"claude"` (`internal/runtime/registry.go`), there is no existing "no
runtime at all" outcome. An SDK-only entrypoint that wants zero Bootstrap
(the `classify.py` example above) needs cli mode to add an explicit
"no built-in runtime" branch distinct from today's implicit claude default,
which agent-led keeps relying on. In CLI mode the precedence is
`--runtime` > `FULLSEND_RUNTIME` (including existing role-prefixed repository
variable handling) > per-agent `agents:` entry > repo-wide `runtime:`.
If none of those sources explicitly selects a runtime, no built-in runtime
is bootstrapped; CLI mode does not apply the implicit Claude default.

#### Composition and validation

`agent:` and `entrypoint:` are one launch choice during `base:` composition.
A child that supplies either field replaces the inherited launch choice,
clearing the other field. A child that supplies neither inherits the base's
choice. Declaring both in one layer is an error; exactly one must be present
after composition. Both fields stay at the harness top level, as `agent:`
does today; forge blocks and conditional overlays cannot select or switch
the launch path.

`entrypoint:` accepts only the mapping form, with a required, non-empty
`command` array of strings and optional `stream_format`. Scalar shorthand,
empty mappings, and a top-level `stream_format:` are invalid. A child-provided
entrypoint replaces the entire inherited mapping, including its command and
parser selection; arrays are not concatenated and mapping fields are not
merged. Omitting `stream_format` in the replacement means `none`. Selecting
`agent:` removes the inherited entrypoint and its parser together. Existing
agent-led telemetry continues to be selected by the runtime.

Validation-loop settings retain their existing field-level inheritance. An
explicit `max_iterations` greater than one in the resolved configuration,
including a value inherited from a base or supplied by a matching conditional
block, counts as retry opt-in. A child that needs one execution must set
`max_iterations: 1`, especially when switching an agent-led base to a script
with external effects. With no configured limit, CLI mode defaults to one
execution; agent-led defaults are unchanged.

#### Command arguments and `env.sandbox` substitution

`entrypoint.command[0]` names the executable to copy into the sandbox; the
remaining elements are its arguments. The runner launches the copied
executable with those arguments directly, without interpreting the array as
a shell command.

Arguments may reference the harness's `env.sandbox` context using `${NAME}`:

```yaml
# harness/classify.yaml
entrypoint:
  command: ["scripts/classify.py", "--effort", "${USER_SUPPLIED_EFFORT}"]
  stream_format: claude
env:
  sandbox:
    USER_SUPPLIED_EFFORT: high
policy: policies/classify-sdk.yaml
timeout_minutes: 5
```

After composition and normal `env.sandbox` value resolution, the runner
substitutes references in command arguments before launch. Each array element
remains one argument, including values containing spaces or an empty string.
Substitution is a single pass over the declared arguments: inserted values
are not recursively expanded, and no shell evaluation, word splitting, or
globbing occurs. A reference missing from the resolved `env.sandbox` context
is an error, with no fallback to the runner's host environment or other
sandbox environment variables. The executable path in `command[0]` remains
literal; substitution applies only to subsequent arguments.

#### Resolving the executable path

**Local relative path, copied into the sandbox.** The author-written
`entrypoint.command[0]` must be a non-empty literal path relative to the
fullsend directory.
URLs, absolute paths, and environment-variable substitutions are rejected
before resolution. The resolved file, including any symlink target, must
remain within that directory and be a regular file. This decision does not
add entrypoint fetching through the URL content-resolution machinery.

For accepted relative paths, resolution reuses `agent:`'s helper:
`Harness.ResolveRelativeTo`
(`internal/harness/harness.go:644`) resolves relative to the fullsend
directory (`absFullsendDir`, `internal/cli/run.go:731`, not the directory
containing the harness YAML file, so `command: ["scripts/classify.py"]` means
`<fullsend-dir>/scripts/classify.py`), and `ValidateFilesExist`
(`harness.go:837`) checks it exists on the host via `os.Stat`. These helpers
alone do not enforce the new field's restrictions: they pass through URLs
and absolute paths, and `os.Stat` alone does not require a regular file or
contain its symlink target. Entrypoint validation must enforce those rules
before upload. Delivery also needs new machinery: the
`agent:` bootstrap uploads to a runtime-specific destination meant to be read
by that runtime (e.g. claude's `<config-dir>/agents/<name>`, per
`claude.go:88`), not a generic "run this" location. The executable needs its
own convention, a sandbox path, how the executable bit/shebang or an
interpreter is determined, and working directory. The implementing PR
must also define task/event input, supporting-file delivery, and output
conventions; copying a Python entrypoint alone does not deliver its imports.

**Considered and rejected: a bare command already on the image's `$PATH`**
(e.g. a tool baked into `image:` and named as `"my-custom-tool"` in
`entrypoint.command[0]`).
This decision always interprets the first element as a local path, never as a sandbox
`$PATH` lookup. An image-only command with no corresponding local file fails
host-side existence validation before a sandbox is provisioned.
Supporting it would need a new rule distinguishing "resolve on the host
filesystem" from "resolve via the sandbox's `$PATH` at runtime", e.g. treating
a bare token with no path separator as the latter and skipping the host-side
stat. This ADR does not add that rule; image-only binaries stay unsupported
until a concrete need arises.

#### Runtime provisioning and child invocations

Each built-in runtime constructs its own launch invocation on top of its own
`Bootstrap()`, claude builds a `--agent '<name>'` flag (`internal/runtime/claude.go`),
pi translates agent selection its own way (`internal/runtime/pi_agent.go`),
codex has no `--agent` concept at all (`internal/runtime/codex_bootstrap.go`).
The runner executes `entrypoint.command` directly instead of calling a
built-in runtime's `Run()`. A supported child invocation still needs the
runtime-specific launch setup appropriate to that child.

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

Environment exports alone do not complete this setup. Claude loads its hook
wiring via `--settings` and plugins via `--plugin-dir`; Pi loads vetted
extensions with `-e` and performs integrity checks; Codex supplies hook-trust
handling and checks runner-held digests in its launch path. Fullsend's
supported child-invocation path must preserve the selected runtime's config
environment, hook activation, plugin/extension loading, and applicable
pre-launch integrity checks. The script author is responsible for using
that path. Its helper, wrapper, or interface is an implementation choice;
tests must exercise actual child hook execution and tamper rejection for
each supported runtime, rather than merely checking that files were uploaded.

Direct SDK calls do not use runtime tool hooks. Arbitrary direct CLI calls
also have no hook-loading guarantee from bootstrap alone. OpenShell network
and filesystem policy still applies to every process, including any
sandbox-enforced wrapper restrictions; this decision does not bypass those
controls. Hook-based protections such as Tirith, tool-level SSRF checks,
canary detection, and tool-output redaction apply only when the child runtime
loads and executes their adapters.

#### SDK credentials and network access

For SDK-only entrypoints, the harness author owns authentication, credential
refresh, and the policy/profile permitting the SDK process to reach the
provider. Omitting `runtime:` does not automatically provision an inference
provider, export SDK credentials, or expand egress. The `classify.py` example
therefore requires author-supplied setup in addition to its launch declaration.

Current automatic OpenAI provisioning depends on the selected runtime/model
(`NeedsOpenAIProvider`); declaring `providers: [openai]` alone does not enable
that path for a runtime-free script. The shipped inference profiles also do
not authorize Python as a connecting binary. Long-running OpenAI SDK clients
must account for revision-scoped placeholders: refreshing the provider does
not update an already-running client's environment. An author-supplied SDK
integration must use supported credential delivery and refresh mechanisms
and remain within the sandbox policy. General automatic SDK provisioning is
outside this decision; adding it would require provider requirements declared
independently of built-in runtime selection.

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
directly. The optional `stream_format` field inside `entrypoint` names which
existing parser (if any) the runner applies to the command's stdout,
independently of `runtime:`:

```yaml
# harness/classify.yaml
entrypoint:
  command: ["scripts/classify.py"]
  stream_format: claude    # Parse stdout as Claude's stream-json for RunMetrics.
```

Valid `entrypoint.stream_format` values are `none`, `claude`, `codex`, and `pi`;
omitting the field after composition means `none`. Any parser may be selected
independently of the bootstrapped runtime, including when no runtime is
selected. The field only describes stdout; it provisions nothing. It cannot
be declared without an entrypoint command. Agent-led harnesses retain their
existing runtime-selected parser; a top-level `stream_format:` is invalid.

Each format's parser is already a standalone function in `package runtime`,
decoupled from that runtime's own launch path, `parseClaudeStream(r
io.Reader, onEvent func(AgentEvent)) error` (`claude_progress.go`),
`parseCodexStream(...) (threadID string, err error)` (`codex_progress.go`),
`parsePiStream(...) (sessionID string, err error)` (`pi_progress.go`). Today
each is only called from its own runtime's `Run()`, but only because no
generic caller exists yet, not because of any structural coupling. A
dispatcher for `entrypoint.stream_format` needs a thin per-format adapter for
the differing return signatures, not a refactor of the parsers themselves.

This is narrower than it might sound, and the illustrative script in
Motivating examples above shows why: it makes three separate
`claude --output-format json` calls (one JSON object per call, printed to
stdout), not the single
`stream-json` NDJSON event stream `parseClaudeStream` expects. Setting
`entrypoint.stream_format` to `claude` parses one clean stream-json stream
from one process; it says nothing
about aggregating several separate child-process invocations, or about a
script's own log lines interleaved with child output, into one coherent
`RunMetrics` record. Picking a parser name is not the same as having an
aggregation contract, if per-call costs, turns, or transcripts across
multiple invocations are wanted, that needs its own structured-event format
for the script to emit, which this ADR does not define.

## Consequences

- Harnesses gain a new launch path via `entrypoint:`, mutually exclusive
  with `agent:`; exactly one is required after composition, and a child's
  explicit launch choice replaces its inherited choice. The entrypoint
  mapping contains `command` and optional `stream_format`; it is replaced
  as a whole during composition.
- A cli-mode harness may still resolve a single `runtime:`
  ([ADR 0091](0091-per-agent-runtime-model-effort.md), via `config.yaml` as
  always), meaning only that runtime's `Bootstrap()` should run (credentials,
  config, sandbox hooks), not its `Run()`. This is not true yet: the Claude,
  Codex, and Pi `Bootstrap()` implementations require an agent path today and
  error without one (`claude.go:49-51` and equivalents). Making this real
  needs an agentless Bootstrap path added to each of Claude, Codex, and Pi,
  tested separately per runtime since codex's credential/config shape has
  nothing in common with claude's or pi's. This decision specifies what
  `runtime:` + `entrypoint:` *should* mean; it is not a claim that it works
  without further implementation. The supported child-invocation path must
  also preserve launch-time hook activation and applicable integrity checks.
- Running *multiple* runtimes' `Bootstrap()` in one sandbox (e.g. a
  meta-harness that wants both codex and claude credentialed at once) is out
  of scope here, a cli-mode harness resolves at most one `runtime:`, the
  same cardinality agent-led already has. Each runtime's config
  directory is already collision-free (`/sandbox/claude-config`,
  `/sandbox/codex-config`, `/sandbox/pi-config`, `internal/sandbox/sandbox.go`),
  so nothing here rules multi-runtime out structurally; it's deferred to a
  future ADR because declaring and validating it (a plural field? interaction
  with `providers:`? per-runtime credential refresh while another runtime is
  also active?) is its own design problem.
- `entrypoint.command[0]` reuses `agent:`'s path-resolution and existence-check
  machinery (`ResolveRelativeTo` against the fullsend directory,
  `ValidateFilesExist`) for accepted local relative paths, with additional
  validation rejecting URLs, absolute paths, substitutions in the executable
  path, and escapes from that directory. Command arguments support single-pass
  substitution from `env.sandbox`, preserving argument boundaries without
  shell evaluation. Delivery needs a new convention: `agent:` uploads
  to a runtime-specific destination meant for that runtime to read, which
  doesn't generalize to "an executable the sandbox runs directly." A sandbox
  path, executable-bit/interpreter handling, and working directory for the
  command still need to be defined; this is new surface, not pure reuse.
  A bare `$PATH` command baked into the image was considered and rejected
  (it would fail host-side `ValidateFilesExist` before a sandbox is ever
  provisioned); unsupported until a concrete need arises.
- cli-mode runs produce no telemetry by default, `RunMetrics` stays empty,
  matching the existing dummy-runtime precedent. A script can opt in via
  `entrypoint.stream_format` if it emits one clean, single-process stream in
  a format the runner already knows how to parse (`claude`, `codex`, `pi`), this does
  not cover aggregating multiple child-process invocations or a script's own
  log output into one `RunMetrics` record; that needs its own contract, not
  specified here.
- The remaining execution contract (task/event input, supporting-file
  delivery, env vars, working directory, and output/result conventions) is
  left to the implementing PR. It must honor the argv, substitution, exit,
  and retry semantics specified here.
- The existing agent-mode validation loop can re-run the runtime in the same
  sandbox after validation fails. CLI mode runs its entrypoint once by
  default. A configured `validation_loop.max_iterations` greater than one,
  including an inherited value, opts a CLI harness into retries after
  validation failure; set it to one to override inherited retries. Such scripts
  must preserve resumable, idempotent state because external effects can
  precede validation failure. A nonzero entrypoint exit, timeout, or launch
  failure terminates the run and does not trigger an automatic replay.
  Validation failure after a zero exit can trigger another invocation only
  when retry is explicitly enabled.
- CLI entrypoints do not automatically receive the harness's `model:` or
  `effort:` as child-CLI arguments. Scripts must pass those settings explicitly
  or use a documented runtime environment mechanism. Bootstrap-only execution
  must provide the supported child-invocation path described above; preserving
  config-directory exports alone does not activate hooks. Child CLIs retain
  their own tool-use loops. Direct SDK calls bypass runtime tool hooks, and
  direct CLI calls receive no hook-loading guarantee from bootstrap alone.
  OpenShell's sandbox policy continues to apply in either case. Acceptance
  tests must verify hook loading, integrity checks, and explicitly selected
  model propagation separately for each supported runtime.
- SDK authentication, credential refresh, and appropriate egress are the
  harness author's responsibility. Selecting no runtime does not provide
  automatic inference credentials or network access for the script.
- Meta-harness children share the sandbox policy and filesystem. Combining
  steps with different permission needs trades ADR 0020's per-step sandbox
  isolation for sequencing within one invocation; the policy must accommodate
  their combined needs deliberately.
- The "handoff" workaround noted in Context (an agent-led prompt constrained
  to one script) is a prompt/policy convention, not an enforced constraint:
  nothing stops the model from invoking the Bash tool more than once. If
  "exactly one script, exactly once" needs to be guaranteed rather than
  requested, that requires runner or policy enforcement this ADR does not
  add.
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
