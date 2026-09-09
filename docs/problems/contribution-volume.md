# Contribution Volume

What happens when AI-generated contributions overwhelm a project's capacity to evaluate them?

## The problem

Fullsend's core model assumes an organization deploying agents into its own repositories — agents it controls, working on intent it authorized. But the same AI capabilities that enable internal autonomy also enable *external* contribution at unprecedented scale.

Steve Yegge's experience maintaining Beads and Gas Town illustrates the near-term reality: ~50 community PRs per day, most AI-assisted, submitted by external contributors the maintainer didn't direct and doesn't control. This isn't a theoretical concern — it's happening now on popular open-source projects, and the volume will only increase.

This document examines the contribution volume problem: what happens when anyone with an API key can generate and submit polished-looking contributions faster than any human can evaluate them. It assumes those contributors are acting in good faith — they want the project to succeed, even if their volume exceeds the project's capacity. Coordinated inauthentic contributions, where volume is the weapon rather than a side effect, are a distinct threat class treated in the [security threat model](security-threat-model.md#threat-7-coordinated-inauthentic-contributions).

## How this differs from the internal model

Fullsend's existing problem documents implicitly assume a controlled environment:

- **Intent representation** assumes authorized intent exists before implementation begins
- **Agent architecture** assumes agents operate under organizational policy
- **Code review** assumes the review system is evaluating work it commissioned
- **Autonomy spectrum** assumes the organization decides what to automate

External AI-generated contributions violate all of these assumptions. There is no prior intent authorization. The contributing agent operates under someone else's policy (or none). The review system must evaluate unsolicited work. And the volume is determined by external actors, not the project's own capacity.

## The economics of cheap contributions

Before AI, submitting a PR to an open-source project required meaningful effort: understanding the codebase, writing code, writing tests, dealing with CI failures. This effort served as a natural filter — only motivated contributors with genuine interest invested the time.

AI-generated contributions break this filter. The cost of producing a polished-looking PR drops to minutes and a few dollars in API costs. The implications:

- **Volume increases nonlinearly.** Each contributor can produce more PRs, and the barrier to becoming a contributor drops to "had an idea and an API key."
- **Quality distribution shifts.** More PRs at the "looks right but isn't quite right" level — functional enough to seem valuable, not good enough to merge without work. This is the most expensive category to evaluate because it requires deep review to determine salvageability.
- **Saying "no" becomes more expensive socially but more necessary strategically.** A well-implemented PR that doesn't align with the project's direction still demands attention. When the contributor invested minutes instead of days, the social cost of rejection shifts — but doesn't disappear, because the contributor often doesn't perceive their effort as trivial.
- **Motivation becomes opaque.** A human contributor who spent weeks building a feature is probably genuinely invested in the project. An AI-generated PR tells you nothing about the contributor's commitment, understanding, or intention.

The [downstream/upstream](downstream-upstream.md) document explores this for organizational contributors. Here the problem is broader: it affects *all* external contributions, including from individuals with no organizational affiliation.

## PR categorization under volume pressure

At scale, not every PR deserves the same depth of evaluation. Yegge's practical workflow categorizes incoming PRs and dispatches them accordingly:

- **Trivially good:** Obvious improvements (typo fixes, documentation, small bug fixes with tests). Can be evaluated quickly, possibly with agent assistance.
- **Needs work but has value:** Contributor had a good idea but the implementation needs iteration. The question is whether the maintainer's time (or an agent's tokens) to get it to merge-quality is worth the value.
- **Well-implemented but unwanted:** Technically solid but doesn't fit the project's direction, duplicates existing work, or adds unwanted complexity. These are the hardest to handle — the contributor did good work, but the answer is still no.
- **Low quality / AI slop:** Clearly generated with minimal thought, doesn't understand the codebase, breaks existing patterns. Easy to reject but high volume.

This categorization is essentially a triage system operating at the project boundary. Fullsend's [agent architecture](agent-architecture.md) describes triage for *issues*, but doesn't address triage for *unsolicited contributions*.

## The salvage question

The dominant model in open source is binary: accept or reject. But AI changes the economics of a third option: *salvage*.

When a contribution has a good idea but poor implementation, a project agent could:

1. Identify the valuable intent behind the PR
2. Reimplement it properly (respecting codebase patterns, adding tests, handling edge cases)
3. Credit the original contributor
4. Merge the agent-produced version

This inverts the traditional model. Instead of the contributor iterating until their PR is acceptable, the project takes ownership of getting the contribution across the finish line. Yegge describes this as "optimizing for community throughput" — the goal is to extract value from contributions rather than to gate quality.

**Trade-offs:**

- **Pro:** Increases project throughput. Contributors whose ideas are merged (even if rewritten) feel valued and stay engaged.
- **Pro:** Reduces the social cost of "no" — the project says "yes, and we improved it" rather than "no, try again."
- **Con:** Costs tokens. The project pays for the rewrite. At scale, this is a meaningful cost.
- **Con:** Can set expectations that all contributions will be polished for the contributor, reducing their incentive to submit quality work.
- **Con:** The "idea" extracted from a poor PR may not match the contributor's actual intent. The project may be building something the contributor didn't really want.
- **Con:** Attribution and credit become complicated. Who "wrote" the contribution?
- **Con:** A contributor who always accepts review and expects the project to rewrite is indistinguishable from the [speed-to-merge](security-threat-model.md#threat-7-coordinated-inauthentic-contributions) pattern that coordinated inauthentic actors use. Salvage-for-throughput can reward the attack.

## The taste problem

Yegge estimates that agents handle roughly 75% of the work well, but the remaining ~25% requires human judgment. That 25% isn't about correctness — it's about *taste*: architectural judgment, design aesthetics, knowing what to include and what to leave out, maintaining project coherence.

Taste is hard to codify. It's the difference between "this code works" and "this code belongs in this project." Fullsend addresses parts of this through [architectural invariants](architectural-invariants.md) (hard constraints) and [intent representation](intent-representation.md) (authorized work). But there's a gap between what invariants can enforce and what a thoughtful maintainer would accept.

Some of what constitutes taste:

- **API design sense.** A contribution adds a function that works correctly but exposes internals that shouldn't be part of the public API. No invariant covers this.
- **Complexity budget.** The project has an implicit complexity budget. A feature that's individually reasonable might push the project past the point where newcomers can understand it. This is a judgment call, not a rule.
- **Consistency that isn't convention.** Not the kind of consistency linters catch (naming, formatting) but deeper patterns: how errors propagate, how modules communicate, what gets abstracted vs. left concrete.
- **Strategic fit.** The feature works, passes tests, doesn't violate invariants — but doesn't belong. The project is going in a different direction, and accepting this PR makes it harder to get there.

This suggests that some review concerns can't be decomposed into sub-agents — they require a human (or a very carefully calibrated agent) who holds the project's overall shape in mind. The [code review](code-review.md) decomposition into correctness/security/intent/style may be missing a "coherence" or "fitness" dimension that's harder to formalize.

## Fork dynamics

If a project consistently rejects contributions, contributors can fork. Before AI, forking was expensive — maintaining a fork required sustained effort. AI changes the economics:

- Forking and customizing becomes cheap. An agent can maintain a fork, cherry-pick upstream changes, and apply custom patches indefinitely.
- The cost asymmetry reverses: it may be cheaper for a frustrated contributor to maintain an AI-maintained fork than to iterate on PRs against a gatekept upstream.
- Projects that are too conservative about accepting contributions risk losing their community to forks. Projects that are too permissive risk accumulating incoherent features.

This is a project-level version of the [autonomy spectrum](autonomy-spectrum.md) problem: how much to accept vs. how much to gate. But it operates at the project boundary rather than within a controlled organization.

## When volume is the attack

The rest of this document treats volume as a capacity problem created by cheap, good-faith AI-assisted work: too many real ideas, not enough maintainer hours. A distinct class of actor manufactures that volume on purpose.

Coordinated inauthentic contributions — sometimes called "claws" — use the same cheap-PR economics to farm credibility, promote clones, and exhaust attention. Individually the artifacts look like the "needs work but has value" or even "trivially good" categories above. Collectively they are a campaign. The [security threat model](security-threat-model.md#threat-7-coordinated-inauthentic-contributions) treats this as its own threat class; this section is only about how it changes the volume problem.

Consequences for the workflows this document discusses:

- **Triage at the project boundary** cannot be only "is this salvageable?" It also has to ask "is this part of a campaign?" — a question about identity and cross-repo pattern, not about the diff. Getting that wrong either burns capacity on inauthentic work or rejects a legitimate first-time contributor.
- **Salvage** is an attractive target. A claw that always accepts reviewer feedback looks like the ideal salvage candidate: low social friction, high apparent cooperation. The project pays tokens to rewrite work that was never meant to land as the contributor wrote it.
- **Signals of thoughtful vs. spammy AI use** (see [open questions](#open-questions)) are necessary but not sufficient. A campaign can satisfy stated contribution rules, pass CI, and still be inauthentic in intent. Content quality is not a proxy for authenticity.
- **Actor-trust rate limits** fail when volume is spread across accounts and each contribution is small. See [Threat 6](security-threat-model.md#threat-6-denial-of-service-dos--resource-exhaustion) and [Threat 7](security-threat-model.md#threat-7-coordinated-inauthentic-contributions).

Existing platform controls (write-permission gating so untrusted actors cannot trigger agents) reduce the *agentic* amplification of this volume. They do not reduce the human-attention cost of a full issue and PR queue, which is the original volume problem with a hostile twist.

## Relationship to other problem areas

- **[Downstream/Upstream](downstream-upstream.md)** addresses organizational contributors and priority reconciliation. This document extends that to unaffiliated individual contributors and the volume problem specifically.
- **[Code review](code-review.md)** designs the review process but assumes manageable volume. Contribution volume asks what happens when volume exceeds review capacity.
- **[Intent representation](intent-representation.md)** assumes authorized intent. External contributions lack prior authorization — the intent must be *inferred* from the PR itself, a fundamentally different problem.
- **[Agent architecture](agent-architecture.md)** describes triage for issues. Extending triage to unsolicited PRs is a natural but non-trivial expansion.
- **[Human factors](human-factors.md)** discusses review fatigue for internal work. External contribution volume compounds this — maintainers reviewing unsolicited PRs experience a different kind of fatigue than reviewing work they directed.
- **[Contributor guidance](contributor-guidance.md)** focuses on making rules clear. Under volume pressure, the question becomes whether clear rules reduce volume (by discouraging misaligned contributions) or just make them more sophisticated (contributors use AI to satisfy all stated requirements while still submitting strategically misaligned work).
- **[Governance](governance.md)** — contribution volume intersects with governance when the project must decide its philosophy: optimize for throughput (accept and improve) vs. optimize for coherence (gate strictly). This is a governance decision that shapes the project's identity.
- **[Security threat model](security-threat-model.md)** — high-volume external PRs are a natural vector for temporal split-payload attacks, and [coordinated inauthentic contributions](security-threat-model.md#threat-7-coordinated-inauthentic-contributions) are a distinct identity-level class that uses volume as a weapon rather than a side effect.

## Open questions

- At what volume does the traditional review model break? Is it 10 PRs/day? 50? 100? The answer likely depends on project size and team capacity, but are there useful heuristics?
- Can triage agents reliably distinguish "needs work but valuable" from "well-implemented but unwanted"? The former deserves investment; the latter deserves a clear, quick rejection. Getting this wrong in either direction is costly.
- How should salvaged contributions be attributed? The idea came from contributor A, but the code was rewritten by an agent. Does contributor A get a co-author credit? A mention in the commit message? Nothing?
- What signals indicate that a contributor is using AI thoughtfully vs. spamming PRs? Is there a way to distinguish genuine AI-assisted contributions from "I told the AI to make PRs to every project I've starred"? Coordinated inauthentic campaigns raise a harder version of this question: the artifacts may be thoughtful-looking and still be a land-grab. See [Threat 7](security-threat-model.md#threat-7-coordinated-inauthentic-contributions).
- Should projects set explicit contribution policies for AI-generated PRs? Some Linux kernel subsystems are experimenting with this. What works and what creates perverse incentives?
- How does the cost of PR salvaging compare to the cost of contributor churn from rejection? Is there a crossover point where salvaging is net-positive?
- Can the "taste" dimension of review be approximated by agents that are deeply calibrated on a project's existing decisions, or does it fundamentally require human judgment?
- How does contribution volume interact with the [security threat model](security-threat-model.md)? High-volume external PRs are a natural vector for the temporal split-payload attack pattern — one "innocent" PR weakens a test, a later one exploits the gap. Does volume make this harder to detect? Separately, when the volume is itself inauthentic, the problem is no longer only cover for a later payload — see [coordinated inauthentic contributions](security-threat-model.md#threat-7-coordinated-inauthentic-contributions).
- What's the governance model for accepting or rejecting a contribution philosophy? If maintainers disagree about throughput vs. coherence, how is that resolved?
