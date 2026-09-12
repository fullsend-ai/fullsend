---
title: "103. Shared configuration as recorded presets bumped by Renovate"
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

# 103. Shared configuration as recorded presets bumped by Renovate

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

An organization wants one shared agent configuration for many repos, and
wants each repo to pick up updates automatically, the way Renovate already
bumps its packages and GitHub Actions pins. Per-repo installation
([ADR 0044](0044-deprecate-per-org-installation-mode.md)) removed the org
config repo that used to do this and left nothing in its place.

The building blocks exist but do not connect. A shared
`config.base.yaml` can be installed with `github setup --config`
([ADR 0069](0069-ready-made-configuration-presets.md)), but nothing records
where it came from, so nothing can refresh it. Agent pins carry a commit
SHA and a `#sha256=` hash, but `agent update` does not re-pin `base:` in
local harness files and does not regenerate `lock.yaml` (#5433, #5802).
No command checks pins in CI (#6191). `repos install`
([ADR 0074](0074-repos-command-consolidation.md)) does not know presets.

Today the fullsend-ai org fills the gap with a workflow that `sed`-rewrites
`config.yaml` in a hard-coded list of repos and pushes to `main` through a
ruleset-bypass App. It always tracks `main`, offers no review, and leaves
stale hashes on any path outside `harness/`. It replaced a Renovate custom
manager that did the bump correctly but ran in only one repo
(fullsend-ai/.fullsend#174). Two readers also ignore the base layer and
are fixed as prerequisites, not decided here: the dispatch Route job reads
only `config.yaml` (#6422's class), and `agent add/update/remove` copy
preset entries into `config.yaml`, where they shadow the preset.

## Decision

Shared configuration is a **preset**: a versioned `config.base.yaml` in
any repo, recorded in each consuming repo by provenance, and bumped by that
repo's own Renovate job.

1. **Preset.** The preset is a `config.base.yaml` plus the harness bases,
   skills and policies it references by URL. It lives in whichever repo
   hosts the org's shared Renovate config. There is no dedicated org repo,
   no org-level workflow and no enrollment list.
2. **Two kinds of files.** Humans own `config.yaml` and local harness
   files. Tooling owns `config.base.yaml` (a byte-identical copy of the
   preset), `lock.yaml`, the shim, and a new `.fullsend/preset.lock.yaml`
   whose `source` URL names the preset and its tracked ref; the resolved
   SHA and `sha256` are derived from it. Tooling never writes the human
   files. That split is why no three-way merge and no compile step are
   needed.
3. **Renovate bumps the pins.** fullsend ships a Renovate preset with
   `custom.regex` managers for the preset record, `agents[].source` URLs
   and harness `base:` URLs (never `config.base.yaml`). A repo's
   `renovate.json` extends it. Renovate's built-in `github-actions` manager
   already bumps the shim.
4. **`fullsend update` does the rest.** Renovate runs it as the
   `postUpgradeTasks` command. It re-fetches the preset into
   `config.base.yaml`, recomputes every `#sha256=`, and regenerates
   `lock.yaml` without re-resolving unchanged dependencies. Run by hand it
   also bumps the shim ref. `--check` writes nothing and exits non-zero on
   drift from a tag or SHA. `repos install` applies the preset on a fresh
   install.
5. **Policy is Renovate's.** Which ref to track, how long to wait
   (`minimumReleaseAge`) and how often to run (`schedule`) are ordinary
   Renovate `packageRules`, shared through the org's Renovate preset.

Repos with no explicit `agents:` entry follow the fullsend build tag for
first-party agents, so the shim bump is their agent bump. Agents generated
by `fullsend agent new` (#6966) are local files and are left alone.

## Consequences

- Agent and preset bumps arrive as ordinary Renovate pull requests, and the
  fullsend-ai sync workflows are retired.
- #5433, #5802 and #6191 close on the verb; #6597 and #6607 become
  Renovate policy.
- `postUpgradeTasks` needs self-hosted Renovate with an anchored
  `allowedCommands` entry, so a repo without Renovate runs `fullsend
  update` by hand.
- The preset URL is a supply-chain trust surface covered by
  `allowed_remote_resources` and `--config-hash`; signing is a follow-on.
- Refusing to run when a repo drifts from its preset is a separate
  decision; this ADR only makes drift visible.
