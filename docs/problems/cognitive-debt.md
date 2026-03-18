# Cognitive Debt

How do we leverage the power of agents without losing our ability to understand and reason about the systems being
built, and our ability to guide their evolution over the long-term?

## The problem

The concept of cognitive debt and the concerns it raises are well articulated in [a blog
post](https://margaretstorey.com/blog/2026/02/09/cognitive-debt/) by Dr. Margaret-Anne Storey. In brief, cognitive debt
occurs when the implementation of a system diverges from the mental model of that system held by the humans involved in
the development and maintenance of the system.

In the traditional software development lifecycle, cognitive debt is managed naturally through routine development
activities: reading and writing code, design discussions, debugging issues, and code review. When these activities are
performed partially or completely by agents, opportunities for realignment of mental models with the implementation are
reduced and cognitive debt accrues at an increasing rate.

### Why it matters

The ability of an agent to produce high quality output is highly dependent on *what* it is being asked to do, and *how*
it is asked to do it. To accurately express a desired final state, you need to have a reasonable understanding of the
current state.

Cognitive debt represents an incomplete and/or inaccurate understanding of the current state. As the level of cognitive
debt grows it becomes increasingly difficult to reason about the behavior of a system, not just what it does or how it
does it, but why it behaves as it does. Without that understanding, evaluating whether a new feature request or design
change is reasonable and appropriate becomes much more difficult, and each change carries more risk.

[Ambient Code](https://ambient-code.ai/2025/09/23/the-path-to-vibe-coding-for-the-enterprise/) suggests that the new
role of developers will be as "shepherds" of codebases, guiding teams of agents to implement project outcomes. This role
requires a high-level understanding of the code, its intent, its design, and how it fits into larger systems. As
cognitive debt increases, that understanding is eroded and the ability of a developer to act as an effective shepherd
becomes limited.

For Konflux, this concern is magnified by the distributed architecture of the system. The model of system operation is
distributed across multiple services, codebases, and teams, making the impact of a change in design or behavior more
difficult to evaluate for both humans and agents.

## Mitigations

Managing cognitive debt in agentic workflows requires deliberate new practices.

### Maintenance of artifacts defining intent and architecture

Every change to a codebase has the potential to move it away from its original intent, as understood by the
developers. When that change is made by an agent, the context for the change that would normally be shared via design
discussion and code review may be lost. To combat this, any significant change should be accompanied by updates to
documents and other artifacts (diagrams, flow charts, etc.) that help articulate the intent and design of the system.

### Recurring change review

Establishing a regular process of reviewing changes provides two important functions: detecting when the project is
diverging from its original intent, and driving down cognitive debt by realigning mental models with the current
implementation. These reviews should be frequent, to avoid the amount of change becoming unmanageable, and
collaborative, so the entire team (or teams) can ensure they have a consistent understanding of the intent and design of
the project.

### Experimentation and adaptation

The interaction between human developers and agents is rapidly evolving, and what works best for different teams and
codebases is still largely unknown. As teams experiment with different approaches to applying agents to the software
development lifecycle they should remain cognizant of cognitive debt and experiment with multiple strategies to manage
it.

## Relationship to other problem areas

- [Intent Representation](intent-representation.md) discusses how intent is expressed. This document covers maintaining
  alignment between intent, implementation, and the mental model of developers.
- [Human Factors](human-factors.md) highlights many closely related concerns about the human role in agentic-first
  workflows.

## Open questions

- How do we measure cognitive debt?
- What's the right frequency for change reviews?
- How do you balance review overhead against velocity gains?

## Conclusion

Unchecked cognitive debt will leave developers unable to guide the systems they're responsible for. Reaping the benefits
of agents and other AI tools without falling victim to the risks of cognitive debt is going to require new techniques
and processes to ensure the value of developer expertise and human innovation isn't lost in the race to deliver code
more quickly.
