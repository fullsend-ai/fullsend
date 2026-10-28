---
title: "152. Correlate work-item and behaviour-scenario traces"
status: Accepted
relates_to:
  - operational-observability
  - testing-agents
topics:
  - observability
  - opentelemetry
  - behaviour-testing
  - work-items
---

# 152. Correlate work-item and behaviour-scenario traces

Date: 2026-10-22

## Status

Accepted

This records the chosen topology; implementation is pending.

## Context

An ADLC flow crosses independently launched agents and CLI commands, forge API
calls, and the issues and PRs they create. A trace for one invocation cannot
show the full path from an originating issue through its resulting PR and
follow-up work. Without durable correlation, operators must reconstruct that
path across separate traces. Behaviour tests have the same gap: a failed
scenario does not have one trace that groups its test-driver operations with
the issues, PRs, and agent runs produced by that scenario. This makes it harder
to diagnose failures and understand which work produced a change.

[ADR 0050](0050-distributed-tracing-instrumentation.md) provides OpenTelemetry
instrumentation and W3C context propagation, while [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)
keeps follow-up cycles in one process. Neither establishes a durable context
for a work item across separate launches or a root span for a behaviour-test
scenario. To help people find a trace from its issue or PR, Fullsend can publish
the context as hidden metadata on that item. A status comment is one possible
discovery surface, but it cannot be the context's source of truth: status
comments are optional and may be deleted when a run completes
([`statuscomment.go` at `abe0c746`](https://github.com/fullsend-ai/fullsend/blob/abe0c746c755b16a9ba8d8db63991c018544876a/internal/statuscomment/statuscomment.go#L248-L279),
[`statuscomment.go` at `abe0c746`](https://github.com/fullsend-ai/fullsend/blob/abe0c746c755b16a9ba8d8db63991c018544876a/internal/statuscomment/statuscomment.go#L346-L377)).
Behaviour-test hooks currently create a test world without a scenario span
([`init.go` at `abe0c746`](https://github.com/fullsend-ai/fullsend/blob/abe0c746c755b16a9ba8d8db63991c018544876a/pkg/behaviourtest/suite/init.go#L18-L58)).

OpenTelemetry requires a child span to inherit its parent's TraceID;
[span links](https://opentelemetry.io/docs/specs/otel/trace/api/) relate spans
without making one a child of the other. This constrains how production
work-item traces connect to one another and how scenario-created items join the
scenario trace. Trace context in an editable description or comment is
external input: a valid `traceparent` is not authenticated, and its sampling
flag is caller-provided ([W3C Trace Context](https://www.w3.org/TR/trace-context/#trace-flags)).
Fullsend therefore needs a signature to establish that stored trace metadata
was produced by a trusted signer. A span's parent is fixed when it starts, so
spans recorded before signature verification cannot later be reparented; a link
can connect them to the admitted trace.

## Options

### Correlate separate run traces with work-item attributes

This avoids long-lived TraceIDs, but reconstructing one issue or PR's lifecycle
requires a backend-specific query and does not provide parent-child topology.

### Give each work item a durable context

This joins independent launches in one trace and lets explicit creation links
connect work items, at the cost of potentially large, long-lived traces.

### Hide context in descriptions

GitHub and GitLab can hold a hidden HTML marker in an issue or change-proposal
description, but updating an existing body can race a human edit; on GitHub,
an issue edit may enter another dispatch path
([`fullsend.yaml` at `abe0c746`](https://github.com/fullsend-ai/fullsend/blob/abe0c746c755b16a9ba8d8db63991c018544876a/.github/workflows/fullsend.yaml#L20-L24)).
Jira's ADF does not hide HTML
([`adf.go` at `abe0c746`](https://github.com/fullsend-ai/fullsend/blob/abe0c746c755b16a9ba8d8db63991c018544876a/internal/forge/jira/adf.go#L20-L31)).
A comment or property is a safer discovery copy because the derived identity
does not require either one.

## Decision

Adopt this trace topology for end-to-end ADLC activity:

- **Work-item continuity:** Each issue or PR has an entity context that
  parents Fullsend launches and its instrumented commands and API calls. With
  no trusted incoming parent, derive a stable context from immutable entity
  identity. With a trusted incoming parent, make the entity anchor its child.
  Publish a signed context envelope for discovery in the earliest
  Fullsend-controlled top-level comment (hidden HTML; a Jira comment property).
- **Context authentication:** Accept trace metadata from an issue, PR, or
  comment only when its signature verifies with Fullsend's configured public
  key. Resolve the key pair through layered configuration, with repository
  secrets as the final fallback. A standard install generates the pair during
  setup and stores it in repository secrets. The BT suite supplies its pair
  through install CLI flags. If the pair or a valid signature is unavailable,
  ignore the stored context and use the entity-derived context.
- **Created work:** When Fullsend creates an issue or PR from an issue, add an
  OpenTelemetry span link from the new item's anchor to the source item. Create
  links only for Fullsend actions, not from human-authored references.
- **Behaviour tests:** Each scenario has one trace. Fullsend and BT-owned
  commands, API calls, agent runs, and issues or PRs created by that scenario
  share its TraceID, with distinct spans for each entity and operation. The
  signed envelope contains the versioned TraceID and parent SpanID, plus
  identifiers for the installation, repository, and scenario. Its signature
  authenticates trace metadata; it does not authorize dispatch.

Start a bootstrap trace from the entity-derived context before resolving keys,
verifying signatures, and performing normal dispatch authorization. Record
those checks as spans in it. Without a valid signed context, continue the
work-item trace from the bootstrap trace. With a valid signature, start the
entity/run span under the signed context and link it to the completed bootstrap
span. Keep bootstrap spans in their original trace: OpenTelemetry fixes a
span's parent at creation. Use only the admitted TraceID and parent SpanID;
apply Fullsend's sampling policy and discard incoming trace flags, `tracestate`,
and baggage. A shared helper derives the fallback context from a durable forge
instance namespace and immutable entity identity so parallel first events
converge. If identity cannot be resolved, defer the launch.

BT keeps scenario context in memory for its own spans and includes a signed
scenario-context envelope in the forge's hidden metadata carrier when it
creates an issue or PR. On GitHub, GitLab, and Forgejo, this is hidden HTML in
the description or body; Jira uses a comment property. The envelope binds the
context to the installation, repository, and scenario. The BT suite passes
its signing and verification keys through the install CLI flags, so the create
request carries the signed parent context with the entity before its `opened`
event starts a separate Fullsend workflow
([`fullsend.yaml` at `abe0c746`](https://github.com/fullsend-ai/fullsend/blob/abe0c746c755b16a9ba8d8db63991c018544876a/.github/workflows/fullsend.yaml#L20-L28)).
That workflow resolves the verification key through layered configuration,
verifies the signature, then derives the entity anchor under the scenario
context from immutable entity identity. Each later launch verifies the
envelope and derives the same entity context. The hierarchy is scenario, entity
anchor, then entity activity, all under the scenario's TraceID. Event
authorization still runs normally. This uses the signed context carried by the
entity; BT needs no separate context store. Export BT spans with remote
Fullsend spans and keep scenario context per test invocation. Shared setup
stays suite-scoped. Drain the collector for a bounded period; later workflows
may fall outside that window.

## Forge scope

The work-item contract covers GitHub issues and PRs, GitLab issues and merge
requests, and Forgejo issues and PRs. Each adapter must expose immutable
instance and entity identifiers (plus original container identity when needed)
and carry context across its dispatch path; Forgejo support applies when its
adapter is implemented and hidden-comment behavior is verified. Jira issues
follow the same topology, with comment properties in place of hidden HTML;
Jira has no change-proposal entity in this contract.

## Consequences

- A work item's launches form one trace, explicit creation links connect related
  work items, and BT-created entities share their scenario trace.
- Long-lived traces can exceed backend size or retention limits, and workflows
  finishing after the bounded BT collection window may be missing from its
  artifact.
- Implementation requires immutable IDs, early propagation, local and remote
  exporters, and CLI, forge, and test-driver spans; missing identity postpones
  launch, exporter failures remain fail-open under ADR 0050, and marker events
  must not retrigger agent work.
- Standard install generates a key pair and stores it in repository secrets;
  BT supplies its pair through install flags. Rotation must retain verification
  keys for signed metadata that remains in use.
- Missing keys or invalid signatures make stored context unusable, so Fullsend
  uses the entity-derived fallback and BT entities do not join the scenario
  trace; dispatch authorization remains independent, with bootstrap spans
  linked to any admitted trace.
