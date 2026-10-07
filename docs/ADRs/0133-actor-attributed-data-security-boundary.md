---
title: "133. Apply a data security boundary to authorized actor-attributed content"
status: Accepted
relates_to:
  - agent-architecture
  - security-threat-model
topics:
  - authorization
  - data-security
  - prompt-injection
  - events
---

# 133. Apply a data security boundary to authorized actor-attributed content

Date: 2026-10-07

## Status

Accepted

## Context

[ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md) decides
which actors are eligible to cause agent processing. [ADR
0107](0107-bot-identity-resolution-for-dispatch-authorization.md) extends that
decision to provider-resolved bot identities. Those decisions establish
authorization and provenance for deciding whether processing may begin; they do
not make the text produced by an authorized actor truthful, safe, or
authoritative.

Agents nevertheless need to use actor-attributed data such as issue bodies,
comments, reviews, commit messages, and linked records. That data may arrive
in the triggering event, a poll or snapshot, or a proactive read through
another authorized API. Comments from an authorized actor can contain genuine
task instructions that the agent must be able to heed. The boundary must still
prevent content from changing platform authority or capabilities, and must
protect against hidden Unicode, prompt injection, and indirect disclosure.

## Decision

Fullsend separates three decisions: whether a source actor is authorized for
the relevant processing path, whether source content may be admitted as data or
task guidance, and which fixed platform instructions and capabilities the model
receives.
Authorization under ADR 0054, ADR 0107, or the trusted Fullsend-originated path
in ADR 0098 permits actor-attributed content to be considered for data
admission, regardless of whether the content was delivered by an event,
polling, a snapshot, or a proactive API read. This is bounded trust: content
with authorized provenance may inform analysis and, where the harness permits,
provide task instructions. It cannot grant platform authority or become a
source of credentials, identity, permissions, routing, harness configuration,
or capabilities. Admission is per source record; an authorized editor does not
launder earlier content authored by another actor, and an unattributed or
ambiguous producer is withheld from instruction use unless explicit platform
provenance covers it.

Before any selected actor-attributed content reaches a model, the host-side
input boundary MUST verify its source, actor attribution, and bounds and apply
the versioned content-filtering pipeline. The pipeline MUST perform Unicode and
control-character safety handling (including hidden and bidirectional
characters), sensitive-data and secret redaction, prompt-injection scanning,
and size limits. The model may receive only the resulting filtered
representation, with explicit data delimitation and any bounded omission or
finding metadata. Raw source content and rejected content MUST NOT be exposed
to the model or retained in ordinary run artifacts.

Filtering is fail-closed for missing or unverifiable provenance, detector
failure or timeout, and content that the active policy cannot safely represent.
A blocking finding produces an omission or quarantine record rather than raw
model input; the filter manifest records the disposition, digest, and bounded
diagnostics without retaining the rejected body. The exact character policy,
detector versions, dispositions, and compatibility rules belong in the
versioned content-filter contract shared by all model-bound data paths.

The model-facing prompt MUST preserve the provenance and boundary of filtered
source records relative to system/developer instructions and harness-provided
task instructions. Content from an authorized actor MAY guide task execution
within the configured harness scope, including by requesting an otherwise
permitted change. Content from an unauthorized, unattributed, or rejected
source MUST NOT be treated as an instruction. No source content may re-dispatch
the agent or elevate its identity, permissions, tools, capabilities, or
platform-owned instructions. The platform's pre-dispatch command parser and
deterministic, schema-validated host/post-script paths remain the only paths
that select a stage or perform a forge mutation.

## Consequences

- Authorized contributors and recognized bots can provide useful context and task guidance, regardless of how the data is retrieved, without gaining platform authority.
- Every forge adapter, poller, snapshot builder, and proactive data source needs provenance binding, bounded retrieval, and the common filtering pipeline before its content is model-visible.
- Hidden-character and prompt-injection findings may omit legitimate-looking content, so the model and operators need bounded diagnostics rather than an assumption that all source text will be preserved.
- Filtering reduces, but cannot prove the absence of prompt injection; immutable instructions, least privilege, output validation, and deterministic host-side mutations remain required defenses.
- The data-filter contract becomes a versioned security boundary whose changes require compatibility review and regression testing across all model-bound data consumers.
