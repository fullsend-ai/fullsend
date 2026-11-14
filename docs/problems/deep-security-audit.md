# Deep Security Audit Workflow

Should Fullsend offer an opt-in workflow for deep security audits, separate
from routine PR review? If it does, what starts it, and which security
guarantees can it honestly claim?

This document is a scoping proposal for discussion. It does not describe an
agreed design. It came out of an assessment
([#9427](https://github.com/fullsend-ai/fullsend/issues/9427)) that compared
Fullsend's review pipeline with an external advisory on frontier AI code
review. That advisory suggests general-purpose agents for routine changes and
specialist harnesses for deeper audits.

**Related:**
- [code-review.md](code-review.md#security-agent-opus) — the security review
  sub-agent that runs on every PR
- [security-threat-model.md](security-threat-model.md#cross-cutting-concern-agent-self-report-unreliability)
  — why a reviewer's "no issues found" is a self-report, not evidence
- [private-context-public-workflows.md](private-context-public-workflows.md) —
  keeping restricted material out of logs, traces, artifacts and forge writes
- [ADR 0025](../ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md)
  — the four credential delivery tiers for sandboxed agents
- [#9431](https://github.com/fullsend-ai/fullsend/issues/9431) — a
  confidential reporting path for security findings (sibling item)

## The problem

Routine PR review and executable security validation have different needs.

The [security sub-agent](code-review.md#security-agent-opus) reads the diff
and the code around it. It looks for known vulnerability patterns and reports
findings in the PR. It runs on every PR, so it has to be fast and cheap. It is
not designed to run exploit code, start services, or build test
environments.

A deep audit does more. It can write a reproduction test, run it against a
disposable test environment, and confirm whether a suspected flaw is real. It
can draft a patch and keep the evidence for a maintainer to inspect. That
work takes more time, more compute and more access. It also produces output
that may need to stay private.

Running a penetration-testing pipeline on every PR is not desirable. The cost
is too high, and most changes do not need it. Without a separate workflow,
though, there is no place to run executable validation when a maintainer
wants it. There is also a risk that someone treats the routine review's
natural-language verdict as if it had been validated. The threat model
already notes that a review agent's
["no security issues found" is a self-report](security-threat-model.md#cross-cutting-concern-agent-self-report-unreliability)
that the harness cannot verify.

## Should the workflow exist at all?

Building nothing is a valid option. The trade-offs:

- **Do not build it.** Projects keep using external security tools,
  human audits and their existing disclosure process. Fullsend stays focused
  on routine review. This avoids new risk, but it leaves the gap between
  pattern-based review and executable validation to each project.
- **Build it as an opt-in harness.** Fullsend ships a separate audit agent
  that a project enables on purpose. The routine review pipeline stays
  unchanged. This keeps the cost and risk away from projects that do not want
  it. It adds a new agent role, a new harness and new publication rules to
  maintain.
- **Extend the routine review.** The security sub-agent escalates selected PRs
  into a deeper mode. This reuses existing plumbing, but it mixes two
  execution budgets and two output audiences in one workflow. It also makes it
  harder to state what a routine review did or did not check.

The rest of this document assumes the opt-in harness, to make the
requirements concrete. That assumption is itself an open question.

## Trigger options

Each trigger answers a different question about who authorizes an audit and
when it runs.

### Explicit request on a PR or issue

A maintainer applies a label or posts a slash command, such as a
hypothetical `/fs-audit`. The audit runs against that change or that issue.

- **Pros:** A person with known authority decides each audit. The cost
  matches demand. The target is clear.
- **Cons:** Coverage depends on someone remembering to ask. The audit is
  tied to a public PR or issue, so even its existence can reveal that someone
  suspects a flaw. The trigger needs at least the same
  [human role minimum](../ADRs/0151-harness-human-role-minimums.md) as other
  write-capable harnesses.

### Scheduled audit

The audit runs on a schedule against the default branch or selected paths.

- **Pros:** Coverage does not depend on someone asking. Runs are not tied to
  a public PR, which makes a private output path easier to keep.
- **Cons:** Cost runs whether or not anything changed. Scheduled runs need an
  authorization record that does not come from a triggering human. The
  threat model's attribution rules for
  [state-only scheduled discovery](security-threat-model.md#threat-2-insider-threat--compromised-credentials)
  apply.

### Release gate

The audit runs before a release, on the release candidate.

- **Pros:** It concentrates effort where a flaw would reach users. It fits
  existing release checklists.
- **Cons:** A finding late in the release cycle can delay the release or
  push maintainers to downgrade it. A flaw found here may already be in an
  earlier release, which needs coordinated disclosure rather than a normal
  fix.

### Escalation from routine review

The routine security sub-agent recommends an audit, and a human confirms it.

- **Pros:** It uses signals the review already produces, such as
  [PR risk scoring](../ADRs/0089-pr-risk-assessment-scoring.md).
- **Cons:** A prompt injection in the PR could try to start or suppress
  escalation. A human confirmation step is needed so the review agent does
  not gain authority to spend audit budget on its own.

These options can be combined. For example, a project could allow explicit
requests and a scheduled run, but never automatic escalation.

## Security requirements

### Target authorization and egress

An audit that runs exploit code needs an explicit, written scope: which
repository, which revision, and which test environments it may touch. Code
under audit is untrusted input. A reproduction test is also code that the
agent wrote while reading untrusted input.

- Run reproduction tests only against disposable test environments that the
  harness creates and destroys for that run. Do not point them at shared
  staging systems or production endpoints.
- Restrict network egress with the sandbox's L7 policies
  ([ADR 0036](../ADRs/0036-agent-execution-sandbox.md)). By default, the test
  environment should reach nothing outside the sandbox. Each permitted
  endpoint needs a stated reason.
- Record the authorized scope in the run's audit trail, so a reviewer can
  check that the audit stayed inside it.

### Credentials

Fullsend's credential model has
[four credential delivery tiers](../ADRs/0025-provider-credential-delivery-for-sandboxed-agents.md).
In credential delivery tiers 1 to 3, real credentials stay outside the
sandbox. In credential delivery tier 4, the
harness copies credential files into the sandbox with `host_files`. There,
the network policy is the security boundary, not credential isolation.

The current review harness already uses credential delivery tier 4 for some
inputs. At
[`fullsend-ai/agents@8be868f`](https://github.com/fullsend-ai/agents/blob/8be868f58820b4e59663d7dd58894ebbea72e3f6/harness/review.yaml#L55-L67),
`harness/review.yaml` can copy a GCP service-account file into the sandbox
when one is configured. An audit harness that reused this pattern would also
hold that credential while it runs code under audit.

So any statement that an audit is "sandboxed" must name the configuration.
"Sandboxed" does not mean "holds no credentials." A credible audit
description says which credential delivery tier each input uses, which
endpoints the policy allows, and what a compromised run could reach.

Options:

- **No runtime credentials for audit inputs (credential delivery tier 1
  only).** The harness prefetches the code and the test environment images,
  and the sandbox holds no credentials for repository or test-environment
  access. Model inference is classified separately: it needs at least
  credential delivery tier 2 (a provider placeholder swapped at the gateway
  proxy) or credential delivery tier 3, unless the model runs entirely
  outside the sandbox. This gives the strongest claim for audit inputs but
  may rule out some model back ends.
- **Allow credential delivery tiers 2 and 3, forbid credential delivery
  tier 4.** Credentials never enter the sandbox filesystem. Some inference
  set-ups, such as file-based cloud auth, would not be available for audits.
- **Allow credential delivery tier 4 with documentation.** The audit can use
  any back end, but every audit report must say that real credentials were
  present and what the policy allowed.

### Validation budget

A deep audit can run out of time or money partway through. If that happens,
the result must not look like a complete validation.

- Reserve part of the budget for validation before exploration starts.
  The agent should not spend the whole budget searching and leave nothing to
  confirm what it found.
- Give each finding a validation state, such as `reproduced`,
  `not reproduced`, or `not attempted`. The harness, not the agent, sets the
  overall state from the evidence it can check. This follows the threat
  model's [verify, don't trust](security-threat-model.md#cross-cutting-security-principles)
  principle.
- When validation is incomplete, the report says so. "No findings" from an
  audit that ran out of budget is reported as "incomplete," not as "clean."

### Disclosure

Routine review publishes its findings as PR comments. That is wrong for a
newly found flaw in a released version, which may need coordinated
disclosure. Fullsend's own
[`SECURITY.md`](https://github.com/fullsend-ai/fullsend/blob/main/SECURITY.md)
asks reporters to use private advisories and not to discuss vulnerabilities
in public.

Audit output therefore depends on the confidential reporting path in
[#9431](https://github.com/fullsend-ai/fullsend/issues/9431). Until that path
exists, audit findings should not go through ordinary review publication.
Confidentiality also covers transcripts, traces, logs and run artifacts, not
only the final comment. See
[private-context-public-workflows.md](private-context-public-workflows.md#boundaries-to-account-for).
By default, the
[run artifact setting](../ADRs/0161-repo-config-limits-run-artifact-content.md)
uploads the whole run output. On a public repository, that output is visible
to anyone who can download the artifact.

### Patching

An audit can draft a patch. The external advisory keeps severity rating,
validation, disclosure and patch review with human experts. It does not show
that autonomous patching or merging of security fixes is safe.

Options:

- **Report only.** The audit produces findings and evidence, with no patch.
- **Draft patch for private review.** The audit attaches a candidate patch to
  the confidential report. A maintainer decides whether and how to use it.
- **Open a PR.** This is not appropriate for an undisclosed flaw. A public PR
  can disclose the flaw before a fix is released.

In every option, a human owns the severity rating, the disclosure decision
and the patch review. The audit agent does not merge.

## What the workflow can and cannot claim

| Claim | Can it be made? | Condition |
|---|---|---|
| A specific finding was reproduced | Yes | The harness kept the reproduction test and its output, and a person can rerun it |
| The audit stayed inside its authorized scope | Partly | Egress policy and audit trail support it; audit log integrity is [not yet enforced](security-threat-model.md#cross-cutting-concern-audit-log-integrity) |
| The audit ran without credentials | Only for a stated configuration | Every input uses credential delivery tiers 1 to 3; false for credential delivery tier 4 `host_files` set-ups |
| The change has no vulnerabilities | No | An audit can find flaws; it cannot prove that none exist |
| The audit completed its validation | Only when the harness confirms it | A budget-exhausted or timed-out run is reported as incomplete |
| The findings were kept confidential | Not yet | Depends on [#9431](https://github.com/fullsend-ai/fullsend/issues/9431) and on artifact, log and trace settings |
| The severity rating is authoritative | No | A human expert owns severity |
| A drafted patch is safe to merge | No | A human expert reviews every patch; the agent does not merge |

## Relationship to other problem areas

- [Code review](code-review.md): routine review stays fast and runs on every
  PR. A deep audit is a separate, opt-in path, not a stricter review mode.
- [Security threat model](security-threat-model.md): code under audit is
  untrusted input. An audit agent that runs that code needs the same
  defenses against prompt injection and credential misuse as any other agent,
  plus egress limits for the code it executes.
- [Private context in public workflows](private-context-public-workflows.md):
  audit findings are a case of restricted output that must stay out of public
  logs, artifacts and forge writes.

## Open questions

- Should the workflow exist, or should Fullsend leave deep audits to external
  tools and human auditors?
- Which triggers should be allowed, and which human role can authorize each
  one?
- Which credential delivery tiers are acceptable for an audit harness? Is
  credential delivery tier 4 ever acceptable when the sandbox runs untrusted reproduction code?
- How large should the reserved validation budget be, and who sets it?
- Which validation states should the result schema expose, and how does the
  harness check them without trusting the agent's own report?
- How are disposable test environments built, and how is their teardown
  verified?
- Until [#9431](https://github.com/fullsend-ai/fullsend/issues/9431) lands,
  should the workflow be blocked on public repositories?
- How does an audit report link to a forge's private advisory feature on
  GitHub, GitLab and Forgejo?
- Should a scheduled audit run with a different identity from the routine
  review agent?
- What evidence would show that an audit finds real flaws often enough to
  justify its cost?
