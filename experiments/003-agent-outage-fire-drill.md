# Experiment 003: Agent Outage Fire Drill

**Date:** 2026-03-16
**Status:** Proposed

## Hypothesis

After several months of agent autonomy, engineering teams will have difficulty operating without agents — not because agents are better, but because humans have lost familiarity with their own codebases and workflows. Specifically:

1. Tasks that took a predictable amount of time pre-autonomy will take significantly longer post-autonomy, even though the humans involved are the same people who built the systems.
2. Domain experts will need a reorientation period before they can implement effectively in subsystems they nominally own.
3. The team may not recognise the extent of their dependency until the agents are removed.

This tests the core claim from [Bainbridge (1983)](../problems/human-factors.md#the-ironies-of-automation): automation removes the practice that keeps operators skilled enough to intervene when automation fails. If the claim holds for software development, an agent outage — even a planned one — should produce measurable degradation in human performance.

## Why this matters beyond the experiment

This is chaos engineering for human capability. Netflix's Chaos Monkey kills production servers to prove the infrastructure can survive failures. This experiment kills the agent to prove the *people* can survive without it. The same logic applies: if you haven't tested the failure mode, you don't know whether you can handle it.

Agent outages are not hypothetical. Infrastructure fails, API keys expire, providers have incidents, budgets get cut. If the team cannot function during a two-week outage, the organisation has an undisclosed single point of failure in its engineering capability. This experiment surfaces that risk while there's still time to address it.

## Design

### Prerequisites

- A repo that has been operating with agent autonomy for at least three months (long enough for habits to shift)
- Pre-autonomy baseline data: typical task completion times, PR throughput, defect rates from before agents were introduced
- Team buy-in — this needs to be framed as a resilience exercise, not a punishment

### Setup

1. **Select a two-week window.** Avoid periods with major deadlines or releases. The point is to measure normal work under normal conditions, minus the agents.

2. **Disable agent access.** No agent-produced PRs, no agent reviews, no agent triage. Humans handle the full development lifecycle as they did before autonomy.

3. **Keep the backlog realistic.** Don't cherry-pick easy tasks or defer hard ones. The team should work from the same backlog they'd have worked from with agents. If agents would normally have picked up certain issues automatically, those issues still need to be addressed — by humans.

4. **Instrument everything.** Capture the same metrics you tracked pre-autonomy so the comparison is like-for-like.

### What to measure

**Quantitative:**

- **Task completion time** compared to pre-autonomy baselines. Not compared to agent-assisted speed (agents will obviously be faster) — compared to how fast the *same team* was before agents existed. If human performance has degraded relative to the team's own historical baseline, that's skill atrophy.
- **PR throughput** — number of PRs opened, reviewed, and merged per week.
- **Defect rates** — bugs introduced during the outage period vs. the pre-autonomy baseline. Are humans making more mistakes than they used to?
- **Time to first commit** — when someone picks up a task, how long before their first meaningful commit? A long orientation period suggests lost familiarity.
- **Questions asked** — how often do people ask for help understanding code they're nominally responsible for? Track Slack questions, PR comments, or pairing sessions that wouldn't have happened pre-autonomy.

**Qualitative:**

- **Daily journal entries** from participants: what was hard, what was surprising, what felt different. Capture reactions while they're fresh.
- **Retrospective** at the end of the two weeks: how did it feel? What was easier or harder than expected? Would you be comfortable if this were permanent?
- **Confidence self-assessment** at the start and end: "how confident are you that you can work effectively without agents?" Compare the start-of-drill prediction against the end-of-drill reality.

### Controls and caveats

- **The pre-autonomy baseline is the key comparison**, not agent-assisted performance. The question is "have humans degraded?" not "are agents faster?" We already know agents are faster.
- **Learning effects during the drill.** Performance may improve over the two weeks as people re-familiarise themselves. The trajectory matters — a steep initial drop that recovers suggests temporary rust, while a sustained deficit suggests deeper atrophy.
- **Hawthorne effect.** People may work harder during the drill because they know they're being observed. This makes the experiment conservative — if performance degrades even with heightened effort, the real-world situation is likely worse.

## Expected outcomes

**If the hypothesis holds:**

- Task completion times are significantly worse than pre-autonomy baselines, at least in the first week
- Domain experts need meaningful reorientation time in subsystems they own
- The confidence self-assessment reveals a gap — people predicted they'd be fine but weren't
- This is strong evidence for the [third category of participation](../problems/human-factors.md#is-the-two-point-model-enough): humans need ongoing hands-on involvement to maintain capability, and the current autonomy model doesn't provide it
- It also reframes agent autonomy as an organisational risk: a capability that degrades the team's ability to function without it

**If the hypothesis doesn't hold:**

- The team performs at or near pre-autonomy baselines after a brief adjustment
- Domain experts retain sufficient understanding of their subsystems
- This weakens the skill atrophy argument and suggests the two-point model may be sufficient for this context
- The drill still has value as a resilience exercise — confirming the team *can* operate without agents is worth knowing

**Mixed outcome (likely):**

- Some subsystems show degradation, others don't. The difference may correlate with how much hands-on work humans retained during the autonomy period — providing natural-experiment evidence for the third category argument.
- Some individuals adapt quickly, others struggle. The difference may correlate with experience level, domain depth, or how they interacted with agents (supervisory vs. collaborative).

## Timing

This experiment requires an agent autonomy period to have elapsed first. It can't run until at least one repo has been autonomous for several months. But it should be planned now so that:

- Pre-autonomy baseline data is captured before agents are introduced (you can't measure degradation without a baseline)
- The team knows the drill is coming (builds it into expectations rather than springing it as a surprise)
- The cadence is established early — this should be a recurring exercise (quarterly or biannually), not a one-off

## Relationship to other experiments

- **[Experiment 002](adr46-claude-scanner/README.md)** tests automated enforcement of architectural invariants. This experiment tests whether the humans responsible for architectural judgment can still exercise it without agent support.
- A complementary experiment could measure review quality specifically (can humans still evaluate agent-produced code?) — this experiment is broader, testing whether humans can still write, review, debug, and ship.
