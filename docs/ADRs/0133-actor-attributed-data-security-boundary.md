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
decision to provider-resolved bot identities. [ADR
0098](0098-entity-first-harness-evaluation.md) gives trusted Fullsend
invocation provenance authority to discover entities, while requiring each
action-indicating entity record to meet its actor's current permission
threshold. These decisions establish authorization and provenance for deciding
whether processing may begin; they do not make the text produced by an
authorized actor truthful, safe, or authoritative.

Agents nevertheless need to use actor-attributed data such as issue bodies,
comments, reviews, and linked records. The boundary applies only to records
whose source system can reliably establish the record and actor attribution,
and for which Fullsend can obtain current authorization from the applicable
trusted authorization provider. That provider may be the source system's
permission subsystem or a configured provider recognized by [ADR
0054](0054-require-authorization-on-all-agent-dispatch-paths.md) and [ADR
0107](0107-bot-identity-resolution-for-dispatch-authorization.md), including
OWNERS-derived permissions and registered-bot roles. GitHub pull request or
issue comments and Jira issue comments are representative records. Self-
asserted author or committer metadata is not sufficient; records whose actor
cannot be reliably established are redacted. That data may arrive in the
triggering event, a poll or snapshot, or a proactive read through another
authorized API. Comments from an authorized actor can contain genuine task
instructions that the agent must be able to heed. The boundary must still
prevent content from changing platform authority or capabilities, and must
protect against hidden Unicode, prompt injection, and indirect disclosure.

## Decision

Fullsend separates three decisions: whether a source actor is authorized for
the relevant processing path, whether source content may be admitted as data or
task guidance, and which fixed platform instructions and capabilities the model
receives.

Authorization under [ADR 0054](0054-require-authorization-on-all-agent-dispatch-paths.md)
and [ADR 0107](0107-bot-identity-resolution-for-dispatch-authorization.md), or
the trusted discovery provenance in [ADR
0098](0098-entity-first-harness-evaluation.md), permits actor-attributed
content to be considered for data admission, regardless of whether the
content was delivered by an event, polling, a snapshot, a proactive API read,
or a model-facing retrieval capability. Discovery provenance authorizes finding
the entity; it does not authorize the entity's content.

Authorization is per source-system record and uses that record's actor. The
actor who updates a change proposal may differ from the actor who wrote a
comment on it, so the latter's authorization is evaluated independently. The
checked actor is the principal authenticated by the source system as having
produced the exact current content revision: the creator for a never-edited
record, or the latest authenticated editor when the source system records an
edit. An event sender, poller, or retrieval credential never substitutes for
that revision actor. If the source system cannot establish attribution for the
current revision, the record is redacted.
actor's applicable permission or recognized role MUST be co-fetched in the
same authoritative API response, delivered in the same verified event payload,
or lazily resolved from the authoritative source when first needed. A lazy
result MAY be cached for the lifetime of the event or entity structure that
requested it, but MUST NOT be reused for an unrelated record or structure.
This is a logical lifetime, not an object-identity lifetime: re-reading or
re-fetching the data, constructing a new structure for a retry or requeue, or
terminating and re-invoking the agent ends the current lifetime and requires a
new authorization result. The new result MUST be resolved again even if the
implementation mutates or reuses the same in-memory structure. Within one
such lifetime, this ADR does not require a separate wall-clock expiry or
revalidation.
The actor MUST meet the applicable observation or mutation threshold for the
selected harness. A label grant authorizes only its verified label transition;
it does not authorize unrelated content from that actor or other actors.

Event-carried permission or role evidence may support initial event admission
and routing, but it MUST NOT satisfy later model-bound authorization checks.
For event-triggered runs, Fullsend MUST freshly resolve each record actor's
current authorization after event admission and before model admission. It
MUST freshly resolve that authorization again immediately before sandbox
initialization and immediately before the first model exposure when the record
will be exposed to a model in that sandbox; if those operations are contiguous,
the same fresh check may serve both gates. Revocation after first exposure
cannot retract content already in the model context, but every later retrieval
result has its own release check.
Non-event retrieval paths perform the equivalent fresh resolution after
retrieval admission and before model admission, followed by the
sandbox-initialization and first-exposure checks. Preloaded records use the
sandbox gates; a Fullsend-controlled capability invoked after sandbox
initialization MUST freshly resolve authorization immediately before releasing
each retrieved result to the model. That release check is the equivalent
checkpoint for mid-run retrieval and MUST apply the same filtering, cache, and
failure semantics. A cache entry MUST NOT cross any checkpoint; the actor MUST
still meet the applicable threshold at each check. Failure, unavailability, or
loss of authorization at a check redacts the record and, if no authorized
records remain, denies the run.

Only a source-system record whose actor attribution can be reliably
established, and for which the applicable source-system or trusted-provider
authorization can be resolved, is eligible for this boundary. A self-asserted
Git author, committer, or account association does not establish actor identity
for admission; if the required attribution or authorization cannot be
established, the record is redacted.

This is bounded trust: content with authorized provenance may inform analysis
and, where the harness permits, provide task instructions. It cannot grant
platform authority or become a source of credentials, identity, permissions,
routing, harness configuration, or capabilities. Unrecognized, unauthorized,
unattributed, ambiguous, or unverifiable source-system records are redacted
before model admission; there is no data-only fallback for them.

Before any selected actor-attributed content reaches a model, the host-side
input boundary MUST verify its source-system record, actor attribution, actor
authorization, and bounds and apply the versioned content-filtering pipeline.
The pipeline MUST perform Unicode and control-character safety handling
(including hidden and bidirectional characters), sensitive-data and secret
redaction, prompt-injection scanning, and size limits. The model may receive
only the resulting filtered representation, with explicit data delimitation and
any bounded omission or finding metadata. Raw source content and rejected or
unauthorized content MUST NOT be exposed to the model or retained in ordinary
run artifacts.

Filtering is fail-closed for missing or unverifiable provenance, missing or
failed actor authorization, detector failure or timeout, and content that the
active policy cannot safely represent.
A blocking finding produces an omission or quarantine record rather than raw
model input; the filter manifest records the disposition, digest, and bounded
diagnostics without retaining the rejected body. The exact character policy,
detector versions, dispositions, and compatibility rules belong in the
versioned content-filter contract shared by all model-bound data paths.

The model-facing prompt MUST preserve the provenance and boundary of filtered
source-system records relative to system/developer instructions and
harness-provided task instructions. Content from an authorized actor MAY guide
task execution within the configured harness scope, including by requesting an
otherwise permitted change. An authorized record is one trusted text unit for
this boundary: quoted, forwarded, or copied text inside that record is not
assigned a separate trust level, and the entire filtered record text is
admitted under the record actor's authorization. Separate linked records still
require their own attribution and authorization. Content from a redacted
source is absent from the model context. No source content may re-dispatch the
agent or elevate its identity, permissions, tools, capabilities, or
platform-owned instructions.
The platform's pre-dispatch command parser and deterministic, schema-validated
host/post-script paths remain the only paths that select a stage or perform a
forge mutation.

This is a target contract. At the time of this ADR, the complete boundary is
not enforced on every model-bound path; existing adapters and runtimes retain
their compatibility behavior until they adopt the common attribution,
authorization, filtering, and redaction contract.

Fullsend-controlled retrieval capabilities are part of this target boundary
and MUST return source-system records with the same attribution,
authorization, filtering, and redaction guarantees. Agent-authored arbitrary
API tools are outside the platform guarantee; a harness that requires this
boundary MUST use controlled capabilities rather than expose raw
source-system reads to the model.

## Consequences

- Authorized contributors and recognized bots can provide useful context and task guidance, regardless of how the data is retrieved, without gaining platform authority.
- Every forge adapter, poller, snapshot builder, proactive data source, and Fullsend-controlled model retrieval capability needs provenance binding, per-record authorization, bounded retrieval, and the common filtering pipeline before its content is model-visible.
- Hidden-character and prompt-injection findings may omit legitimate-looking content, so the model and operators need bounded diagnostics rather than an assumption that all source text will be preserved.
- Filtering reduces, but cannot prove the absence of prompt injection; immutable instructions, least privilege, output validation, and deterministic host-side mutations remain required defenses.
- Quoted, forwarded, or copied text is intentionally not detected or assigned a separate trust level inside an authorized record. A trusted actor or bot can therefore relay hostile text as part of a trusted record; this residual risk is accepted in exchange for a whole-record admission model, and is mitigated by trusted-actor caution, filtering, explicit delimitation, least privilege, output validation, deterministic host-side mutations, and the fresh authorization checkpoints.
- Any harness that admits an authorized record as task guidance MUST keep that guidance within its configured scope and enforce least privilege, output validation, and deterministic host-side mutation rules; the whole-record rule does not authorize a new stage, capability, provider, or separate source-system record.
- The data-filter contract becomes a versioned security boundary whose changes require compatibility review and regression testing across all model-bound data consumers.
