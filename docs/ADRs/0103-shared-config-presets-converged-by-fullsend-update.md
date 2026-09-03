---
title: "103. Shared configuration as recorded presets converged by fullsend update"
status: Accepted
relates_to:
  - governance
  - agent-infrastructure
topics:
  - configuration
  - installation
  - distribution
  - presets
---

# 103. Shared configuration as recorded presets converged by fullsend update

Date: 2026-09-03

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

[ADR 0044](0044-deprecate-per-org-installation-mode.md) made per-repo the
only installation mode and named the cost: no central place to share agent
configuration, and every repo bumped one at a time. The pieces meant to fill
that gap exist but do not connect: `config.base.yaml`
([ADR 0069](0069-ready-made-configuration-presets.md)) is written by
`github setup --config` and nothing records where it came from; `agent
update` skips `base:` pins in local harness files and never regenerates
`lock.yaml` (#5433, #5802, #6191); `repos install`
([ADR 0074](0074-repos-command-consolidation.md)) has no preset concept.
Two readers — the dispatch Route job's `yq` gates and `agent
add/update/remove` — read only the overlay, so ADR 0069's layering never
reaches them (#6422's class); those are fixed as prerequisites, not decided
here, and the `agent` one must land before any preset ships agents: today
`agent add` copies preset entries into `config.yaml`, where they shadow
the preset forever. The fullsend-ai org replaced a working Renovate custom manager
(fullsend-ai/.fullsend#174) with a workflow that `sed`-rewrites
`config.yaml` across a hard-coded repo list, pushes to `main` through a
ruleset-bypass App at agents `main` HEAD, and leaves stale hashes on every
non-harness path.

GitHub Agentic Workflows solves this with a central `agentic-workflows`
repo, a `source:` field in each installed workflow, and `gh aw update --org
--create-pull-request`. It needs a three-way merge because one file mixes
managed fields with user edits (it already carves `source` out of the
merge), a compile step because pins live in generated `.lock.yml`, and it
infers the tracking strategy from the ref's shape — which resolves every
`add` to a SHA and so tracks branch HEAD by default.

## Decision

Shared fullsend configuration is a **versioned preset**, recorded in each
consuming repository by provenance, and **bumped by Renovate** — the same
job that bumps the repo's packages and its workflow pins — with one
first-party verb doing the fullsend-specific part.

1. **A preset is a git-hosted `config.base.yaml`** plus the harness bases,
   skills and policies it references by URL, schema-validated on fetch. It
   lives in whichever repo already hosts the org's shared Renovate config;
   no dedicated org repo, no org-level workflow and no enrollment list are
   needed — the `<org>/.fullsend` repo of
   [ADR 0003](0003-org-config-repo-convention.md) is one possible host,
   not a requirement.
2. **Every pin is a Renovate-bumpable string, and the verb never writes
   human-owned files.** `config.yaml` and local harness files are the human
   layer. `config.base.yaml` (byte-identical to the fetched preset, so
   `--config-hash` keeps working), `lock.yaml`, the shim, and a new
   `.fullsend/preset.lock.yaml` whose `source` URL carries the tracked
   ref, with the resolved SHA and `sha256` derived from it, are the bot
   layer, rewritten whole.
   `github setup --config` creates that record from a blob URL on a branch
   or tag, resolved the way `agent add` resolves one. Carving every managed
   field into its own file is what removes the three-way merge; run-time
   resolution removes the compile step.
3. **fullsend ships a Renovate preset** (`custom.regex` managers for the
   preset record, `agents[].source` URLs and harness `base:` URLs — never
   `config.base.yaml` — with `git-refs` / `github-tags` datasources) that a
   repo's `renovate.json` extends. Renovate rewrites the pin strings;
   **one idempotent verb, `fullsend update`**, run as its `postUpgradeTasks`
   command, does the derived work: re-fetch the preset into
   `config.base.yaml`, recompute every `#sha256=`, regenerate `lock.yaml`
   without re-resolving unchanged dependencies. Run by hand it also bumps
   the shim ref; `--check` writes nothing and exits non-zero on drift from
   a tag or SHA. `repos install` applies the preset on a fresh install.
4. **Tracking strategy and cadence are Renovate's**, not new fullsend
   fields: `packageRules` per dependency choose branch, major tag, exact tag
   or SHA, `minimumReleaseAge` gates adoption, `schedule` sets cadence, and
   the org's shared Renovate preset carries those choices to every repo.
   The Route job reads both config layers so a preset can carry policy.

Repos with no explicit `agents:` entry already follow the fullsend build tag
for first-party agents; for them the shim bump is the agent bump. Agents
generated by `fullsend agent new` (#6966) are local files with no
provenance and are left alone except for their `image:` pin.

## Consequences

- Agent and preset bumps arrive as ordinary Renovate pull requests next to
  package and action bumps, replacing `sed` scripts, direct pushes and
  ruleset-bypass Apps; #5433, #5802 and #6191 close on the verb, #6597 and
  #6607 become Renovate policy.
- `postUpgradeTasks` runs only on self-hosted Renovate with an anchored
  `allowedCommands` entry and the shell executor off, so each repo's
  Renovate job (not the Mend-hosted App) runs the verb; a repo without
  Renovate runs `fullsend update` by hand. The fullsend-ai sync workflows
  are retired.
- Orgs get gh-aw's configurable sharing model without a compile phase; the
  org tier ADR 0044 removed returns as a preset URL plus a Renovate preset,
  not a config repo.
- The preset URL and its refs are a supply-chain trust surface: the
  `allowed_remote_resources` union-with-deny-all floor and `--config-hash`
  apply; signing and a `redirect:` for relocated presets are follow-ons.
- Enforcing a policy floor (refusing to run when a repo drifts) is a separate
  decision; this ADR only makes drift visible. The model maps onto Tekton
  remote resolution (git resolver `revision`, bundle digests) if stages
  later run as Tekton tasks.
