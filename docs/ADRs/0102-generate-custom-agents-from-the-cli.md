---
title: "102. Generate custom agents from the CLI"
status: Accepted
relates_to:
  - agent-architecture
topics:
  - cli
  - harness
  - onboarding
---

# 102. Generate custom agents from the CLI

Date: 2026-09-03

## Status

Accepted

## Context

Building a custom agent from scratch is the most common piece of negative user
feedback on fullsend. The
[Bring Your Own Agent guide](../guides/user/bring-your-own-agent.md) asks the
author to hand-write a harness with roughly thirty fields, an agent definition
whose `tools:` must stay consistent with its body, a result schema, a
post-script that safely turns model output into a forge mutation, a CEL
`trigger`, and a `policies/base.yaml` that a per-repo install does not vendor.

Most of those mistakes are invisible until the first dispatch after merge
(#6830). Three of them are invisible even then:

- A harness with no `trigger:` registers, validates, and appears in
  `fullsend agent list`, and is then skipped by dispatch with a bare `continue`
  and no annotation — while resolve and load failures both emit `::error::`.
  None of the seven fleet harnesses has a `trigger:` to copy from, because the
  fleet is dispatched by stage workflows instead.
- A `role:` the hosted mint does not serve is only regex-checked locally and
  surfaces as an opaque `403` at run time (#6563).
- A `policy:` or provider path naming a file that is not there fails at run
  time, or worse degrades to a warning and a sandbox that cannot reach Vertex
  (#6834).

[#4839](https://github.com/fullsend-ai/fullsend/issues/4839) records a July
decision to prefer guides over a CLI for this. That decision was taken in Slack
and was never written up as an ADR, so this is a new decision rather than a
superseding one. Two things have changed since: user feedback that the guides
are not enough, and the precedent of
[GitHub Agentic Workflows](https://github.com/github/gh-aw), which solved the
same onboarding problem with `gh aw new` — a generator that writes a minimal
valid source, leaving the user to edit prose rather than plumbing.

## Options

**Improve the guides further.** Cheapest, and the status quo. But the failure
modes above are not comprehension failures — a reader who understands the guide
perfectly still cannot tell that an omitted `trigger:` means the agent will
never run, because nothing reports it.

**Ship the stub templates from `fullsend-ai/agents` and have the CLI fetch and
pin them like `agent add` does.** Keeps template changes on the agents repo's
release cadence. Rejected for the first cut: it makes the generator's happy
path depend on network and `allowed_remote_resources` state, which reintroduces
the "fails only at first dispatch" class this change exists to remove, and it
creates a merge-order dependency between the two repositories. Revisitable
later as an opt-in `--template-ref`.

**Generate from templates embedded in the CLI.** Chosen.

## Decision

Add `fullsend agent new <name>`, which writes a complete, valid, registered
agent from a minimum of parameters and validates the result before returning.

Four properties make it worth having rather than being a scaffolding
convenience:

1. **A trigger is mandatory.** The command refuses to write a trigger-less
   harness, and `--on` presets emit expressions taken verbatim from the CEL
   reference — a test asserts the generator and the documentation stay the same
   text. The `command:` and `pr-opened` presets both refuse events from forks,
   which matters because the default trigger is attached to every generated
   agent, including agents with role `coder`.
2. **The role table is closed and checked.** `--role` accepts the five fleet
   roles the hosted mint serves. It is hardcoded rather than derived from
   `mintcore.BuiltInRoles()`, because derivation would re-admit `scribe` —
   which `config.ValidRoles()` deliberately excludes as a mint-only dogfood
   role — and would fail open for any future canonical role with no provider
   pairing. A unit test asserts each row still matches
   `mintcore.RolePermissionsFor`.
3. **The generator writes what a per-repo install does not vendor.**
   `policies/base.yaml`, and the providers and profiles the chosen role needs,
   are written when absent and never overwritten. Providers are referenced by
   path rather than bare name, because the embedded provider fallback fills in
   only the OpenAI provider.
4. **The result is validated in process.** Everything is rendered into a
   scratch directory and loaded through the same loader dispatch uses, so a
   harness that would fail validation never leaves a partially written
   `.fullsend` behind.

Templates are embedded with `go:embed`, matching how the repository already
ships scaffold content.

## Consequences

- Creating a working custom agent becomes one command, and the three
  silently-fatal mistakes above become generation-time errors with actionable
  text. The Bring Your Own Agent guide keeps its hand-written path as the
  explanation of what was generated.
- Harness shape is now encoded in a second place. Mitigated but not
  eliminated: the harness is built as a `harness.Harness` value and marshalled
  rather than formatted as text, so the generator cannot emit a field the
  validator does not know about; golden tests pin the generated bytes; and the
  role table has a drift test against the mint. A new *required* harness field
  would still need a matching change here, and the golden tests are what would
  catch it.
- `ValidateRunnerEnvWith` is not run at generation time — it requires every
  `${VAR}` in the harness `env` blocks to be set in the calling process, which
  is true in CI and false on a developer's machine — so an unset variable
  still surfaces at `fullsend run`.
- The default sandbox image digests are compiled in, so they are repinned by
  hand on the same cadence as the fleet repin PRs. A golden test makes a repin
  visible in review.
- `fullsend lock` and `fullsend run` are unchanged and do not share the
  generator's check helper: they interleave minting, runner-env validation and
  `${VAR}` expansion between the same steps in different orders, so sharing one
  helper would change their behaviour.
