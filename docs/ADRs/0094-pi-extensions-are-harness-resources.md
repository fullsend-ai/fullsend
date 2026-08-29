---
title: "94. Pi extensions are harness resources"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - runtime
  - harness
  - security
---

# 94. Pi extensions are harness resources

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

pi grows its tool surface through extensions: JavaScript/TypeScript modules
loaded with `-e` that register tools, providers and event handlers. The pi
runtime already loads the vendored Vertex providers and the sandbox hook
adapter ([ADR 0090](0090-runtime-neutral-sandbox-hooks-contract.md)) that
way, under `--no-extensions` and `defaultProjectTrust: never`, so nothing
from the target repository is picked up — but a harness had no way to add
one of its own. `plugins:` is Claude Code's marketplace layout, which pi
skips; pi's `settings.json` `packages`/`extensions` sources install from the
network at startup. The fleet wants extension-provided tools
([#6520](https://github.com/fullsend-ai/fullsend/issues/6520),
[#6550](https://github.com/fullsend-ai/fullsend/issues/6550),
[#6527](https://github.com/fullsend-ai/fullsend/issues/6527)), and the user
requirement was explicit: a path list should be a complete configuration.

## Decision

Add `extensions:` to the harness schema: a list of directories in the
harness repository, in string form or `{path, args, env}` when the
extension needs CLI flags or environment. Three rules govern it.

1. **Harness-repo content only.** An extension has the same trust as
   `skills:`, `plugins:` and `scripts:`: org-allowlisted URL base,
   content-addressed fetch, injection scan. URLs, `npm:`/`git:`/`ssh:`
   sources and `..` segments are rejected at validation. Nothing changes
   on the target-repo side: `defaultProjectTrust: never`, `--no-approve`
   and `--no-extensions` stay as they are; the runner appends the vetted
   `-e` paths.
2. **Local and vendored.** The directory must be loadable by pi's own
   entry-point rule (validated at harness load), and its dependencies are
   committed — the sandbox never runs `npm install`. pi's rule is not the
   obvious one: a `package.json` carrying a `pi` object decides the verdict
   by itself, so a directory that names no resolvable `pi.extensions` entry
   loads nothing at all rather than falling back to `index.js`, and does so
   silently. Validation mirrors that, and refuses an entry that resolves
   outside the directory.
3. **No per-tool declaration, and no per-tool exemption either.**
   `--no-extensions` plus explicit `-e` closes the set of code that can
   register tools, so an extension needs no manifest of the tools it adds.
   It gets no privilege from that closure: every sandbox hook, the optional
   tool allowlist included, decides on an extension tool exactly as on any
   other, and an org that runs the allowlist lists extension tool names in
   `FULLSEND_TOOL_ALLOWLIST` like any other name. The adapter cannot grant
   an exemption anyway — the manifest it would key on lives in the
   agent-writable config directory.

Run-time mechanics follow from those rules: upload to a runner-owned
directory, a tree-hash preflight before each iteration that fails closed,
`args` restricted to flags the extension itself registers, and an `env`
deny-list — not the export order — keeping the runtime's and the providers'
variables out of an extension's reach, since pi passes its environment to
every hook script it spawns. They are documented in
[pi runtime: extensions](../runtimes/pi.md#extensions). Claude Code and the
dummy runtime name and skip the list rather than dropping it silently.

## Options

- **Reuse `plugins:` for both runtimes.** Rejected: the formats differ
  (marketplace `plugin.json` vs. pi entry points), and one list meaning
  different things per runtime is a silent surprise.
- **pi `settings.json` `packages`/`extensions` sources.** Rejected: pi
  installs them from the network at startup, and the set of code that may
  register tools would no longer be closed by `--no-extensions` + `-e`.
- **A mandatory per-extension tool manifest (declared tool names, Claude
  mappings).** Rejected for UX: the closure above makes it redundant, and
  it is exactly the bookkeeping the requirement excludes.
- **Exempting extension tools from the tool-allowlist hook.** Rejected:
  the decision would rest on agent-writable manifest fields, and naming the
  tools in `FULLSEND_TOOL_ALLOWLIST` costs the org one line.

## Consequences

- A harness adds an extension with one list entry, for local and
  URL-sourced harnesses alike, and it fails loudly: at validation when pi
  would refuse the directory, at exit 96 when the sandbox copy moved.
- Extensions must not write into their own directory between iterations and
  must contain no symlinks; the preflight treats either as tampering.
- A hash over the extension *source* only binds what the loader reads, so
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
- Extension `env` cannot set the interpreter environment, any credential- or
  proxy-shaped name, or the runner's and providers' families.
- Follow-ups out of scope here: an image-baked (`image:`) prefix form;
  `replaces_builtin` guards; per-tool Claude-name mapping; the Track E
  sub-agent tool (#6527); `--tools` union with extension tools. Per-agent
  runtime selection remains
  [ADR 0091](0091-per-agent-runtime-model-effort.md).
