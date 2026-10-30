# Private context in public agent workflows

How can an agent use private planning or issue-tracking context while working
on a public repository without publishing information beyond its authorized
audience?

## The problem

A public repository can have legitimate dependencies on private information:
an internal issue tracker, a business roadmap, an unreleased requirement or a
restricted design discussion. Giving an agent access may improve its work.
That read permission does not also authorize public disclosure.

The boundary applies to the agent's whole invocation. Protected input can
appear in runtime-emitted thinking, assistant text, tool summaries, lifecycle
script output, validation feedback, artifacts, traces and forge writes. A
private artifact store does not make a public review body safe. A quiet
console does not protect a transcript uploaded as a public-repository artifact.

This problem is independent of the tracker product, forge or organization.
It also arises without an attack: quoting relevant context is a natural part
of explaining a review. Prompt injection creates an additional path by asking
an agent to repeat or summarize protected material.

## Existing design and gaps

[ADR 0021](../ADRs/0021-jsonl-reasoning-trace-exposure.md) chooses access control
for full-fidelity transcripts rather than relying on heuristic sanitization.
It also calls for declared suppression for protected external sources. This
is design intent; it should not be taken as evidence that every execution
path already enforces such a switch.

Live runtime-event rendering shipped through
[PR #3186](https://github.com/fullsend-ai/fullsend/pull/3186).
[#6734](https://github.com/fullsend-ai/fullsend/issues/6734) tracks configurable
artifact storage, including disabling forge-native uploads.
[ADR 0050](../ADRs/0050-distributed-tracing-instrumentation.md) addresses
telemetry separately: private remote export does not change the audience of
local files, console output or public reviews.

[#2513](https://github.com/fullsend-ai/fullsend/issues/2513) tracks warnings
about private Jira data in public-repository artifacts.
[#1189](https://github.com/fullsend-ai/fullsend/issues/1189) identifies private
content in public retro issues. These establish parts of the problem; neither
a documentation warning nor a destination allow-list proves that derived
content is safe to publish.

## Boundaries to account for

- **Observability:** stdout/stderr, CI annotations, summaries, raw streams,
  transcripts, local telemetry, remote traces, debug and security records.
- **Public effects:** reviews, inline comments, issues, PR descriptions,
  generated code and tests, filenames, commits and other agent-driven writes.
- **Continuation:** retro, fix, debugging, resumption and chained agents that
  consume prior private evidence.
- **Policy authority:** who may authorize disclosure or impose a blocking
  decision on the public project, and which configuration is trusted.

Credential isolation and scanning remain necessary. They do not classify
ordinary private prose or determine whether a derived business conclusion
may be published. Moving a runner onto private infrastructure also does not
change the audience of logs published by a public-repository workflow.

## Options and trade-offs

### Keep protected processing in a private execution environment

Run the confidential stage in an access-controlled worker or control repository.
Publish only approved material to the public project.

This narrows exposure and can retain useful private evidence. It requires
cross-repository authorization, dispatch, artifact retrieval and status
correlation. It is a deployment pattern to evaluate, not an assertion that
Fullsend already provides a drop-in installation for it.

### Restrict the public job's inputs to approved public material

Provide an explicitly approved public projection of requirements. Keep private
tracker access out of that invocation.

This gives the public agent information it can explain and cite. Someone must
maintain the projection, prevent stale requirements and decide what may be
released. It may omit context needed for private policy decisions.

### Allow protected context with controlled output destinations

Route full evidence to private storage and internal trace endpoints. Possible
artifact destinations include a private S3 bucket or a private OCI registry
accessed with ORAS. Restrict public logs to approved operational metadata and
constrain public writes.

This can preserve existing workflow placement and detailed debugging. It
requires enforcement across every lifecycle stage and publication path,
including custom scripts and failures. Public fallback when private storage
fails would defeat the boundary. Trusted policy must apply before private
reads and cannot be weakened by the untrusted PR or agent.

### Separate public technical review from private policy evaluation

A public review cites public evidence. A private evaluator checks protected
planning constraints and retains its rationale privately. An authorized
publisher releases only an approved result or sends it for human review.

This avoids asking one free-form public explanation to carry a private
rationale safely. It introduces result binding, freshness, merge-policy and
operator responsibilities. It does not eliminate information disclosure:
a public hold can reveal a private decision, and repeated proposals may
reveal more about the hidden policy.

These options can be combined. A project might use public requirements for
technical review, private execution for planning checks and private evidence
storage for the latter. The choice depends on the permitted disclosures and
the project's governance.

## Example: a private roadmap affects a public PR

Suppose a proposal conflicts with a confidential roadmap. An unrestricted
review might explain the conflict by quoting a private milestone or business
rationale. A code agent could copy private acceptance criteria into a public
PR description. These are hypothetical examples, independent of any product
or organization.

The private reviewer's evidence and explanation remain internal. If the project
explicitly permits it, a publisher could issue the fixed public message
"Maintainer review required before merge."

A required check could enforce that hold, but only with trusted results tied
to the repository, current PR head and applicable policy. Missing or stale
results must not authorize merge. The public decision bit is itself a
release of information. If even that signal is unacceptable, a uniform
maintainer gate may be more appropriate.

Knowing a downstream contributor's private priorities is also different from
having authority to veto an upstream contribution. Private planning policy
should not be misrepresented as a publicly demonstrated code defect or fed
into an automated fix loop as though it were an actionable public finding.

## Evidence needed before calling a mode confidential

Use synthetic protected markers rather than real private records. Exercise
runtime text, commands and tool responses, lifecycle scripts, validation,
errors, retries, timeouts, cancellations and storage failures. Inspect actual
public logs, artifacts, annotations, summaries and forge writes for disclosure.

Verify that evidence remains accessible to authorized private consumers,
that unauthorized readers are denied, and that subsequent agents preserve
the same boundary. Test bypass attempts through direct APIs, shell tools,
custom scripts and generated patches. Credential scanning or a second LLM's
approval alone cannot demonstrate the required confidentiality property.

## Relationship to other problem areas

- [Security threat model](security-threat-model.md#indirect-information-disclosure):
  indirect disclosure and prompt injection can produce the same public leak.
- [Operational observability](operational-observability.md): preserving useful
  debugging evidence while controlling its audience.
- [Agent infrastructure](agent-infrastructure.md): execution placement and
  infrastructure ownership do not alone determine output visibility.
- [Downstream/Upstream](downstream-upstream.md): authority to reconcile private
  business priorities with the public project's direction.

## Open questions

- How should protected sources and run classification be declared and enforced
  before private data is read? What happens when sensitivity changes mid-run?
- Which policy belongs in repository, harness or operator configuration, and
  which trusted authority can approve an exception?
- What console metadata is acceptable, including errors and timing signals?
- How should fixed public results be bound to a revision and authorized
  publisher, and how should stale or unavailable evidence affect merge?
- Can useful publication permissions be expressed once across built-in and
  custom agents without relying on prompt-only compliance?
- Which free-form outputs require human approval, and how is that approval
  recorded without disclosing its private supporting evidence?
- How do private artifact readers, resumption and retro avoid expanding the
  original audience? What retention and deletion guarantees are needed?
- Which configurations should be rejected until their complete execution and
  publication paths can honor the selected boundary?
