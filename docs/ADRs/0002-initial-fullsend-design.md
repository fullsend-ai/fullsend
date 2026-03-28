---
title: "Initial Fullsend Design"
status: Proposed
relates_to:
  - agent-architecture
  - autonomy-spectrum
  - code-review
  - governance
  - intent-representation
  - security-threat-model
topics:
  - fullsend
  - github
  - workflow
  - agents
---

# Initial Fullsend Design

Date: 2026-03-23

## Status

Proposed

## Context

This ADR is the **initial Fullsend design** for a **GitHub-centric issue-to-merge agent workflow**: autonomous agents handling routine work from **issue creation** through **merged PR**. It targets **GitHub-hosted organizations** exploring the [Fullsend vision](../vision.md). Downstream consumers (for example teams in the Konflux ecosystem) may implement it concretely, but the workflow is defined in terms of **GitHub primitives** (issues, PRs, labels, checks, branch protection, CODEOWNERS) so it stays portable.

Contributors need a **clear, implementable picture** of how work flows when **multiple independent agents** can be triggered:

- **Automatically** (e.g. when specific labels are applied), and
- **On demand** via **`/` commands** in issue or PR comments, so humans can **restart or resume** the pipeline from any stage without a single central orchestrator process.

This matches Fullsend’s stated design direction: **trust derives from repository permissions**, **CODEOWNERS and similar rules remain human-owned guardrails**, and **the repository plus branch protection and checks act as the coordination layer** rather than a privileged coordinator agent (see [README](../../README.md) and [agent architecture](../problems/agent-architecture.md)).

This ADR records a **high-level workflow design** and decomposes it into **building blocks** that teams can implement and harden separately. It assumes **adversarial thinking** and **sandboxed execution** for anything that runs untrusted code or fetches third-party content (aligned with [security threat model](../problems/security-threat-model.md)).

This document does **not** mandate a single implementation (GitHub App vs Actions vs external runner); it describes **interfaces** (labels, comments, checks) and **responsibilities** so implementations can vary.

## Decision

We propose adopting the following **reference workflow** as the mental model for issue → PR → merge with multiple agents, **label-driven state transitions**, **slash-command overrides**, and **explicit sandbox boundaries** underneath each agent class.

### Actors and triggers

| Trigger | Purpose |
|--------|---------|
| **Label applied** (automation or human) | Idempotent signal to enqueue or resume work for a named agent role |
| **`/` command in a comment** | Human-in-the-loop control: re-run a stage, override a stuck state, or inject intent |
| **GitHub webhooks** | Delivery mechanism for label/comment/PR/check events to whatever executes agents |
| **Branch protection + required checks** | Non-bypassable quality bar; agents must converge checks before review handoff |
| **CODEOWNERS + branch rules** | Human approval on guarded paths remains **outside** this ADR’s automation scope |

**Slash commands (illustrative, configurable per repo):**

- `/triage` — run or re-run triage on the issue
- `/code` — hand issue to PR agent (expects readiness or forces with human ack — policy per repo)
- `/review` — enqueue review swarm for current PR head
- `/reset-triage` — clear triage outcome labels (including **`not-ready`** and **`not-reproducible`**) and re-open triage (optional)

Commands should be validated (e.g. authorized commenters, org members only) in implementation.

### Labels as state machine (reference set)

Repos may extend this set; names below are **semantic**, not prescriptive strings.

| Label | Meaning |
|-------|---------|
| `duplicate` | Triage: same work is already tracked elsewhere; this issue should be **closed** and discussion continues on the canonical issue |
| `not-ready` | Triage: insufficient information to proceed (detail missing before a reliable repro attempt). Applying this label **must** be accompanied by a **triage-output comment on the issue** that explains **why** (what is missing, unclear, or insufficient)—humans and the next triage pass need that context; **do not** set **`not-ready`** without posting that comment |
| `not-reproducible` | Triage: enough information was available to attempt reproduction, but the reported bug **could not be reproduced** in the triage sandbox; **human intervention** is required. **No further automated processing** (no PR agent, no implementation/review pipeline) while this label is present. Applying this label **must** be accompanied by a **triage-output comment on the issue** that records **what was tried** (commands, versions, environment assumptions) and **how it failed** to match the report (e.g. expected symptom absent, wrong error, tests green, timeouts)—**do not** set **`not-reproducible`** without that comment |
| `ready-for-coding` | Triage passed; implementation may proceed |
| `ready-for-review` | PR agent finished implementation + checks; awaiting multi-agent review |
| `ready-for-merge` | Review coordinator: **all** reviewers **unanimously** approved merge **for the PR head SHA at the end of that review round** (subject to branch protection / humans). The label **must not** remain set across a **new** review round or a **new** PR head without a **fresh** unanimous round—see Phase C (**When a review run starts**). Downstream automation and humans should treat **`ready-for-merge`** as **invalid** unless it reflects the **current** head after the **latest** completed review round |
| `requires-manual-review` | Review coordinator: reviewers **did not** unanimously agree to merge (split vote, conflicting conclusions, or conflicting **security severities**); **humans** must decide next steps |

**Mutual exclusion:** Implementation should enforce consistent label sets (e.g. removing `ready-for-coding` when setting `ready-for-review`). An issue marked **`duplicate`** must **not** carry **`ready-for-coding`**, **`ready-for-review`**, **`ready-for-merge`**, or **`requires-manual-review`**. An issue marked **`not-reproducible`** must **not** carry **`ready-for-coding`**, **`ready-for-review`**, **`ready-for-merge`**, or **`requires-manual-review`** — automation stops until humans resolve the situation or triage runs again (see Phase A). **`not-reproducible`** and **`not-ready`** should **not** be applied together (triage picks one outcome per pass). **`ready-for-merge`** and **`requires-manual-review`** must **not** be applied together on the same issue/PR.

**Later-phase labels (for reset semantics):** Labels **`ready-for-review`**, **`ready-for-merge`**, and **`requires-manual-review`** are treated as **downstream of triage**; triage and PR-agent **starts** strip them when resetting pipeline state (see below).

### Agent roles (logical; may map to one or many processes)

1. **Triage agent** — Duplicate detection, issue intake, reproducibility, test artifact proposal
2. **PR agent** — Branch, implement, test iteratively, open/update PR, fix checks
3. **Review agents** — N parallel reviewers + **one coordinator** (randomly designated per review round) to coalesce feedback

Each role is a **building block**: separate prompts, policies, sandboxes, and CI jobs can evolve independently.

---

## Workflow narrative

### Phase A — Issue triage

**Inputs (strict):** The triage agent considers **only**:

- Issue **`title`** and **`body`** (GitHub REST/GraphQL fields of the same names),
- **GitHub-native file/image attachments** on the issue (when the **Issues** APIs or UI expose them to the agent),
- **Repo context** needed for reproduction and conventions (default branch, contributing guide, etc.).

It **does not** read the **issue comment thread** for intake decisions—no scanning prior human discussion, `@mentions`, or buried requirements in comments. That keeps triage **bounded**, **cheaper**, and **less exposed** to prompt-injection and noise in long threads.

**Contributor rule:** If something is missing or wrong for triage, **edit the issue `body`** (and **`title`** if needed) and add or replace **attachments** using **only GitHub’s native issue attachments** (uploads GitHub stores and exposes on the issue—see **Test case artifact** below). Do **not** expect the triage agent to discover updates hidden in comments. **Edits to `title` or `body`** trigger triage again automatically; you can also use **`/triage`**.

**Triage agent responsibilities:**

1. **Duplicate detection** — Before investing in reproduction or coding, check whether the same (or substantially the same) problem is **already tracked** in another issue. Use **repo/org search**, **issue list filters**, **embedding or keyword similarity**, or other **policy-defined** signals (implementation choice). Base the match **only** on **`title`**, **`body`**, and **attachments** of the current issue compared to **indexed metadata** of candidate issues—not on reading arbitrary comment threads for candidates unless the search backend already summarized them. If triage concludes the new issue is a **duplicate** with **high enough confidence** (threshold per repo): apply the **`duplicate`** label; post a **triage-output comment** that **links the canonical issue** (GitHub URL or `#NNN` reference) and briefly states why it is considered the same; **close** the **new** issue (`state: closed` via API). Do **not** apply **`ready-for-coding`**, **`not-ready`**, or **`not-reproducible`** for an implementation track. If uncertain, **do not** mark duplicate—continue with normal triage.
2. **Information sufficiency** — From the **`title`**, **`body`**, and **attachments** only, decide whether the issue contains enough detail to act (expected behavior, actual behavior, version/context, minimal steps). If not, post a **structured triage-output comment on the issue** first (or in the **same atomic update** as the label) that lists **specific** missing items and explains **why** the issue cannot proceed yet—then apply **`not-ready`**. **Never** apply **`not-ready`** without that explanatory comment. Do not start implementation. (That comment is **machine handoff and feedback**, not something the triage agent re-reads as user input on the next run—the next run still trusts **`title` + `body` + attachments**.)
3. **Reproducibility** — When feasible, attempt to **reproduce** the problem inside the **triage sandbox** (see Sandboxing), using **only** what appears in the **`title`**, **`body`**, and attachments. **Skip** if the issue was already handled as a **duplicate** (closed). If information is **insufficient** to attempt reproduction meaningfully, apply **`not-ready`** only after posting a **triage-output comment on the issue** explaining what is missing or ambiguous for repro (do not apply **`not-reproducible`**). If information is **sufficient** but the bug **cannot be reproduced** after a good-faith attempt, post a **structured triage-output comment on the issue** first (or in the **same atomic update** as the label) that documents **what was tried** (steps, commands, branch/commit or version context, sandbox constraints) and **how it failed** to reproduce the issue as described (e.g. observed behavior vs reported behavior, logs or exit codes, “expected failure did not occur”). Then apply **`not-reproducible`**. **Never** apply **`not-reproducible`** without that comment. Flag for **human intervention**; **do not** enqueue further automated work (**no** PR agent, **no** implementation/review pipeline) until triage runs again. Do **not** use **`not-reproducible`** when the right outcome is simply “need more detail” (**`not-ready`**).
4. **Test case artifact** — When possible, produce a **test case** aligned with the repo’s **existing test framework** (same runner, conventions, paths). **Attachments** mean **only** what GitHub supports as **issue attachments** (native uploads on the issue via GitHub UI or **Issues API** attachment mechanisms the platform provides). Do **not** rely on ad hoc binary hosting, external blob stores, or non-GitHub “attachment” URLs as a substitute—if it cannot be expressed as **body** text, **fenced code in a triage-output comment**, or a **GitHub-native attachment**, the triage-output comment must still specify exact file paths and patch-shaped instructions for the PR agent.

**Outcomes:**

- **Duplicate path:** Apply **`duplicate`**, comment with **link to canonical issue**, **close** this issue; no implementation workflow.
- **Ready path:** Apply **`ready-for-coding`**, summarize reproduction result in the triage-output comment and point to the proposed test artifact. (**`not-ready`** and **`not-reproducible`** were already cleared at triage **start** if they were set.)
- **Not ready path:** Apply **`not-ready`** only with a **triage-output comment on the issue** that explains **why** (per **Information sufficiency** and **Reproducibility** above); do not apply `ready-for-coding` or `not-reproducible`.
- **Not reproducible path:** Apply **`not-reproducible`** only with a **triage-output comment on the issue** that records **what was tried** and **how it failed** (per **Reproducibility** above); do not apply `ready-for-coding`. **Stop** automated processing for this issue until a **new triage run** (automation or human-triggered) **starts** — at triage **start**, **`not-reproducible`** is **removed** together with **`not-ready`**, **`ready-for-coding`**, and later-phase labels, and triage executes the **full** Phase A sequence again from a clean slate.

**Triggers (triage agent):**

1. **Issue created** (`issues` **`opened`**).
2. **Issue `title` or `body` edited** (`issues` **`edited`** when `title` or `body` changed).
3. **`/triage`** in a comment.

**When a triage run starts** (before duplicate detection and the rest): **remove** **`not-ready`**, **`not-reproducible`**, **`ready-for-coding`**, and **all later-phase labels** (**`ready-for-review`**, **`ready-for-merge`**, **`requires-manual-review`**). That **resets the pipeline** on the issue so triage outcomes are authoritative for a new pass and a prior **`not-reproducible`** handoff does not block a full re-triage. (**`duplicate`** is a triage outcome: do **not** remove it here unless **repo policy** says a new triage run clears it—default is to **leave** **`duplicate`** unless humans removed it.)

### Phase B — Implementation (PR agent)

**Entry:** The **`ready-for-coding`** label was **applied** to the issue (trigger event) **or** **`/code`** was invoked—**before** the run strips labels. Issue is **open** and **not** **`duplicate`**, **not** **`not-reproducible`**, and in no other conflicting state.

**Triggers (PR agent):**

1. **`ready-for-coding`** label **added** to the issue.
2. **`/code`** in a comment.

**When a PR-agent run starts:** **remove** **`ready-for-coding`** and **all later-phase labels** (**`ready-for-review`**, **`ready-for-merge`**, **`requires-manual-review`**). Implementation proceeds from the issue **`title` / `body` / attachments** and triage handoff comments—not from stale review/merge labels.

**PR agent responsibilities:**

1. **Read issue handoff** — Issue **`title`**, **`body`**, and **attachments** (the canonical human-maintained spec), plus **labels**, plus **triage agent output** (the structured triage-output comment(s) with reproduction summary and proposed test artifact). Do **not** depend on informal human comments for requirements unless repo policy explicitly expands scope.
2. **Branch and implement** — Produce a fix following repo conventions; respect CODEOWNERS implications (agent may still open PR but cannot satisfy human review on guarded paths without humans).
3. **Testing loop (iterative)** — Run the **existing test suite** in the **PR sandbox**. Incorporate **triage-provided tests** if present; if none, **author tests** consistent with the framework. Repeat **implement → test** until **local/CI-equivalent tests pass** in the sandbox used for iteration.
4. **Open or update PR** — Link issue; describe changes.
5. **GitHub checks loop** — After push, **required checks** run on GitHub. On failure, PR agent **fetches logs**, fixes, pushes; repeat until **all required checks pass** (or until policy caps retries — implementation detail).
6. **Handoff to review** — When checks are green: add **`ready-for-review`**. (**`ready-for-coding`** was already removed when this run **started**.) PR agent then **waits** until review outcome.

### Phase C — Multi-agent review

**Entry:** The **`ready-for-review`** label was **applied** (or **`/review`** or **PR synchronize** per re-review policy)—**before** the run strips **`ready-for-review`**.

**Triggers (review swarm + coordinator):**

1. **`ready-for-review`** label **added** to the issue (or linked PR—policy per repo).
2. **`/review`** in a comment.
3. **PR synchronize** (push to the PR branch)—**re-review** per policy below.

**When a review run starts** (initial review, **`/review`**, or **push-triggered re-review**): **remove** **`ready-for-review`** **and** **`ready-for-merge`**. A new round **supersedes** any prior merge verdict until the coordinator finishes this round—otherwise **`ready-for-merge`** could describe an **old** head after the author **pushed** new commits, which is **unsafe** for bots and humans. Reviewers evaluate the **current** PR head; the coordinator applies outcomes using the algorithm below. (**`requires-manual-review`** is **not** removed here by default—humans may still need to resolve an earlier split verdict unless **repo policy** clears it when enqueueing a new round.)

**Review swarm:**

- **Configurable count N** of **independent review agents** run in parallel (separate invocations, separate context windows where applicable).
- **Coordinator selection:** One reviewer is **randomly chosen** as **coordinator** for this round (deterministic seed optional for auditability).
- **Coordinator duties:** Collect reviewer outputs; produce **one consolidated GitHub comment** (findings, severity, requested changes); apply the **coordinator algorithm** below; update labels per rules below.

**Coordinator algorithm (merge vs manual vs rework):**

- Each reviewer outputs a structured verdict (e.g. **approve merge**, **request changes**, **comment-only**), including **security severity** when relevant.
- **Unanimous approve-merge:** **All** counted reviewers **approve merge** with **no** outstanding **request changes** and **no** **conflicting security severities** on whether the PR is safe to merge. Apply **`ready-for-merge`**, remove **`ready-for-review`** and **`requires-manual-review`** if present.
- **Unanimous request-changes:** **All** agree the PR needs revision (no one approves merge yet). Apply **`ready-for-coding`**, remove **`ready-for-review`** — same as **Changes requested** below (not **`requires-manual-review`**).
- **Not unanimous** (split votes, some approve and some reject, or **conflicting security severities** that prevent a single merge judgment): Apply **`requires-manual-review`**, remove **`ready-for-review`**, do **not** apply **`ready-for-merge`**. The coordinator comment must summarize **who said what** so humans can decide.

**Outcomes:**

- **Changes requested** (coordinator maps to unanimous rework): Coordinator comment documents issues; remove **`ready-for-review`**, add **`ready-for-coding`** so the PR agent can resume; clear **`requires-manual-review`** if set.
- **Unanimous merge:** Add **`ready-for-merge`** (and remove **`ready-for-review`**). This label applies **only** to the **PR head SHA** the reviewers just evaluated in **this** round. Actual merge may still require **human approval**, merge queue, or bot merge permission per [governance](../problems/governance.md) — this ADR only defines **agent-visible** labels.
- **Requires manual review:** Add **`requires-manual-review`** (and remove **`ready-for-review`**); do **not** add **`ready-for-merge`**.

**Re-review policy:**

- **On every push** to the PR head while the change is **in the review stage** (implementation has handed off to review; automation tracks round state—the **`ready-for-review`** label may already have been **cleared** when the current round **started**): **automatically** enqueue a **new** multi-agent review round (same N, new coordinator selection). **When that round starts**, **`ready-for-merge`** is cleared together with **`ready-for-review`** (see **When a review run starts**), so merge approval is **never** left pointing at a **superseded** head SHA. This keeps review aligned with the latest diff without waiting for humans.
- **On demand:** **`/review`** in a comment **also** enqueues a review round (re-run or extra pass). **Both** apply: pushes trigger re-review **and** maintainers can force a round via **`/review`** even without a new push. A **`/review`**-triggered round **also** clears **`ready-for-merge`** at **start**, so an extra review pass invalidates the previous unanimous verdict until the new round completes.

**Merge:** After **`ready-for-merge`**, the PR is merged per [governance](../problems/governance.md), branch protection, and org policy. This ADR does **not** define further **automated** steps on the repo after merge; see [Future considerations](#future-considerations) for an optional **merge-time audit trail**.

---

## Building blocks (independent work streams)

These can be owned and shipped separately:

### 1. Webhook + dispatch service

Normalize GitHub events, idempotency keys, dedupe label flapping. ([Architecture](../architecture.md#1-webhook--dispatch-service))

### 2. Slash-command parser + ACL

Map comments to intents; audit log. ([Architecture](../architecture.md#2-slash-command-parser--acl))

### 3. Label state machine guard

Validates legal transitions; prevents contradictory labels (including **`duplicate`** and **`not-reproducible`** vs implementation labels). Coordinates **atomic** **on-start** label strips for triage, PR, and review runs so resets are race-safe. ([Architecture](../architecture.md#3-label-state-machine-guard))

### 4. Triage agent runtime

Prompt, tools, repo context packaging, output schema (triage-output **comment** + labels + optional **close issue**). Context fetchers **must** supply issue **`title`**, **`body`**, and **attachments** only for intake—not the full comment thread. ([Architecture](../architecture.md#4-triage-agent-runtime))

### 5. Duplicate / similarity search

Issue index, search API integration, or LLM-assisted candidate retrieval with **confidence thresholds** and audit logging; feeds triage **before** reproduction. ([Architecture](../architecture.md#5-duplicate--similarity-search))

### 6. Repro sandbox template

Hermetic-ish environment for repro commands (language-specific images). ([Architecture](../architecture.md#6-repro-sandbox-template))

### 7. Test artifact formatter

Emits framework-native test snippets and attachment bundles. ([Architecture](../architecture.md#7-test-artifact-formatter))

### 8. PR agent runtime

Git operations (token-scoped), patch application, local test runner integration. ([Architecture](../architecture.md#8-pr-agent-runtime))

### 9. PR sandbox / CI mirror

Same toolchain as contributors; secrets policy. ([Architecture](../architecture.md#9-pr-sandbox--ci-mirror))

### 10. Check failure triage

Log fetch, classification, fix loop policies (max iterations, escalation). ([Architecture](../architecture.md#10-check-failure-triage))

### 11. Review agent runtime

Static/dynamic analysis hooks, policy packs, output schema. ([Architecture](../architecture.md#11-review-agent-runtime))

### 12. Coordinator merge algorithm

Random coordinator selection; **unanimous** approve-merge → **`ready-for-merge`** (scoped to **current** PR head for that round); **review run start** clears **`ready-for-merge`** with **`ready-for-review`** so pushes do not leave stale merge approval; **unanimous** rework → **`ready-for-coding`**; **split or conflicting severities** → **`requires-manual-review`**; consolidated comment schema. ([Architecture](../architecture.md#12-coordinator-merge-algorithm))

### 13. Observability

Trace IDs spanning issue → PR → checks → review for incident response and correlation across automation runs. ([Architecture](../architecture.md#13-observability))

---

## Sandboxing (underneath the agents)

**Principle:** Anything that **executes repository code**, **runs user-supplied repro steps**, or **fetches network content** runs in a **dedicated sandbox** with **least privilege**, **no write access to production systems**, and **ephemeral credentials**.

**Layers (conceptual):**

| Sandbox | Used by | Contains |
|--------|---------|----------|
| **Triage / repro** | Triage agent | Issue-derived commands, dependency install, repro script; **read-only** clone or shallow fetch; network egress policy per threat model |
| **PR / build-test** | PR agent | Full dev dependencies, test execution, compiler; **push** only to bot-owned branches; PR secrets (tokens) scoped to repo |
| **Review** | Review agents | Optional dynamic checks; **no** merge rights; optional read-only or isolated run for fuzzing |
| **GitHub Actions / hosted runner** | Checks | Org-controlled workflows; OIDC where possible |

**Data flow:** Agents receive **minimal** tokens (fine-scoped GitHub App). **Secrets** never appear in issue **`body`**, PR **`body`**, or **comments**. **Prompt injection** from those surfaces is assumed — see [security threat model](../problems/security-threat-model.md): triage and review prompts should treat **`body`** text and **comments** as **untrusted**, with tool allowlists and output validation.

---

## Diagrams

The first figure is the **happy-path lifecycle** with **explicit triggers** (labels, slash commands, merge). The second is a **layered platform view**: what lives in GitHub vs dispatch vs agents vs sandboxes for **Phases A–C** only.

### End-to-end flow (labels, slash commands, merge)

```mermaid
flowchart TB
  subgraph TRG["Triggers"]
    direction LR
    LB[Label events]
    S1["/triage"]
    S2["/code"]
  end

  subgraph PH1["Phase A — Issue triage"]
    T[Triage agent]
    TR{Triage outcome}
    NR[not-ready]
    NREP[not-reproducible]
    RFC1[ready-for-coding]
  end

  subgraph PH2["Phase B — PR implementation"]
    P[PR agent]
    TC{Tests + checks green?}
    RFR[ready-for-review]
  end

  subgraph PH3["Phase C — Multi-agent review"]
    S3["Slash: /review"]
    RW["Review swarm + coordinator"]
    CHG{Changes needed?}
    RFC2[ready-for-coding]
    RFM[ready-for-merge]
  end

  LB --> T
  S1 --> T
  T --> TR
  TR -->|missing info| NR
  TR -->|not reproducible| NREP
  TR -->|ready| RFC1

  RFC1 --> P
  LB --> P
  S2 --> P
  P --> TC
  TC -->|iterate| P
  TC -->|yes| RFR

  S3 -->|dispatch| RW
  RFR --> RW
  LB --> RW
  RW --> CHG
  CHG -->|yes| RFC2
  RFC2 --> P
  CHG -->|no| RFM

  RFM --> DONE["Merge per governance; end of automated pipeline in this ADR"]
```

**How to read it:** **`/triage`** and **`/code`** sit in the top **Triggers** strip with **labels**. **`/review`** sits inside Phase C. **`ready-for-merge`** leads to **human- or policy-governed merge**; this ADR does not show further automation after merge (see [Future considerations](#future-considerations)).

### Sandbox stack (layered platform)

```mermaid
flowchart TB
  subgraph L1["GitHub — surfaces"]
    direction LR
    IPL["Issues & PRs · labels · checks"]
    CMT["Comments · slash commands"]
    MRG[Merge & PR closed events]
  end

  subgraph L2["Control plane"]
    direction TB
    WH[Webhook ingest & normalize]
    SP[Slash parser + ACL]
    SM[Label state guard]
    WH --> SP
    SP --> SM
  end

  subgraph L3["Agent runtimes"]
    direction LR
    TA[Triage agent]
    PA[PR agent]
    RA[Review + coordinator]
  end

  subgraph L4["Sandboxes (least privilege)"]
    direction LR
    SB1["Triage / repro"]
    SB2["PR build-test"]
    SB3[Review]
  end

  IPL --> WH
  CMT --> WH
  MRG --> WH
  SM --> TA
  SM --> PA
  SM --> RA
  SP -->|"/review"| RA
  TA --> SB1
  PA --> SB2
  RA --> SB3
  PA <--> IPL
```

**How to read it:** **Comments** (including **`/triage`**, **`/code`**, **`/review`**) feed **webhook ingest** together with **issues/PRs/labels/checks** and **merge / PR-closed** events (merge events may still be logged for policy or observability). **`/review`** also shows as an **explicit edge** from the **slash parser** to the **review** runtime (alongside **label-guard** dispatch). The **PR agent** ↔ **checks** loop is the **bidirectional** edge on the issue/PR surface.

---

## Consequences

### Positive

- **Composable delivery:** Teams can implement dispatch, triage, PR, and review independently.
- **Human operability:** Labels + slash commands give **observable** state and **recovery** without a single orchestrator brain.
- **Alignment with Fullsend themes:** Sandboxes and check loops mirror **autonomy is earned** from [vision](../vision.md); coordination stays in repo mechanics rather than a coordinator agent.

### Negative / risks

- **Label races:** Concurrent automation can fight; requires idempotency and state guard (building block).
- **Cost and latency:** Multi-agent review × N is expensive; needs quotas and backoff.
- **False readiness:** Over-trusting reproduction in a sandbox unlike user environments — mitigated by documenting environment assumptions in triage comments.
- **Governance:** `ready-for-merge` must not bypass **branch protection** or **CODEOWNERS** unless org policy explicitly allows bot merge.

## Open questions

- Exact **label naming** and **per-repo customization** (config file in repo vs org defaults).
- **Merge authority:** When may a bot merge after `ready-for-merge`? (Overlaps [governance](../problems/governance.md) and [autonomy spectrum](../problems/autonomy-spectrum.md).)
- **Interaction with** [intent representation](../problems/intent-representation.md) and [autonomy spectrum](../problems/autonomy-spectrum.md) for repos that should not auto-advance stages.
- **Comment graph:** How to reliably associate review sub-comments with a **round** when threading or edits differ by GitHub surface (issue vs PR conversation).
- **Issue `body`/`title` history:** GitHub may not expose full revision history for **`body`** and **`title`** via API; tracing *what* changed between triage passes may be limited to **`updated_at`** (and webhook payloads) unless the org adds external logging.
- **Duplicate detection:** Search scope (repo vs org), **confidence threshold**, human override when the bot marks **`duplicate`** incorrectly, and whether to require **linked** duplicate-of metadata (GitHub **linked issues**) in addition to labels.

## Future considerations

This ADR’s **normative** workflow ends when the PR is ready to merge and merge is carried out under **governance** and branch protection. The following is **not** required for that pipeline; it is a plausible **follow-on** for orgs that want a **single, readable audit trail** after merge.

### Merge-time audit trail

**Motivation:** Long issue/PR threads, retried checks, and many agent comments make it hard to reconstruct **what happened at each stage**. A **merge-time audit trail** (for example one **canonical comment** on the issue, updated after merge) can give **post-merge review**, incident analysis, and onboarding a **navigation layer** grounded in **observable platform facts** (timestamps, actors, labels, check conclusions, SHAs, comment links) rather than a free-form recap alone. It supports **transparency over trust** (see [vision](../vision.md)) but **does not gate merge**.

**Possible shape when adopted:**

1. **Trigger** — Merge event / `pull_request` closed as merged (primary); optional slash command (e.g. **`/flow-trace`**) for regeneration or repair (restricted commenters — e.g. maintainers).

2. **Evidence collection** — From GitHub (and internal dispatch logs if available): issue and PR timelines, **label transitions** (including **`duplicate`**, **`not-reproducible`**, **`requires-manual-review`**, **`not-ready`** ↔ **`ready-for-coding`**, **`ready-for-review` ↔ `ready-for-coding`**, and triage re-runs), **issue `body` / `title` edit** events when available, comment timelines with **stable permalinks**, required check runs and final conclusions, merge commit SHA, base/head SHAs at merge (including **head SHA per review round** when useful), and **correlation identifiers** (e.g. trace IDs from the observability building block) when the implementation emits them. Attribute comments to **agent vs human** when the platform provides that signal (e.g. GitHub App actor).

3. **Narrative structure** — Sections that mirror **triage, implementation, and review** (the stages this ADR defines), each listing **what ran**, **outcome**, and **pointers** (e.g. permalink to the final check suite). Explicit **handoff points** (labels applied/removed, PR opened, first and last **ready-for-review** / **ready-for-merge** transitions).

4. **Triage history** — Document **each** period where the issue was **not workable** or **not reproducible**: every **`not-ready`** and **`not-reproducible`** (with **permalink** to triage-output comments that record **why** or **what was tried**), triage re-runs after **`body` or `title` edit**, and transitions toward **`ready-for-coding`**. Include **`duplicate`** outcomes (canonical link, issue closed) when no PR exists. For ADR violations (label without required comment), still record the pass and link the best artifact.

5. **Implementation–review cycles** — Document **each** PR loop between implementation and review (checks green → **`ready-for-review`** → coordinator rework → **`ready-for-coding`** → commits → **`ready-for-review`** again, etc.). Per cycle: review round identity, head SHA when available, label bounds, and **summary + permalink** for every material agent comment (coordinator, reviewers, PR agent). If a round has no standalone comment, link the best primary artifact (check run, bot summary).

6. **Publish** — One **top-level comment** on the **issue** (and optionally a shorter pointer on the merged PR — policy per repo). Stable heading or marker (e.g. `## Fullsend audit trail`) so automation or **`/flow-trace`** can **update or replace** the same logical artifact idempotently.

7. **Safety** — **Redact** secrets; **link** to check runs instead of pasting large logs. Treat issue/PR bodies as **untrusted** when summarizing; **anchor** the trail in **API-derived events** so issue **`body`** edits cannot rewrite history. Summaries are for navigation; **permalinks** are the source of truth.

**Likely building blocks** (if implemented): a **read-mostly** runtime subscribed to merge (and optional slash) events, plus a **formatter** that maps raw events into the narrative — **deterministic** (template + sorted events) or **LLM-assisted** with a strict fact bundle; if LLM-assisted, summaries must **not contradict** linked comments.

**Sandbox (conceptual):** **Read-mostly** GitHub API access and optional internal observability stores; **post or edit** only the audit comment; **no** merge or branch write; no execution of repo code unless explicitly required for forensics (default: none).

**Risks if adopted:** Noisy timelines and **API rate limits** — formatters should **summarize with links** and cap inline content. **Narrative drift** if an LLM misstates history unless output is **schema-locked** to verified facts.

**Open design questions** (defer until adoption): Audit comment on **issue only**, **PR only**, or both; **multiple PRs** per issue; **squash** vs **merge** commit naming in the narrative; minimum **fact** fields before posting; deterministic vs LLM formatter trade-offs.

## References

- [Vision](../vision.md)
- [Roadmap](../roadmap.md)
- [README](../../README.md)
- [Agent architecture](../problems/agent-architecture.md)
- [Security threat model](../problems/security-threat-model.md)
- [Autonomy spectrum](../problems/autonomy-spectrum.md)
- [Code review](../problems/code-review.md)
- [Governance](../problems/governance.md)
- [ADR 0001 — Use ADRs for decision making](./0001-use-adrs-for-decision-making.md)
