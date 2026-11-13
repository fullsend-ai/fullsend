# Review Finding Evidence

How should a review finding record the evidence behind it? This covers the
conditions an attacker needs, the deployment it assumes, whether anyone
reproduced it, and whether it survived an adversarial challenge. How can
downstream policy use that evidence without treating unknown context as safe?

**Related:**
- [code-review.md](code-review.md#the-confidence-problem) -- review sub-agents
  and the confidence problem
- [graduated-approval-policy.md](graduated-approval-policy.md) -- richer
  verdicts than approve or block
- [trustworthiness-evidence.md](trustworthiness-evidence.md#a-concrete-artifact-the-trust-scorecard)
  -- the `status`-versus-`score` distinction for evidence records
- [review-autonomy-evidence.md](review-autonomy-evidence.md#change-types-where-agents-underperform)
  -- observed severity miscalibration and gaps in operational reasoning
- [security-threat-model.md](security-threat-model.md#cross-cutting-concern-agent-self-report-unreliability)
  -- agent self-reports and fail-closed defaults
- [private-context-public-workflows.md](private-context-public-workflows.md#separate-public-technical-review-from-private-policy-evaluation)
  -- keeping sensitive deployment details out of public review output

This document is a proposal for discussion, not an agreed design. It came out
of an assessment of an external advisory on AI-assisted code review
([#9427](https://github.com/fullsend-ai/fullsend/issues/9427)) and is tracked
in [#9430](https://github.com/fullsend-ai/fullsend/issues/9430).

## The problem

A review finding makes a claim about a change. Different findings make
different kinds of claims, and they need different evidence:

1. **Code defect.** The code does something wrong. Reading the diff can
   establish this.
2. **Exploitability under stated conditions.** An attacker who meets certain
   prerequisites can use the defect. This needs a reachable path from an entry
   point the attacker controls.
3. **Deployment impact.** The defect matters in the configurations that people
   actually run. This needs knowledge of how and where the code is deployed.

A reviewer that only sees the diff and the repository can usually establish
the first kind of claim. It often cannot establish the second or third. Today
the finding format gives it no way to say so.

### Deployment assumptions are prose only

The review result schema lives in `fullsend-ai/agents`. At commit
[`8be868f`](https://github.com/fullsend-ai/agents/blob/8be868f58820b4e59663d7dd58894ebbea72e3f6/schemas/review-result.schema.json#L116),
a finding has `severity`, `category`, `file`, `line`, `description`,
`remediation`, `actionable`, `id`, and `dimension`. The schema rejects any
other field. It has no place for attacker prerequisites, deployment
assumptions, affected configurations, reachability, or reproduction status.

As a result:

- A reviewer that assumes a deployment mode puts the assumption in the
  description, or leaves it out. Nothing downstream can tell the difference.
  [#3405](https://github.com/fullsend-ai/fullsend/issues/3405) and
  [#5332](https://github.com/fullsend-ai/fullsend/issues/5332) record narrower
  cases where review missed that a fix did not reach every deployment mode.
  The per-org installation references in those issues are historical, since
  per-org installation is deprecated under
  [ADR 0044](../ADRs/0044-deprecate-per-org-installation-mode.md).
- Unknown deployment context looks the same as context the reviewer checked
  and found safe.
- Severity combines "how bad would this be if true" with "how sure are we".
  The security sub-agent's
  [fail-open rule](https://github.com/fullsend-ai/agents/blob/8be868f58820b4e59663d7dd58894ebbea72e3f6/skills/pr-review/sub-agents/security.md?plain=1#L137-L144)
  marks a gate as **critical** when its controlling configuration is absent
  and the gate then permits access. The rule does not ask whether an attacker
  can reach the gate, or whether any supported deployment leaves the
  configuration unset.

The [review autonomy evidence](review-autonomy-evidence.md) corpus shows the
cost of this. On PR #4080 the review agent found a regex defect but rated it
Low, and the corpus traces the gap to missing reasoning about concrete impact.
That corpus also lists operational reasoning about deployment constraints as a
gap.

### Challenge state is implicit

The `pr-review` skill runs a challenger sub-agent after the dimension
sub-agents. The challenger tries to disprove findings, removes false
positives, merges duplicates, and calibrates severity. At commit `8be868f`, the
challenger can end in more states than the output shows:

| Challenge state | When it happens | What the result records today |
|---|---|---|
| Completed | The challenger returned valid adjudication for every input finding | Adjudicated findings; the per-finding `kept`, `downgraded`, or `merged` action is stripped before output |
| Skipped, nothing to adjudicate | No findings remain after withholding `sub-agent-failure` findings | Nothing |
| Skipped, time budget | Under 600 seconds remain before the challenger step ([checkpoint](https://github.com/fullsend-ai/agents/blob/8be868f58820b4e59663d7dd58894ebbea72e3f6/skills/pr-review/SKILL.md?plain=1#L126)) | A `low`, non-actionable `sub-agent-failure` finding |
| Failed | Timeout, tool error, malformed output, or invalid accounting ([fallback](https://github.com/fullsend-ai/agents/blob/8be868f58820b4e59663d7dd58894ebbea72e3f6/skills/pr-review/SKILL.md?plain=1#L1082)) | The unchallenged finding set plus a `low`, non-actionable `sub-agent-failure` finding |
| Partial | The findings JSON exceeds 80 000 tokens, so `low` and `info` findings are withheld ([size guard](https://github.com/fullsend-ai/agents/blob/8be868f58820b4e59663d7dd58894ebbea72e3f6/skills/pr-review/SKILL.md?plain=1#L1038)) | Nothing marks which findings were challenged |

The outcome rules in
[step 6f](https://github.com/fullsend-ai/agents/blob/8be868f58820b4e59663d7dd58894ebbea72e3f6/skills/pr-review/SKILL.md?plain=1#L1283)
allow `approve` when only `low` or `info` findings remain. A skipped or failed
challenge adds only a `low` finding, so it does not change the verdict. A
review can therefore approve a PR whose findings no one challenged, and
policy cannot tell that approval from one that followed a completed challenge.

A completed challenge, a skipped challenge, and a reproduced exploit are
different evidence states. Downstream policy, such as the protected-path and
risk gates in `post-review.sh`, cannot see the difference today.

## What a solution needs

Any option should meet these requirements:

- **Unknown is a value.** Every evidence field can say "unknown", and missing
  evidence reads as unknown. An absent field never reads as the favorable
  answer. This follows the fail-closed principle in the
  [security threat model](security-threat-model.md).
- **Separate claims stay separate.** A record can say "this is a confirmed
  code defect, exploitability unknown" without inflating or deflating
  severity to express it.
- **Policy can act on it.** Post-processing can read the evidence state with a
  deterministic rule, not by parsing prose.
- **Self-reports are labeled.** Most evidence fields are written by the review
  agent itself. Where the harness can derive a value, such as challenge state,
  it derives it rather than trusting the agent.
- **Public output stays safe.** Some deployment details are sensitive. The
  evidence format must not force them into a public review comment.

## Candidate evidence fields

The table below lists candidate fields and their allowed values. The names are
illustrative. The values matter more than the names: each field has an
explicit `unknown`, and an absent field means `unknown`.

| Field | Scope | Allowed values | Notes |
|---|---|---|---|
| `claim` | Finding | `code-defect`, `exploitable`, `deployment-impact` | The strongest claim the finding makes. A higher claim needs the fields below it. |
| `reachability` | Finding | `path-shown`, `path-asserted`, `not-reachable`, `unknown` | `path-shown` cites the entry point and each step. `path-asserted` claims a path without citing it. |
| `attacker_prerequisites` | Finding | `status`: `known`, `none`, `unknown`; `items`: list of strings | `none` means no prerequisites, such as an unauthenticated public endpoint. An empty list without `status` is invalid. |
| `deployment_assumptions` | Finding | `status`: `known`, `none`, `unknown`; `items`: list of `{assumption, source}` | `source` is one of `deployment-context`, `repository-code`, `reviewer-inference`, `unknown`. |
| `affected_configurations` | Finding | `status`: `all`, `listed`, `none-supported`, `unknown`; `items`: list of strings | `none-supported` means the defect only appears in configurations the project does not support. |
| `reproduction` | Finding | `reproduced`, `attempted-not-reproduced`, `not-attempted`, `not-possible-in-sandbox`, `unknown` | Review runs read code and diffs. Most findings are `not-attempted` today. |
| `challenge` | Finding | `kept`, `downgraded`, `merged`, `not-challenged`, `unknown` | Keeps the challenger action that step 6d strips today. `not-challenged` covers withheld findings. |
| `challenge` | Run | `status`: `completed`, `skipped-no-findings`, `skipped-time-budget`, `failed`, `partial`, `unknown`; `reason`: string; `withheld_count`: integer | `partial` means some findings were withheld. Ideally the harness derives this instead of the agent. |
| `deployment_context` | Run | `status`: `provided`, `absent`, `stale`, `unknown`; `ref`: document path and revision | Records which deployment-context document, if any, informed the review. |

Two of these fields exist in another form today. The challenger already
returns `challenger_action` per finding, and the orchestrator already knows
which state the challenger ended in. Keeping that information is cheaper than
inventing it.

## Representing unknown

### Option A: explicit `unknown` enum values

Every field has an `unknown` value, and the schema requires the field.

**Pros:** A reader sees exactly what the reviewer did not establish. Schema
validation catches a missing field.
**Cons:** Every finding carries every field, even an `info` style note. A
reviewer under pressure may fill in `unknown` everywhere, which is honest but
not useful.

### Option B: absence means unknown

Fields are optional. Post-processing treats a missing field as `unknown`.

**Pros:** Small findings stay small. Adding the fields does not break
producers that do not emit them yet.
**Cons:** Absence cannot tell "the reviewer considered this and does not know"
from "the reviewer never considered it". A consumer that forgets the default
may read absence as safe.

### Option C: status and value pairs

Each field is an object with a `status` and an optional value, like the
signals in the [trust scorecard](trustworthiness-evidence.md#a-concrete-artifact-the-trust-scorecard).

**Pros:** The record separates "measured and low" from "never measured". It
matches a pattern the project already uses for trust evidence.
**Cons:** It is the most verbose shape. It is harder for an agent to emit
correctly, so schema validation failures and retries may rise.

The candidate table above mixes Options A and C. Simple fields use enum values
with `unknown`, and list-valued fields use a `status` plus `items`.

## Where the evidence lives

### Option 1: per-finding evidence block in the review result

Add an optional `evidence` object to each finding in
`review-result.schema.json`, plus a run-level `challenge` block.

**Pros:** Evidence travels with the finding through re-review, dispositions,
and posted comments. One artifact holds the review.
**Cons:** This changes a shared contract. The challenger output, the
orchestrator merge rules, the re-review projection, and `post-review.sh` all
need to carry the new fields. Most values are still self-reported by the
agent.

### Option 2: run-level evidence only

Record only run-level state: challenge status and deployment-context status.
Leave per-finding evidence in prose.

**Pros:** This is the smallest contract change. The harness can derive most
of it, for example from the presence and text of a challenger
`sub-agent-failure` finding, so it does not depend on agent self-report. It
closes the "approved without challenge" gap directly.
**Cons:** It does not separate code defects from exploitability or deployment
impact. Severity calibration stays where it is.

### Option 3: separate evidence artifact

Write evidence to its own file, bound to the PR head SHA, next to the review
result.

**Pros:** The review result schema stays stable. The evidence file can have
its own audience, which helps when it refers to sensitive deployment details.
**Cons:** Consumers must join two artifacts and check that both refer to the
same revision. The [trust scorecard](trustworthiness-evidence.md) discussion
describes the same trade-off between one bound record and many separate ones.

### Option 4: structured prose conventions

Ask reviewers to write fixed headings in each description, such as
"Prerequisites", "Assumptions", and "Reproduction".

**Pros:** No contract change. Human readers benefit at once.
**Cons:** Policy cannot use it reliably. It does not represent unknown in a
way a gate can check.

These options can be combined. For example, Option 2 can land first because it
needs no agent self-report, and Option 1 can follow once eval cases show which
per-finding fields change outcomes.

## A deployment-context document

Reviewers cannot reason about deployment impact without knowing the
deployment. One option is a versioned document that the repository owner
maintains. It would list entry points, trust boundaries, supported
configurations, and accepted risks. Fullsend has no such document today. Any
file name or location here is hypothetical.

Trade-offs to weigh:

- **Injection and tampering.** A PR can edit the document in the same change
  it asks the reviewer to approve. A PR that adds an "accepted risk" can talk
  the reviewer out of its own finding. The reviewer would need to read the
  document from the base branch, and changes to it would need human review,
  in the same way as protected paths.
- **Staleness.** A stale document is worse than none, because the reviewer
  trusts it. The run-level `deployment_context` field records the revision
  used. A staleness rule, such as age or the last change to entry points,
  would need to mark it `stale`.
- **Sensitive details.** Some trust boundaries and accepted risks should not
  appear in a public repository or in a public review comment. The options in
  [private context in public workflows](private-context-public-workflows.md#separate-public-technical-review-from-private-policy-evaluation)
  apply. One example is a public document with coarse facts and a private
  document read by a private evaluator.
- **Effort.** Small projects may never write one. The evidence fields must
  work, and fail closed, when the document is `absent`.

## Severity and evidence

The issue that started this proposal asks whether severity should depend on a
reachable path and evidence of impact. That includes fail-open configuration
heuristics that the security sub-agent now rates as critical.

### Option S1: keep worst-case severity, add evidence alongside

Severity stays "how bad if true". The evidence fields say how much of that is
established.

**Pros:** It never lowers a guardrail because context is missing. Policy can
combine severity and evidence however it likes.
**Cons:** Reviews stay noisy. A heuristic critical with unknown reachability
still blocks the PR, and humans learn to ignore it.

### Option S2: cap severity when reachability is unknown

For example, a fail-open finding with `reachability: unknown` is at most
`high`. Only `path-shown` findings can be `critical`.

**Pros:** Severity carries more meaning. Critical findings become rarer and
more trustworthy.
**Cons:** It conflicts with fail-closed. A real critical defect that the
reviewer could not trace becomes easier to dismiss. An attacker who can hide
the path, for example through indirection, benefits.

### Option S3: two axes

Record `severity` (impact if true) and a separate `assessed_impact` derived
from the evidence fields. The verdict rules use both.

**Pros:** Neither axis hides the other. Calibration of each axis can be
measured separately against the review autonomy corpus.
**Cons:** Two numbers are harder to explain in a review comment. The derived
axis needs a rule, and the rule needs evidence from eval cases, as
[graduated approval](graduated-approval-policy.md#path-forward) already
requires for scoring.

## How downstream policy could use evidence

The table below shows one possible mapping from challenge and reproduction
state to policy behavior. It is a starting point for discussion, not a
decision. "Today" is the behavior at commit `8be868f`.

| Run challenge status | Finding reproduction | Highest remaining severity | Today | Possible behavior |
|---|---|---|---|---|
| `completed` | any | `low` or `info` | Approve | Approve |
| `skipped-no-findings` | not applicable | none | Approve | Approve |
| `skipped-time-budget` or `failed` | any | `low` or `info` (plus the failure finding) | Approve | Comment and hold for a human, or approve with a visible "unchallenged" label |
| `partial` | any | `low` or `info` | Approve | Approve, and mark withheld findings `not-challenged` in the posted review |
| any | `reproduced` | `medium` or above | Request changes | Request changes; a reproduced finding cannot be dismissed without a human |
| `completed` | `not-attempted` | `critical`, with `reachability: unknown` | Request changes | Request changes, and ask the owner to confirm the deployment assumption |
| `skipped-time-budget` or `failed` | any | `high` or `critical` | Request changes | Request changes, and state that severity was not challenged |
| any | any | any, with `deployment_context: stale` | No effect | Treat deployment-impact claims as `unknown` |

Post-processing could add an evidence gate next to the protected-path and risk
gates. Like those gates, it would downgrade an approval to a comment. It would
never upgrade a verdict. Where the harness can derive a value, such as run
challenge status, the gate should use the derived value. Agent-reported values
are [self-reports](security-threat-model.md#cross-cutting-concern-agent-self-report-unreliability):
the harness can validate their format, but not their truth.

## Follow-up work outside this repository

The review schema, the `pr-review` skill and its sub-agents, and
`post-review.sh` live in `fullsend-ai/agents`. Changes there should follow the
project's process for changing shared contracts. Likely follow-up items:

- Add evidence fields to `schemas/review-result.schema.json`, and update every
  consumer that copies or validates finding fields.
- Keep the challenger's per-finding action instead of stripping it, and record
  the run-level challenge status.
- Revisit the fail-open rule in `skills/pr-review/sub-agents/security.md`
  after choosing a severity option.
- Add an evidence gate to `post-review.sh`, after eval cases show that it
  changes outcomes.

## Open questions

- Which single field would change the most outcomes? Run-level challenge
  status is the cheapest candidate, but no eval case measures it yet.
- Should an unchallenged approval hold for a human, or approve with a visible
  label? Holding is safer. It also turns every slow review into human work.
- Can the review agent ever reproduce a finding? Review runs read code. A
  sandboxed reproduction step would cost time and widen what the review
  sandbox can do.
- Who writes and maintains the deployment-context document, and how does a
  reviewer know it is current?
- How should evidence fields carry through re-review? If a fix changes
  reachability, does the old evidence still apply to the same finding `id`?
- Would explicit `unknown` values make reviews more honest, or would agents
  fill in `unknown` everywhere to avoid being wrong?
- Does this evidence belong in the [trust scorecard](trustworthiness-evidence.md)?
  The share of approvals that followed a completed challenge could be one
  input to a track record.
