---
title: "154. Correlate work-item and behaviour-scenario traces"
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

# 154. Correlate work-item and behaviour-scenario traces

Date: 2026-10-22

## Status

Accepted

This records the chosen topology; implementation is pending.

## Context

An ADLC flow crosses separately launched agents and CLI commands, forge API
calls, and the issues and PRs they create. A trace for one invocation cannot
show how an issue led to a PR and follow-up work. Behaviour tests have the same
gap: a failed scenario has no trace grouping its driver operations with the
issues, PRs, and agent runs it produced. Without durable correlation, operators
must reconstruct these paths across traces to diagnose failures and understand
which work produced a change.

[ADR 0050](0050-distributed-tracing-instrumentation.md) establishes OpenTelemetry
instrumentation and W3C context propagation; [ADR 0144](0144-reuse-poll-and-entity-bound-run-processes.md)
keeps follow-up cycles in one process. Neither gives a work item a context that
survives independent launches or gives a behaviour-test scenario a root span.
Trace context in an editable description or comment is external input: a valid
`traceparent` does not authenticate its contents, and its sampling flag is
caller-provided ([W3C Trace Context](https://www.w3.org/TR/trace-context/#trace-flags)).
Production launches must authenticate the previously published entity context
to continue its parent span; behaviour-test launches also need it to carry the
scenario parent across a separate forge-triggered workflow. A span's parent is
fixed when it starts, so a run must verify a signed parent before adopting it.

This ADR aims to give each issue or PR durable trace continuity and group each
behaviour-test scenario with the work it creates.

## Options

### Correlate separate run traces with work-item attributes

This avoids long-lived TraceIDs, but reconstructing an issue or PR's lifecycle
requires a backend-specific query and does not provide parent-child topology.

### Derive contexts from entity identity and store no envelope

A stable entity-derived TraceID makes independent launches easy to group, but
without a stored parent SpanID they cannot continue the entity's span tree or
carry a behaviour-test scenario parent into a separate workflow.

### Give each work item a durable context

This lets independent launches continue one trace and lets explicit creation
links connect ordinary work items, at the cost of potentially large,
long-lived traces.

### Store context in hidden entity metadata

Hidden HTML in a forge item's description is discoverable without adding a
visible comment, but updating a body can race a human edit. Jira's ADF does not
preserve hidden HTML, so Jira needs an entity-property carrier.

## Decision

We adopt signed, entity-bound trace contexts so independent launches on one
issue or PR continue its trace, while each behaviour-test scenario and its
created work share one TraceID.

- **Work-item continuity:** Give each issue or PR a stable entity-derived
  context. Each Fullsend launch verifies and continues its signed context when
  available; otherwise it starts from the entity-derived context. Publish the
  signed context as hidden metadata on the entity for later launches to find.
  Metadata-only writes preserve human-authored body text and do not trigger
  another agent run.
- **Context authentication:** Accept stored context only when its signature
  verifies with the configured public key and its installation, repository,
  and destination entity identities match the current event. A `traceparent`
  alone is never trusted.
- **Created work:** Fullsend-created ordinary issues and PRs get their own
  entity contexts and an OpenTelemetry span link to the source item. Do not
  infer links from human-authored references. Work created within a behaviour
  test instead inherits that scenario's context and TraceID, including
  Fullsend-created descendants of scenario-created entities.
- **Behaviour tests:** Start one trace per scenario. Fullsend and BT-owned
  commands, API calls, agent runs, and entities created by that scenario use
  distinct spans under the scenario TraceID.

The signed envelope is versioned and binds the TraceID and parent SpanID to the
Fullsend installation, forge instance, repository, destination entity, and,
for behaviour-test work, scenario. For ordinary Fullsend-created work it also
carries the source span context as a link, not as a parent. The signature covers
all context and identity fields. A versioned derivation from immutable forge
instance, repository, and entity IDs provides a stable fallback and makes
parallel first events converge. If those IDs cannot be resolved, Fullsend
records available diagnostics and postpones agent dispatch rather than starting
uncorrelated work.

The signing key is private material; the verification key is public. Layered
configuration carries the public key and a protected reference to the signing
key. A standard install generates the pair and stores the private key in
repository secrets. The BT suite supplies its verification key and a protected
signing-key source through install CLI flags; flag values must not contain raw
private-key bytes. Keep signing material out of agent sandboxes and redact it
from diagnostics and telemetry. If no verification key or valid signature is
available, ignore stored context and use the entity-derived context.

Start a bootstrap trace from the entity-derived context before resolving keys,
verifying signatures, and performing normal dispatch authorization. Record
those checks as spans in it. Without a valid signed context, continue from the
bootstrap trace. With a valid signature, start the entity/run span under the
signed context and link it to the completed bootstrap span. OpenTelemetry fixes
a span's parent at creation, so bootstrap spans remain in their original trace.
Use only the admitted TraceID and parent SpanID; apply Fullsend's sampling
policy and discard incoming trace flags, `tracestate`, and baggage.

An entity ID does not exist until its create request succeeds. For
Fullsend- or BT-created work, put a signed pending-creation marker in the create
request, then replace it with a signed envelope bound to the returned entity
ID. The pending marker never grants trace context or a span link; a workflow
that sees it re-reads the entity and waits for a bounded interval for the bound
envelope before adopting the context or link. If the bound envelope does not
arrive or fails verification, use the entity-derived fallback. This prevents a
copied envelope from attaching an unrelated entity to a trace.

BT keeps scenario context in memory for its own spans and propagates the signed
envelope through entity metadata so a separate forge-triggered Fullsend
workflow can continue the scenario trace. On GitHub, GitLab, and Forgejo, use
hidden HTML in the issue or PR body. For Jira issues created by Fullsend or BT,
use an issue property, including the pending marker in the create request, then
replace it with the signed, entity-bound property after Jira returns the issue
ID. Jira's create operation accepts issue properties in the request
([Create issue API](https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issues/)).
For Jira issues created elsewhere, publish the signed context as a property on
the first Fullsend-controlled comment. An event that sees a pending marker
waits for a bounded interval for the bound property before adopting the
scenario parent. Fullsend verifies the envelope and destination identity, then
creates the entity anchor under the admitted context. Fullsend-created
descendants of a scenario entity propagate the same scenario TraceID. Dispatch
authorization still runs normally. BT needs no separate durable context store.
Export BT spans with remote Fullsend spans, keep scenario context per
invocation, and drain the collector once during suite teardown; later workflows
may fall outside that bounded window.

## Forge scope

The work-item contract covers GitHub issues and PRs, GitLab issues and merge
requests, and Forgejo issues and PRs. Each adapter exposes immutable forge
instance, repository, and entity identifiers and carries signed context across
its dispatch path; Forgejo support applies when its adapter is implemented and
hidden-metadata behavior is verified. Jira uses issue properties for
Fullsend- or BT-created issues and comment properties on Fullsend's first
comment for issues created elsewhere; Jira has no change-proposal entity in
this contract.

## Consequences

- Operators can follow each work item's launches and explicit creation links;
  scenario operations and scenario-created entities share one trace.
- Long-lived traces can exceed backend size or retention limits, and workflows
  finishing after the bounded BT collection window may be missing from its
  artifact.
- Implementation requires immutable IDs, protected key provisioning,
  entity-bound envelopes, bounded dispatch deferral, local and remote
  exporters, and CLI, forge, and test-driver spans; exporter failures remain
  fail-open under ADR 0050. Key rotation retains verification keys for
  metadata still in use.
- BT adds signing and a metadata write for each created entity; provision keys
  once per leased pool and overlap one bounded collector drain with suite
  teardown so tracing adds no scenario wave or per-PR gate time. Trace output
  complements the suites' existing redacted debug artifacts.
- Missing keys or invalid/misbound signatures make stored context unusable, so
  Fullsend uses the entity-derived fallback and BT entities do not join the
  scenario trace; dispatch authorization remains independent, with bootstrap
  spans linked to any admitted trace.
