---
title: "166. Select Pi extension tools without widening access"
status: Accepted
relates_to:
  - security-threat-model
  - agent-architecture
topics:
  - runtime
  - tools
  - security
---

# 166. Select Pi extension tools without widening access

Date: 2026-11-10

## Status

Accepted

## Context

[ADR 0094](0094-pi-extensions-are-harness-resources.md) admits extension code
through trusted harness resources. The Pi runner translates built-in agent
tool names, but drops other declared names. An extension can therefore load
while its tool cannot be selected by a strict agent allowlist.
[#9179](https://github.com/fullsend-ai/fullsend/pull/9179) addresses MCP tool
patterns and child permissions; ordinary extension tools need the same
least-privilege property without acquiring an MCP identity.

The [threat model](../problems/security-threat-model.md) requires tool access
to remain bounded. This ADR aims to let an agent select an exact tool supplied
by a vetted Pi extension without enabling unrelated tools.

## Options

Removing the strict tool list exposes default tools and unrelated extension
tools. Hardcoding every plugin's name into the built-in translation table
couples independently versioned resources to runner releases. Resolving exact
declared names against the loaded extension registry preserves that boundary.

## Decision

Fullsend resolves exact declared Pi extension tool names against vetted
extensions to preserve strict tool selection.

Keep existing built-in translations. Treat an otherwise unmapped exact name
as a requested native extension tool, and resolve it only after loading the
runner-vetted extension set. A missing or ambiguous requested tool fails the
run before inference. It never falls back to all extension tools, Pi defaults
or a broader built-in tool. This decision does not add wildcard matching for
native extension tools or change MCP pattern semantics.

The effective tool set is explicit, including when empty. Hook authorization
uses the same canonical native name. Child runs, if supported, load the vetted
code that supplies a requested tool and receive only the intersection of
parent and child permissions. A runtime path that cannot enforce this refuses
the selection. Repository discovery cannot supply tools or alter identity;
extension integrity checks continue before launch and retry. Selecting a tool
does not grant shell, filesystem, network or publication authority.

## Forge scope

This runtime contract applies equally to GitHub, GitLab and Forgejo runs.
It introduces no forge-specific provider or authorization exception.

## Consequences

- A read-only agent can expose one extension tool while excluding builtin
  shell, editing and compatibility tools.
- Tool typos and missing extensions become explicit setup failures.
- Hook checks and child permission tests must use the same resolved names.
- Runner implementation and real-Pi denial tests are required before native
  extension selections are supported.
