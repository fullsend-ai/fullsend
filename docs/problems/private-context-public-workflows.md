# Private Context in Public Agent Workflows

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
ADR 0021's owner-scoped storage has not been implemented for any repository.
The [GitHub action](https://github.com/fullsend-ai/fullsend/blob/823f3c22b66bdbb4e8d046e3ca1bf787ce5330ff/action.yml#L573-L578)
and [GitLab CI template](https://github.com/fullsend-ai/fullsend/blob/823f3c22b66bdbb4e8d046e3ca1bf787ce5330ff/internal/scaffold/fullsend-repo-gitlab/.gitlab/ci/fullsend-agent.yml#L203-L207)
upload run output, including transcripts, as forge-native artifacts. On GitHub,
any signed-in user can download artifacts from a public repository;
private-repository artifacts are available to repository readers, not just
owners. The GitLab template sets no `artifacts:access` restriction. GitLab's
[`artifacts:access`](https://docs.gitlab.com/ci/yaml/#artifactsaccess) defaults
to `all`, so artifacts in public pipelines are downloadable by anyone,
including anonymous users.
[#6734](https://github.com/fullsend-ai/fullsend/issues/6734) tracks alternative
artifact storage, including disabling forge-native uploads.

ADR 0021 also specifies opt-in suppression of the JSONL transcript artifact,
declared in agent configuration for protected external sources. This control
is not implemented: there is no configuration field or transcript extraction
path that implements the opt-in suppression. Operators cannot currently rely
on this decision to suppress transcripts. The decision does not cover console
output, reviews, traces or other forge writes.

The existing [private-repositories guide](../guides/infrastructure/private-repositories.md)
provides operator guidance: `AGENTS.md` disclosure rules, per-agent risk
recommendations, output checks and the harness `SecretRedactor`. Those controls
depend on agents following the instructions and on patterns the scanner can
recognize. The guide's lower-risk recommendations for repo-local output do not
establish safety when a public repository's agents consume private sources.

The [scribe agent](https://github.com/fullsend-ai/agents/blob/5b3049ba969789e0aa82e7a412f7280ce2607215/agents/scribe.md#L67-L88)
already requires a `public_safe` classification for each topic and new issue,
with a single-word `public_safe_category` for rejected content. Its
[post-script](https://github.com/fullsend-ai/agents/blob/5b3049ba969789e0aa82e7a412f7280ce2607215/scripts/post-scribe.src.sh#L290-L305)
checks that flag before repository writes. For items marked unsafe, it skips
the write and logs the category instead of the title or body. This constrains
scribe's forge writes and rejection-log text, but trusts the model's own
classification. It does not independently validate the publication safety of
derived content marked safe, or protect artifacts, transcripts or the live
console thinking stream. It is an existing release gate with limited coverage,
not a general confidentiality guarantee.

Live runtime-event rendering
([PR #3186](https://github.com/fullsend-ai/fullsend/pull/3186)) streams model
thinking and assistant text to the job console. On a public repository,
protected input quoted in that output reaches public job logs even if artifact
uploads are suppressed.

[ADR 0050](../ADRs/0050-distributed-tracing-instrumentation.md) addresses
telemetry separately: private remote export does not change the audience of
local files, console output or public reviews.

[#2513](https://github.com/fullsend-ai/fullsend/issues/2513) records the
direction to use private GitHub repositories as the standard for private Jira
refinement. It chooses documentation warnings as the immediate step and a
harness-level runtime check that warns or blocks Jira integration in a public
repository as the follow-up. This records the intended guard, rather than
establishing that runtime enforcement is available.

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

Input access and publication permission are separate controls. Protected
context can enter through deterministic prefetch code as well as an agent's
retrieval tools. Restricting tool access alone does not protect prefetched
content; suppressing output does not reduce what the agent can read.

## Options and trade-offs

### Refuse or warn on protected sources for public-repository installs

Check the repository's visibility and configured sources before allowing an
agent to read protected context. Refuse that combination or warn the operator,
starting with the runtime validation planned in #2513 for Jira integrations.
Apply the check during setup and before execution so later configuration or
visibility changes also receive validation.

Refusal can prevent known unsafe configurations without having to sanitize
all downstream output. It can also exclude useful workflows that separate
private processing from approved public output. A warning preserves operator
choice but does not prevent disclosure if the operator proceeds. Both choices
need trusted source classification, coverage of custom integrations and a
policy for unknown sensitivity. Repository visibility alone does not identify
all private inputs or public destinations.

### Keep protected processing in a private execution environment

Run the confidential stage in an access-controlled worker or control repository.
Publish only approved material to the public project.

This narrows exposure and can retain useful private evidence. It requires
cross-repository authorization, dispatch, artifact retrieval and status
correlation. The private-repository direction above does not establish that
fullsend provides this cross-repository publishing arrangement.

### Restrict the public job's inputs to approved public material

Provide an explicitly approved public projection of requirements. Keep private
tracker access out of that invocation.

This gives the public agent information it can explain and cite. Someone must
maintain the projection, prevent stale requirements and decide what may be
released. It may omit context needed for private policy decisions.

Making the entire source public is a separate disclosure decision: existing
descriptions, comments, attachments and future updates would all need a
publication policy. It is not equivalent to approving a limited projection.

### Mediate access to protected context

[ADR 0141](../ADRs/0141-deterministic-filtered-entity-context-staging.md)
already chooses deterministic, filtered staging of forge entity content for
issue and change-proposal runs. It applies
[ADR 0133](../ADRs/0133-actor-attributed-data-security-boundary.md), which
separates actor authorization, data admission and platform authority. Direct
model-initiated source-system calls remain outside ADR 0133's guarantee.
Neither decision authorizes publication of admitted private content; this
option builds on that admission boundary rather than replacing it.

For other protected sources, have trusted pipeline code select the context
before execution, or expose a restricted retrieval interface whose source
permissions and network controls prevent broader access. Neither approach
requires giving the agent unrestricted access to an entire tracker or
document store.

Context needs vary by task. A change may need one work item's approved fields,
while duplicate detection or retrospective analysis may need a broader corpus.
For sources outside the decided staging scope, compare bounded prefetch with
scoped retrieval for task effectiveness and disclosure risk. The choice depends
on the task and disclosure policy.

Source-specific controls may operate at issue, comment or field granularity.
Their visibility settings must be interpreted explicitly: a source's
"public" audience may still require authentication, and permission to read
does not itself authorize publication. Classification needs to travel with
selected context and derived work items. These controls reduce exposure but
do not make all information received by the agent safe for public output.

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

Another option is to suppress selected artifacts or disable their publication
for protected runs. Suppressing collection and disabling public upload are
different choices: the former removes retained evidence needed for debugging,
retro analysis and resumption; the latter can preserve an authorized private
copy. Neither prevents disclosure through logs, public comments or generated
code. An artifact switch is a partial mitigation, not a confidential mode.

GitLab's `artifacts:access` setting can restrict artifact downloads through the
UI and API. This is a partial, forge-specific mitigation: it does not prevent
forwarding artifacts to downstream pipelines or access through runner APIs
using job tokens. Job-token access requires separate project CI/CD visibility
controls. Artifact access restrictions also leave logs, public comments and
generated code outside their protection.

### Separate private planning and public contribution loops

Keep private planning stages and their work items within a private execution
and tracking environment. An authorized release step produces an approved
public work item. Public contribution agents work only from public material;
a separate private process can ingest public progress and update private
tracking without publishing the private linkage.

The release step could be implemented by a person, trusted pipeline code or a
constrained agent. A handoff limited to creating public issues need not also
have permission to push code or merge changes. This is a separation of trust
domains, not just a private runner targeting a public repository, and does not
require choosing new agent roles here.

This limits which stages handle private context, but the release step remains
a disclosure boundary requiring validation or approval. Keeping work items
current adds coordination cost. Public agents may miss relevant private
dependencies until approved information is relayed, so the effectiveness of
the handoff also needs evaluation.

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

## Indirect disclosure through derived work

Removing explicit private identifiers does not necessarily remove the
information they reveal. For a hypothetical example, a private requirement
could lead to a distinctive compatibility exception in an intermediate work
item, then a matching code branch or test in a public PR. An observer familiar
with that constraint could infer the private context without seeing its name.

This can happen across several planning and coding stages without a direct
quotation or a credential leak. Public-release review must therefore consider
derived requirements and observable behavior as well as text. Source labels,
prompt instructions and pattern-based redaction alone do not establish that
the released result cannot reveal protected information.

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
- Which tasks can use a bounded prefetch, and which need scoped retrieval?
  What evidence would justify expanding the selected context?
- Who approves a private-to-public work-item handoff, and how are its content,
  derived behavior and subsequent updates checked without exposing private
  rationale or linkage?
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
