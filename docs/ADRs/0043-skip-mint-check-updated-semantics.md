---
title: "43. Updated semantics for --skip-mint-check"
status: Accepted
relates_to:
  - agent-infrastructure
  - security-threat-model
topics:
  - install
  - mint
  - dev-mint
---

# 43. Updated semantics for --skip-mint-check

Date: 2026-06-06

## Status

Accepted

Supersedes the `--skip-mint-check` description in the Decision section of
[ADR 0033](0033-per-repo-installation-mode.md).

## Context

ADR 0033 described `--skip-mint-check` as skipping "mint validation, GCP
provisioning, **and app setup**." This was accurate at the time: when a
user brought their own external mint, there was no reason to create GitHub
Apps (the external mint already held the PEMs).

The dev mint (introduced alongside this ADR) changes the picture. The dev
mint runs locally or over a cloudflared tunnel and stores PEMs on disk in
a data directory (`--mint-data-dir`). Because the dev mint does **not**
manage PEM provisioning itself, the installer must still create GitHub Apps
and write their private keys to disk so the dev mint can serve them.

The existing "skip app setup" behaviour therefore becomes incorrect when
`--mint-data-dir` is used: skipping app setup would leave the dev mint
with no PEMs and every token request would fail.

Additionally, the dev mint is typically accessed over `http://localhost`
(no TLS), which was previously rejected by the mint URL validator. The
validator needs a loopback exception so users can point `--mint-url` at a
local dev mint instance.

## Decision

`--skip-mint-check` now means: **skip GCP mint provisioning** (Cloud
Function deployment, Workload Identity Federation setup, Secret Manager
provisioning). It no longer implies skipping GitHub App creation.

Specifically:

- GitHub Apps **are still created** when `--skip-app-setup` is not set.
- PEM storage is determined by flag combination, evaluated in order:
  1. Disk (`--mint-data-dir`) — takes precedence; when set, PEMs are
     written to `{data-dir}/pems/{role}.pem` regardless of `--mint-project`.
  2. Secret Manager (`--mint-project` set, no `--mint-data-dir`) — PEMs
     go to GCP Secret Manager.
  3. Repo secrets (fallback) — when neither `--mint-project` nor
     `--mint-data-dir` is set.
- HTTP is permitted for loopback addresses (`localhost` and any address
  for which `net.IP.IsLoopback()` returns true, covering `127.0.0.0/8`
  and `::1`) so that `--mint-url http://localhost:8321` works with the
  dev mint.
- Non-loopback HTTP URLs and embedded credentials in the URL are still
  rejected.

The `--mint-data-dir` flag (also introduced with the dev mint) indicates
that PEMs should be written directly to disk rather than stored as GitHub
repo secrets or in Secret Manager.

## Consequences

- Users running a dev mint can use `--skip-mint-check --mint-data-dir
  ~/.fullsend-mint --mint-url http://localhost:8321` and get a fully
  functional installation without any GCP infrastructure.
- Users pointing to a third-party external mint (no GCP, no `--mint-data-dir`)
  continue to work: `--skip-mint-check` still skips all GCP provisioning.
  The installer creates GitHub Apps and stores PEMs as repo secrets on the
  `.fullsend` config repo (the existing fallback path).
- The description of `--skip-mint-check` in the ADR 0033 Decision section
  is frozen (accepted ADRs are immutable); this ADR supersedes it.
