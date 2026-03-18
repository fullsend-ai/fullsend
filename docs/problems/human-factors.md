# Human Factors

What happens to the people when agents take over the routine work?

The other problem documents focus on making the agentic system work correctly and securely. This one asks whether the people involved will *want* to participate alongside it, and whether the system inadvertently degrades the things that make contributing to open-source projects rewarding.

## Domain ownership and expertise

Today, people build deep knowledge of specific subsystems. "Ask Sarah about the build pipeline" or "James knows the reconciler inside out." This expertise is valuable in itself and is also a source of professional identity and satisfaction.

In a fully autonomous model, agents don't develop relationships with code the way people do. If an agent can implement, review, and merge changes to the reconciler without James, what is James's role? He may still be listed in CODEOWNERS for guarded paths, but if his main interaction is approving changes he didn't write, didn't review, and didn't design, his relationship to the subsystem changes fundamentally.

**Tensions:**

- Domain experts are exactly the people whose approval is most valuable on guarded paths. But if the only work left for them is approving agent-driven changes, the role shrinks.
- Expertise atrophies without practice. If agents do all the implementation, the domain expert's knowledge gradually becomes stale.
- Onboarding new domain experts becomes harder if the path to expertise (writing code, making mistakes, understanding consequences) is removed.

## Role shift: from author to supervisor

The vision document says "humans set direction, agents execute." For many contributors, this describes a less satisfying way to participate. Writing code, debugging, and shipping are core to why people contribute to open-source projects. Supervising agents is a fundamentally different activity.

**What changes:**

- **Creative work decreases.** Design and architecture remain human, but the hands-on problem-solving that many contributors enjoy moves to agents.
- **The feedback loop changes.** Instead of "I wrote this, I tested it, it works," the loop becomes "I described what I wanted, the agent built it, tests pass." The sense of ownership is weaker.
- **The primary output becomes intent, not code.** As described in [intent-representation.md](intent-representation.md), humans express what they want and agents implement it. The contributor's work shifts from writing Go or Rust to writing Markdown — intent documents, acceptance criteria, architectural constraints. Even "code review" isn't really code review: it's reviewing how well the agent interpreted and refined the intent. Contributors who built deep language expertise find that their daily tool is now prose.
- **The skill that matters changes.** Being effective stops meaning "writes excellent Go" and starts meaning "writes unambiguous intent documents that agents can execute correctly." This is a real skill, but it's a different one, and not necessarily what drew people to the project.

## Review fatigue

Agents handle code review (see [code-review.md](code-review.md)), so humans aren't reviewing PRs line by line. But humans still review — they review intent. On guarded paths, they approve changes to security-critical or architecturally significant areas. And they review how well agents interpreted and refined their intent documents.

This is still fatiguing, in different ways:

- **Volume.** Agents can generate and process changes faster than humans can evaluate whether the results match what they wanted. The bottleneck moves from "review this diff" to "verify this outcome aligns with what I meant."
- **Abstraction gap.** Reviewing intent alignment is harder than reviewing code. With code, you can trace logic. With intent, you're asking "did the agent understand what I meant?" — a fuzzier question that requires holding both the intent document and the implementation in mind.
- **Vigilance problem.** If agents correctly interpret intent 95% of the time, the remaining 5% becomes harder to catch. This is well-studied in automation research (see [evidence section below](#evidence-from-automation-and-ai-research)) — humans are poor monitors of mostly-correct automated systems, and this complacency cannot be trained away. The shift from code review to intent review doesn't fix this; it may make it worse, since intent misalignment is subtler than a logic bug.

## Contributor motivation in open source

Konflux-ci is an open-source project. Contributors participate for reasons beyond a paycheck — learning, building reputation, solving interesting problems, and being part of a community.

If agents handle routine contributions, what's left for human contributors?

- **High-value contributions become the only contributions.** This could raise the barrier to entry — new contributors often start with small fixes and build up to larger work.
- **Community dynamics change.** If most PRs are from agents, the social fabric of the project (review conversations, mentorship through PR feedback, shared ownership) thins out.
- **Recognition shifts.** In open-source, your commit history is your CV. If agents write most of the code, how do contributors demonstrate their value?

## Job security and professional value

Most konflux-ci contributors are paid engineers. For them, the concerns above have an additional dimension:

- If agents can do the routine 80% of the work, organizations need fewer engineers for the same output. The remaining engineers need to be the ones capable of the hard 20%.
- The transition period is particularly uncomfortable — people are being asked to help build a system that may reduce the need for their role.
- "Humans set direction" is reassuring until you realize that direction-setting is a smaller team than implementation.

This doesn't mean autonomous agents are wrong. But pretending this concern doesn't exist will generate resistance that looks like technical objections but is actually about something deeper.

## Evidence from automation and AI research

The concerns above are not speculative. There is a substantial body of research — from decades of automation studies and from recent AI-specific work — that supports them.

### The ironies of automation

Bainbridge's foundational ["Ironies of Automation" (1983)](https://doi.org/10.1016/0005-1098(83)90046-8) identified the central paradox: automating a task removes the practice that keeps operators skilled enough to intervene when automation fails. The more reliable the automation, the worse the problem — because operators have fewer opportunities to exercise their skills, and failures are rarer but harder to catch. This remains one of the most-cited papers in human factors research and its core argument is directly applicable: if agents handle all routine development, the domain experts who approve guarded-path changes lose the practice that makes their approval meaningful.

### The out-of-the-loop performance problem

Endsley & Kiris ["The Out-of-the-Loop Performance Problem and Level of Control in Automation" (1995)](https://doi.org/10.1518/001872095779064555) demonstrated that when operators are removed from active control, their situation awareness degrades — they may still perceive low-level data, but lose comprehension of what it means. Critically, **this effect was significantly greater under full automation than under intermediate levels of automation**. When operators retained some active role in the decision-making loop, their situation awareness was preserved and they performed better when they needed to intervene. This finding directly challenges a model where humans are fully removed from implementation: partial involvement isn't just a compromise, it produces measurably better oversight than full removal.

### Automation complacency and bias

Parasuraman & Manzey ["Complacency and Bias in Human Use of Automation" (2010)](https://doi.org/10.1177/0018720810376055) found that automation complacency occurs in both novice and expert users, **cannot be overcome with training or instructions**, and worsens under high automation reliability. Operators of highly reliable systems were 50% less likely to detect failures than operators of less reliable systems. In our context: if agents produce correct results 95% of the time, the remaining 5% becomes harder for human reviewers to catch, not easier.

### AI coding tools and skill formation

Recent research on AI coding tools specifically reinforces these patterns:

- An [Anthropic study (2025)](https://www.anthropic.com/research/AI-assistance-coding-skills) found that developers using AI coding assistance scored **17% lower on comprehension tests** when learning new libraries. Developers who delegated code generation to AI scored below 40% on comprehension, while those who used AI for conceptual inquiry scored 65% or higher. The mode of interaction — whether you do the work or delegate it — directly affects whether you build understanding.
- A [METR randomized controlled trial (2025)](https://metr.org/blog/2025-07-10-early-2025-ai-experienced-os-dev-study/) found that experienced open-source developers working on their own repositories were **19% slower with AI tools** — but perceived themselves as 20% faster. The gap between perceived and actual performance is relevant to the rubber-stamping risk: people may believe they are reviewing effectively when they are not.
- Research on [overreliance in human-AI interaction (2025)](https://arxiv.org/html/2509.08010v1) identifies cognitive offloading, automation bias, and the erosion of critical thinking as risks of LLM-assisted work, and notes that long-term deskilling is a serious concern in environments where AI tools dominate routine tasks.

### Newcomer pathways and communities of practice

Lave & Wenger's *Situated Learning: Legitimate Peripheral Participation* (1991) established that learning in communities of practice happens through a gradient — newcomers enter through small, peripheral tasks and gradually take on more central roles. If automation removes the peripheral tasks (small bug fixes, documentation, simple features), the pathway that produces future experts and maintainers is disrupted. This is not just an open-source concern; it applies to any organization that needs to develop new domain experts over time.

## Is the two-point model enough?

The [vision](../vision.md) defines two points of human participation: strategic intent and guarded paths. Everything else is autonomous. The problems described above — expertise atrophy, loss of creative work, review fatigue, disappearing contributor pathways — are all consequences of that model. But rather than treating them as side-effects to mitigate, it's worth asking whether they indicate a problem with the model itself.

The two-point model assumes that humans can remain effective at their two roles (setting direction and approving guarded-path changes) without doing the work in between. The research summarized above suggests they can't:

- **Guarded-path approval requires understanding.** If domain experts stop implementing changes, their ability to evaluate agent-produced changes degrades. The security model depends on informed human approval, but the autonomy model removes the activity that keeps humans informed. Over time, guarded-path approvals risk becoming rubber stamps — not because people are careless, but because they've lost the context that made their judgment valuable.
- **Strategic direction requires ground-level knowledge.** Architectural decisions that aren't grounded in recent hands-on experience with the codebase tend to drift from reality. If humans only interact with code through intent documents and approval queues, the quality of their strategic input declines.
- **The contributor pipeline depends on a gradient.** Open-source contributors enter through small contributions and grow into larger roles. If agent autonomy eliminates the small-contribution layer, the pipeline that produces future domain experts and maintainers dries up. The two-point model assumes a supply of capable humans at both points, but doesn't account for where those humans come from.

This doesn't mean the vision is wrong — it means it may be incomplete. There may need to be a third category of human participation: not strategic intent, not guarded-path approval, but **ongoing hands-on involvement** in parts of the codebase, specifically to maintain the understanding and skills that make the other two roles work.

What this third category looks like, how it interacts with the [autonomy spectrum](autonomy-spectrum.md), and whether it can be designed without simply slowing everything down, are open questions. But the problems documented in the sections above suggest that a model with only two human touchpoints may undermine its own foundations.

## What might help

These aren't solutions — they're directions worth exploring:

- **Guarded paths as meaningful ownership.** CODEOWNERS isn't just a security mechanism — it preserves genuine human ownership of the areas that matter most.
- **Agents as force multipliers, not replacements.** Design workflows where agents handle toil so humans can focus on harder, more interesting problems. But verify that this actually happens in practice — it's easy for "focus on harder problems" to quietly become "there are no problems left for you."
- **Rotation and growth.** If domain experts risk skill atrophy, create deliberate opportunities for hands-on work — spikes, experiments, prototypes that agents don't touch.
- **Transparent metrics.** Track not just agent effectiveness but human engagement. If humans are rubber-stamping intent approvals on guarded paths, the system is failing even if the code is correct.
- **Contributor pathways.** Explicitly design how new contributors enter the project when agents handle the easy on-ramps. Mentorship, pairing, or reserved "human-first" areas could help.
- **Honest communication.** Be open about the tensions. If the scope of human work is genuinely changing, say so — people will engage more constructively with a clear picture than with vague reassurances.

## Relationship to other problem areas

- **Governance** decides who controls the system, but human factors determine whether people *engage* with that control or quietly disengage.
- **Autonomy spectrum** defines what agents can do. Human factors asks what the experience is like for the humans on the other side of that boundary.
- **Code review** designs the agent review process. Human factors asks what it's like for humans when code review — traditionally a core engineering activity — is no longer something they do.
- **[Contributor guidance](contributor-guidance.md)** focuses on making contribution rules clear to both humans and agents. Human factors explores whether the resulting workflow remains rewarding enough to sustain human participation.

## Open questions

- How do we measure whether the contributor experience is healthy? What signals indicate disengagement before people quietly stop participating?
- Is there a natural limit to agent autonomy that preserves meaningful human involvement, or does the logic of automation always push toward full autonomy?
- How do we avoid a two-tier system where a small number of "architect" humans set direction while everyone else writes intent documents? Is that even avoidable?
- What skills should contributors be building to stay effective in a heavily agentic workflow?
- How do we handle the fact that different people will feel differently about this? Some engineers will welcome agent autonomy; others will experience it as loss. One-size-fits-all approaches won't work.
- Can agents themselves help with the human factors — for example, by surfacing "this area hasn't had human-authored changes in 6 months" as a signal worth attention?
- Should human engagement be an input to autonomy decisions? If full autonomy in a repo is degrading the understanding of the people responsible for its guarded paths, is that a reason to pull back autonomy — even if the code quality metrics look fine?
- What would a "third category" of human participation look like in practice? Is it reserved areas of the codebase, periodic implementation rotations, collaborative agent-human work, or something else?
