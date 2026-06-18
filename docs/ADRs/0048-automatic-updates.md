---
title: "48. Automatic Updates"
status: Accepted
relates_to: []
topics:
  - versioning
  - updates
  - automatic updates
---

# 48. Automatic Updates

Date: 2026-06-09

## Status

Accepted

<!-- ADRs are point-in-time records, but not fully frozen after acceptance.
     Minor annotations are welcome: cross-references to related ADRs, short
     notes linking to newer decisions, or clarifying remarks. However, do not
     substantially rewrite the Context, Decision, or Consequences sections. If
     the decision itself needs to change, write a new ADR that supersedes this
     one. For evolving design narrative, use docs/architecture.md. -->

## Context

Currently Fullsend uses a moving tag (`v0`) so users pick up the latest changes. When a release happens
a new tag `vMAJOR.MINOR.PATCH` gets created and the moving tag gets moved to the same SHA. New Fullsend
runs pick up these changes as they use the moving tag. Fullsend also uses `latest` as a binary
version by default, so users automatically pick up new changes for the binary as well.

On the one hand we have concerns about breaking people when releasing new stuff, as things break in
unexpected ways, and tests do not catch those. On the other hand there are people willing to accept
updates and deal with the consequences later.

There are also infrastructure problems. What happens when the update include a new variable
that needs to be present in the platform of choice? There are external changes like those
that make automatic update a challenge.

## Decision

Our decision is to provide two tags:

* Moving tag that tracks the latest release (probably called `latest`).
* Version tags that track releases (`vMAJOR.MINOR.PATCH` which area already created).

By default Fullsend should be installed in a way that it tracks the binary version (`fullsend --version`).
Users should explicitly change something to track a new version tag or the moving tag.

Fullsend must make users aware of the implications of choosing a moving tag:

* Broken releases.
* Infrastructure changes required.

## Consequences

* `v0` should be migrated to the new moving tag and deleted.
* Current users track the new floating tag automatically to keep behavior consistent.
* New users track the version tag they install at.

See [Automatic Updates](../plans/automatic-updates.md) for the design details.
