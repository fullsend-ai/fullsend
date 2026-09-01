---
title: "94. Plugins are runtime-scoped harness resources"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - harness
  - security
---

# 94. Plugins are runtime-scoped harness resources

Date: 2026-08-29

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

pi grows its tool surface through extensions — JavaScript/TypeScript
modules loaded with `-e` that register tools, providers and event handlers. The pi
runtime already loads the vendored Vertex providers and the sandbox hook
adapter ([ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md)) that
way, under `--no-extensions` and `defaultProjectTrust: never`, so nothing
from the target repository is picked up — but a harness had no way to add
one of its own. pi's `settings.json` `packages`/`extensions` sources install
from the network at startup, which the sandbox cannot do. The fleet wants
extension-provided tools
([#6520](https://github.com/fullsend-ai/fullsend/issues/6520),
[#6550](https://github.com/fullsend-ai/fullsend/issues/6550),
[#6527](https://github.com/fullsend-ai/fullsend/issues/6527)), and the user
requirement was explicit: a path list should be a complete configuration.

The harness does not choose the runtime. `runtime.ResolveForAgent` reads it
from org and per-repo config, with per-agent overrides
([ADR 0091](0091-per-agent-runtime-model-effort.md)), so the same harness
runs under whichever runtime the org picked. A per-runtime resource key
would therefore make the harness carry configuration for a decision it does
not own, and would multiply with every runtime added after pi — Codex and
OpenCode are both in the roadmap.

Across those runtimes, plugin directories fall into two families rather than
one per runtime:

- **Manifest bundles** a runtime reads at startup: Claude Code's
  `plugin.json` layout, which Codex also reads (`.codex-plugin/plugin.json`
  and `.claude-plugin/plugin.json`).
- **Code modules** a runtime loads and executes: pi's `-e <dir>` extensions,
  and OpenCode's plugin modules.

The families are distinguishable from the directory itself, which is what
makes one key possible.

## Decision

One harness key, `plugins:`, lists directories a runtime loads. Each entry
is a path string, or `{path, env, pi}` when it needs environment or
runtime-specific options. Five rules govern it.

1. **The directory decides which runtime loads it.** `internal/pluginformat`
   is a leaf package (no dependency on `internal/harness` or
   `internal/runtime`) that classifies an entry: `plugin.json` at the root
   is a Claude plugin, otherwise pi's own `-e <dir>` loader rule decides
   whether it is a pi extension, and a directory that is neither is a
   validation error. The marker order is precedence, not exclusivity: a
   Claude plugin that bundles a Node MCP server ships a `package.json`
   whose `main` resolves, which would satisfy pi's rule as well, and such a
   directory is a Claude plugin. Each runtime loads the entries of its own
   kind and *names and skips* the rest, so switching runtime never silently
   drops a plugin.
2. **Harness-repo content only.** A plugin has the same trust as `skills:`
   and `scripts:`: a path in the harness repository or a forge tree URL
   pinned with `#sha256=`, org-allowlisted, content-addressed, injection
   scanned. `npm:`/`git:`/`ssh:` sources and `..` segments are rejected at
   validation — pi would fetch the first two from the network at startup.
   Nothing changes on the target-repo side: `defaultProjectTrust: never`,
   `--no-approve` and `--no-extensions` stay as they are; the runner appends
   the vetted `-e` paths.
3. **Local and vendored.** A pi-format directory must be loadable by pi's
   own entry-point rule (validated at harness load), and its dependencies
   are committed — the sandbox never runs `npm install`. pi's rule is not
   the obvious one: a `package.json` carrying a `pi` object decides the
   verdict by itself, so a directory that names no resolvable
   `pi.extensions` entry loads nothing at all rather than falling back to
   `index.js`, and does so silently. Validation mirrors that, and refuses an
   entry that resolves outside the directory.
4. **Runtime-specific options are namespaced.** `env` is the code family's
   knob (it is exported before the runtime starts) and `pi: {args}` holds
   the flags pi passes after `-e <path>`. On a Claude plugin both are a
   validation error rather than a silent drop, which keeps `ClaudeRuntime`
   behaviour unchanged. A future runtime adds its own block instead of its
   own key.
5. **No per-tool declaration, and no per-tool exemption either.**
   `--no-extensions` plus explicit `-e` closes the set of code that can
   register tools, so an extension needs no manifest of the tools it adds.
   It gets no privilege from that closure: every sandbox hook, the optional
   tool allowlist included, decides on an extension tool exactly as on any
   other, and an org that runs the allowlist lists extension tool names in
   `FULLSEND_TOOL_ALLOWLIST` like any other name. The adapter cannot grant
   an exemption anyway — the manifest it would key on lives in the
   agent-writable config directory.

One entry serves one runtime. A polyglot directory is not a goal and mostly
not possible: pi's package rule means a `skills/` subfolder — which a Claude
plugin may well have — disables `index.*` outright.

Directories only, in this decision. Single-file pi entries (`-e <file>`,
which pi accepts) are a follow-up: they need their own hash and upload shape
and would have no Claude counterpart.

Run-time mechanics follow from those rules: upload to a runner-owned
directory, a tree-hash preflight before each iteration that fails closed,
`pi.args` restricted to flags the extension itself registers, and an `env`
deny-list — not the export order — keeping the runtime's and the providers'
variables out of a plugin's reach, since pi passes its environment to every
hook script it spawns. The harness author's walkthrough is
[pi runtime: plugins](../runtimes/pi.md#plugins-pi-extensions); the mechanics
and their reasoning are in [Runtime Implementation: Pi
extensions](../contributing/runtime-implementation.md#pi-extensions-adr-0094).

## Options

- **A separate `extensions` key for pi.** Rejected: the runtime is chosen
  by org and per-repo config, not by the harness, so a per-runtime key makes
  the harness carry a decision it does not own — and it multiplies with
  Codex and OpenCode. Detecting the format per directory costs one leaf
  package and keeps one list working across a runtime switch.
- **pi `settings.json` `packages`/`extensions` sources.** Rejected: pi
  installs them from the network at startup, and the set of code that may
  register tools would no longer be closed by `--no-extensions` + `-e`.
- **A mandatory per-plugin tool manifest (declared tool names, Claude
  mappings).** Rejected for UX: the closure above makes it redundant, and
  it is exactly the bookkeeping the requirement excludes.
- **Exempting extension tools from the tool-allowlist hook.** Rejected:
  the decision would rest on agent-writable manifest fields, and naming the
  tools in `FULLSEND_TOOL_ALLOWLIST` costs the org one line.

## Consequences

- A harness adds a plugin with one list entry, for local and URL-sourced
  harnesses alike, and it fails loudly: at validation when no runtime would
  load the directory, at exit 96 when the sandbox copy moved.
- The `plugins:` list is now format-checked. A directory that is neither a
  Claude plugin nor a pi extension, and two entries that would upload under
  the same sandbox name, are refused at load — both used to pass and either
  fail or drop an entry at run time.
- Plugins must not write into their own directory between iterations and
  must contain no symlinks; the preflight treats either as tampering.
- A hash over the plugin *source* only binds what the loader reads, so
  the loader environment is pinned as well. The on-disk transpile cache is
  disabled (`JITI_FS_CACHE=false` in `PiRuntime.EnvExports`): it lives in an
  agent-writable directory and validates an entry against a marker derived
  from the source alone, so a rewritten cache body would execute while the
  source, this preflight and the hook adapter's checksum all stayed clean.
  The rest of the family is cleared outright right after the agent-writable
  `.env` is sourced, on every provider path — above all the loader's module
  *alias* map, which points a loaded specifier at a different file and is
  read from the environment because pi's bundled entry point does not pin
  that option. A time-of-check/time-of-use window remains, shared with the
  hook-adapter guard: a process left running by an earlier iteration can
  rewrite the tree between the check and pi's import.
- Plugin `env` cannot set the interpreter environment, any credential- or
  proxy-shaped name, or the runner's and providers' families.
- Base-composed plugin directories key their lock entry on the directory URL
  rather than on `<dir>/plugin.json`, since a plugin entry no longer has one
  marker file; existing lock files re-resolve once.
- Follow-ups out of scope here: single-file pi entries; the Codex and
  OpenCode loaders; `.claude-plugin/plugin.json` as a second Claude marker;
  Claude-side honouring of `env`; an image-baked (`image:`) prefix form;
  `replaces_builtin` guards; per-tool Claude-name mapping; the Track E
  sub-agent tool (#6527); `--tools` union with extension tools. Per-agent
  runtime selection remains
  [ADR 0091](0091-per-agent-runtime-model-effort.md).
