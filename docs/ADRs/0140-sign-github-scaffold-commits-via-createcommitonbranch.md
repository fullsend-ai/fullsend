---
title: "140. Sign GitHub scaffold commits server-side with createCommitOnBranch"
status: Accepted
relates_to:
  - security-threat-model
topics:
  - install
  - forge-abstraction
  - commit-signing
---

# 140. Sign GitHub scaffold commits server-side with createCommitOnBranch

Date: 2026-10-12

## Status

Accepted

## Context

`fullsend repos install` delivers scaffold files on GitHub through the Git
Data REST API (`internal/forge/github`). GitHub signs commits created that
way only when the request is authenticated as a GitHub App installation.
With the credentials a human operator uses — a `gh auth` token or a
personal access token — the commit is unsigned, and a ruleset with
`required_signatures` rejects the ref update ("Commits must have verified
signatures"). Organization-level rulesets commonly cover every branch, so
PR-based delivery fails as well as `--direct`. Issue #8367 records the
observation and the live probes behind this decision.

Earlier signing work addressed agent-produced commits, not installs made
with user tokens: gitsign ([#1685](https://github.com/fullsend-ai/fullsend/issues/1685),
removal pending in [#5456](https://github.com/fullsend-ai/fullsend/issues/5456)),
an SSH key supplied as a secret ([#5349](https://github.com/fullsend-ai/fullsend/pull/5349)),
and the Commits API with the App token ([agents#357](https://github.com/fullsend-ai/agents/issues/357)).
The threat model treats commit signing as provenance for output
([security-threat-model.md](../problems/security-threat-model.md#relationship-to-commit-signing)).
All forge writes go through `forge.Client` ([ADR 0005](0005-forge-abstraction-layer.md)),
and a vendored install ([ADR 0047](0047-vendored-installs-with-vendor-flag.md))
commits a streamed, executable binary through the same client.

## Options

- **Client-side PGP signature via the REST `signature` parameter.** The
  operator's own key signs, but only PGP is accepted (SSH signing keys are
  not), the key must be registered and reachable by the CLI, the canonical
  commit text must be reproduced byte for byte, and setting a custom
  committer disables GitHub's own signing. GitLab has no equivalent.
- **Local `git` clone, `git commit -S`, push.** Uses whatever signing the
  operator configured, but every forge write would leave `forge.Client`,
  and the fork and cross-repository PR flows would be rewritten around a
  local checkout.
- **Commit with a GitHub App installation token** (the mechanism of
  agents#357). Signed by GitHub, but the App must already be installed on
  the repository, the author becomes `<app>[bot]` rather than the
  operator the DCO trailer names, and a human-run command gains a mint
  dependency.
- **Ruleset bypass actor.** The right lever for agent identities; it does
  not apply to a human operator's install.

## Decision

The GitHub forge client creates a commit with the GraphQL
`createCommitOnBranch` mutation whenever every changed file is a regular
inline file (mode `100644`, no `LocalPath`), and for every deletion-only
commit. GitHub signs those commits for any credential type; the author is
the authenticated account and the committer is GitHub, as for the web
interface.

Commits that change a `LocalPath` or non-regular-mode file — the vendored
bundle — keep the Git Data REST path, which streams blobs and sets modes
but is unsigned with user tokens; vendored installs are documented as
incompatible with signature-requiring rules.

## Consequences

- Human-run installs, upgrades and uninstalls succeed on repositories whose
  rules require verified signatures, with no key material on the operator's
  machine; the standard install writes only regular files and always takes
  the signed path.
- The mutation is folded into the existing `forge.Client` contract: the
  branch head read before the diff is its `expectedHeadOid`, so `STALE_DATA`
  maps to `forge.ErrNonFastForward` and is retried, and rule rejections map
  to `forge.ErrBranchProtected`, with the new `forge.ErrSignedCommitsRequired`
  naming a signature rule.
- The mutation is not idempotent, and the transport resends it after a 5xx
  or timeout; a resend of a commit GitHub had already applied fails with
  `STALE_DATA`, so on that error the client checks whether the branch
  already holds the requested content before treating it as a race.
- The idempotency pre-check and tree-filtered deletions stay in front of the
  mutation (identical content still creates a commit; a missing deletion
  aborts it), and a mode-only difference on an existing file counts as
  unchanged because the mutation cannot change modes.
- The executable bit is no longer a managed property of GitHub installs:
  new files are `100644` and existing modes are left alone (the installer
  action already `chmod +x`es the vendored binary).
- Vendored installs stay unsigned with user tokens and cannot target
  signature-requiring repositories until vendoring stops committing a
  binary.
- The signature is GitHub's web-flow key attesting that GitHub created the
  commit for the authenticated account, not the operator's own key:
  policies that verify a specific key or a Sigstore identity are not met,
  and a ruleset constraining the committer email rejects these commits as
  it rejects web-interface commits.
- "Signed if supported" can be silently false on GitHub Enterprise Server
  without web commit signing enabled; and because behaviour tests run as
  App installations, for which the REST path was already signed, a unit
  test pins the mutation for the regular-file case.
