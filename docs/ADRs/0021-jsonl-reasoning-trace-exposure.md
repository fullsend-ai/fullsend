---
title: "21. JSONL reasoning trace exposure from sandboxed agents"
status: Accepted
relates_to:
  - operational-observability
  - security-threat-model
topics:
  - observability
  - security
  - sandbox
  - retro
---

# 21. JSONL reasoning trace exposure from sandboxed agents

Date: 2026-04-15

## Status

Accepted

## Context

The retro agent needs access to the raw JSONL conversation transcripts that
agents produce inside sandboxes. These JSONL files capture the full reasoning
trace: prompts, completions, tool calls, thinking, and intermediate state.
They must be extracted from the sandbox after the agent run completes.

Three consumers need these traces:

- **Retro agent** — automated post-mortem analysis of agent reasoning,
  failures, and missed opportunities.
- **Human debugging** — replaying agent sessions with tools like
  [claude-replay](https://github.com/es617/claude-replay) to understand
  what happened and why.
- **Human session resumption** — picking up a Claude conversation where the
  agent left off using tools like
  [ci:continue-session](https://github.com/openshift-eng/ai-helpers/blob/main/plugins/ci/commands/continue-session.md),
  preserving full context (e.g., when an agent escalates or gets stuck).

This decision builds on three prior ADRs:
[ADR 0016](0016-unidirectional-control-flow.md) establishes that the retro
agent's feedback loop operates through the forge, not by modifying its own
harness. [ADR 0017](0017-credential-isolation-for-sandboxed-agents.md) ensures
credentials never enter the sandbox.
[ADR 0018](0018-scripted-pipeline-for-multi-agent-orchestration.md) provides
the pipeline in which the retro agent consumes traces from prior stages.

The security concern is that JSONL files inherently contain protected data.
ADR 0017 keeps credentials out, but protected *data* still flows in through
multiple channels: pre-scripts prefetching from private repos and external
systems, REST API responses from the credential proxy, the repo content
itself, and validation script outputs. All of this enters the agent's context
window and ends up in the JSONL.

## Options

### Option A: Binary gate

Suppress the entire JSONL when scripted flags indicate the agent accessed
sensitive data through pre-scripts, REST API, or validation scripts. Return
an explanatory message instead.

**Trade-offs:** Safe and deterministic, but over-restrictive. For private repos,
the flag would almost always be raised — the prefetch model always provides
repo data. The retro agent loses observability for the runs that need it most.

### Option B: Credential-pattern sanitizer

Regex-based scanning to strip tokens, API keys, PEM blocks, and other
credential formats before exposing the JSONL.

**Trade-offs:** Well-scoped and deterministic, but credentials should already
be absent per ADR 0017. Only addresses credentials, not private code, PII,
or data from external systems.

### Option C: Heuristic sensitive-data sanitizer

Broader pattern matching — emails, IP addresses, internal URLs, PII-like
patterns — applied to the JSONL before exposure.

**Trade-offs:** Wider coverage than credential-only scanning, but the boundary
between "sensitive" and "useful context" is inherently fuzzy. High
false-positive risk (over-redacting, making the JSONL useless for session
resumption) and false-negative risk (missing unanticipated patterns).

### Option D: LLM-based redaction

Run the JSONL through a model that identifies and redacts sensitive content.

**Trade-offs:** More contextually aware than regex, but non-deterministic,
costly, and introduces a new prompt injection surface — the original JSONL
could manipulate the redaction model into preserving content it should remove.

### Option E: Structured extraction

Extract only structured metadata (tool calls, decisions, timing) rather than
the raw JSONL.

**Trade-offs:** Secure by construction, but defeats the purpose. Session
resumption requires the full message history. The retro agent needs the
reasoning, not just the outcomes.

### Option F: Owner-scoped storage with credential scanning

Store the full JSONL where repo owners have default access and can grant
per-JSONL access to others. Apply credential scanning as an invariant check
rather than a sanitization strategy. Allow opt-in suppression via agent
configuration for agents handling data from protected sources beyond the
target repo.

**Trade-offs:** Preserves full fidelity for all three consumers. Relies on
access controls rather than content redaction. Credential detection catches
isolation breaches in ADR 0017's model. Requires a storage system with
granular access controls.

## Decision

Use owner-scoped storage with credential scanning as defense-in-depth
(option F).

**JSONL files are extracted from the sandbox and stored where repo owners
have default access.** Owners can grant access to specific JSONL files to
other users as needed. The specific storage mechanism is a separate decision.

**Credential scanning is an invariant check, not a sanitization strategy.**
The JSONL is scanned for credential patterns before storage. If credentials
are detected, the JSONL is suppressed and an incident is raised — because
credential presence means
[ADR 0017](0017-credential-isolation-for-sandboxed-agents.md)'s isolation
model was breached. This is a bug detector, not a redactor.

**Agents can opt in to JSONL suppression.** The agent's YAML configuration
can declare JSONL exposure as restricted for agents that handle data from
protected sources beyond the target repo (external APIs, cross-repo data
via the REST proxy). This declaration is reviewed through the normal
PR/CODEOWNERS process, consistent with ADR 0017's principle that agent
configuration drives sandbox infrastructure.

**Default is exposed, not suppressed.** JSONL extraction is the default for
all agents. Suppression is the exception, explicitly declared in config.

Content-based sanitization (options C, D) and structured extraction (option E)
were rejected because they either degrade the JSONL to the point where session
resumption and retro analysis break, or they provide a false sense of security
through unreliable heuristics. The binary gate (option A) was rejected because
it suppresses JSONL for nearly all useful runs on private repos.

## Consequences

- The retro agent, human debugging tools, and session resumption all get
  full-fidelity reasoning traces by default.
- Credential detection in JSONL files surfaces isolation breaches in
  ADR 0017's model as incidents rather than silently redacting them.
- A storage system with owner-scoped access and per-artifact sharing is
  required — the choice of system is deferred to a future decision.
- Agents handling cross-repo or external protected data must explicitly
  declare JSONL suppression in their configuration, adding a review
  obligation through CODEOWNERS.
- JSONL files from private repos contain private data by nature — access
  control, not content sanitization, is the security boundary.
