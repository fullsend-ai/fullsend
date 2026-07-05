# Contributor Guidance

How do we make contribution rules clear to both human contributors and AI agents — without requiring contributors to use AI themselves?

## Why this matters

Open source projects aspire to accept contributions from the general public. Contributors range from individual hobbyists to engineers at large corporations, with varying levels of familiarity with the project's architecture and conventions.

If agents are actively developing the project, the contribution process must work for:

- **Human-only contributors** — someone opening their first PR with no AI tooling
- **Human-assisted contributors** — someone using AI to help write code but driving the process themselves
- **Agent-driven contributors** — an organization's internal agent submitting PRs on behalf of its team
- **Hybrid teams** — a mix of the above working on the same codebase

The challenge: optimizing for agent workflows can make things harder for humans, and vice versa. The solution must serve both.

## The core problem: implicit vs. explicit knowledge

The fundamental issue isn't about machine-readable formats vs. human-readable documentation. AI coding assistants (Claude, GitHub Copilot, Cursor) understand natural language just fine. The problem is **what's written down vs. what's learned through participation**:

**Humans learn through:**
- Asking questions in Slack, issues, or PR comments
- Watching what gets approved vs. rejected in code review
- Mentorship from experienced contributors
- Institutional memory ("we tried that approach in 2023 and it caused problems")
- Understanding *why* through context they absorb over time

**Autonomous AI agents only have:**
- What's written in the repository (docs, code, git history)
- They can't ask clarifying questions to humans
- They can't attend meetings or hear hallway conversations
- They don't absorb institutional knowledge over time
- They can't learn from "we discussed this last quarter and decided..."

This creates a **verbosity gap**: to enable AI agents, we need to write down all the institutional knowledge that humans currently absorb through participation. But making documentation that verbose can overwhelm human contributors who can't process that volume of detail in at once.

## The two-audience problem

Every contribution guideline, every architecture decision record, every CODEOWNERS constraint serves two audiences now:

1. **Humans** who learn contribution norms through word of mouth, mentorship, and institutional knowledge
2. **AI agents** (like Claude, GitHub Copilot, or Cursor) that only have access to what's written down in the repository

These audiences have different needs:

| Audience | How they learn | What they need in documentation |
|---|---|---|
| Human contributor | Mentorship, community interaction, "word of mouth" | Enough context to get started; can ask questions and learn the rest |
| AI agent | Only written documentation in the repo | Everything explicit and written down; can't ask mentors or attend meetings |
| Human reviewer | Institutional knowledge from being involved in the project | Written rationale they can reference; fills gaps with experience |
| AI agent reviewer | Only written architectural decisions and policies | Complete decision history and rationale; can't rely on "we discussed this last quarter" |

Today's open source contribution guides assume humans will learn the unwritten norms through community participation: "Be respectful. Follow the code style. Write tests. Open an issue first for large changes." This works because humans can:

- Ask clarifying questions on Slack or in issue comments
- Learn by watching what gets approved vs rejected
- Absorb institutional knowledge from code review discussions
- Understand implied context ("we prefer X over Y" → because we had a bad experience with Y last year)

AI agents can't do any of this easily. Most of today's agent "knowledge" is documented in the codebase itself through plain text files (typically Markdown format). This content is typically **verbose** and not easy for humans to absorb.

## What contributors need to know

At a minimum, any contributor (human or agent) needs to understand:

### Intent authorization tier classification

Is my change a bug fix, a small improvement, a new feature, or an architectural change? The [intent representation](intent-representation.md) intent authorization tier system exists, but:

- Can a new contributor reliably classify their own change?
- If they misclassify (intentionally or not), how does the system course-correct?
- Are the intent authorization tier definitions publicly documented in a way that's discoverable?

### Authorization requirements

What approvals does my change need? Intent authorization tier 0 (standing rules) needs none. Intent authorization tier 1 (tactical) needs a linked issue. Intent authorization tier 2+ (strategic) needs explicit authorization. But:

- How does a first-time contributor know this?
- If they're fixing what they perceive as a bug, do they need to understand that some "bugs" are actually feature requests requiring higher-intent-authorization-tier authorization?
- What happens if they submit a PR without the required authorization? Is there a helpful error message, or does it just languish?

### Repository-specific conventions

Each repo may have its own CODEOWNERS boundaries, architectural invariants, and local conventions. How does a contributor discover these?

- **CONTRIBUTING.md** — Human-oriented guidelines, often concise
- **CLAUDE.md** (and others) — Instructions targeted to an AI agent implementation
- **Architecture docs** in the organization's architecture repo — Cross-repo invariants and design decisions
- **CODEOWNERS** — Shows *who* reviews *what*, but not *why* those paths are guarded (the "why" should be documented elsewhere)
- **Git history and merged PRs** — Patterns and conventions shown through example

Human contributors can ask questions to fill gaps. AI assistants need it all written down. A contributor needs to know which of these exist and where to find them.

### Testing and CI expectations

Human and agents will need to understand how to contribute automated tests alongside code:

- What tests need to pass before a PR is ready for review?
- What are the conventions for contributing new tests?
- What distinguishes unit, integration, and end to end testing? Which types of testing are
  emphasized?
- Which tests can be run locally vs. those that require a dedicated environment?
- Do code coverage metrics matter, and how are they computed?

Historically, human conventions around testing are implied through `Makefile` targets, CI
configurations, and instructions in contributor guides. Agents can be instructed through **CLAUDE.md** and similar system prompt files.

### Draft/WIP Pull Requests

For exploratory contributions ("I think this might fix the issue but I'm not sure"), what's the low-friction path to get feedback?

- **Draft PRs** are the GitHub-native answer — they signal "work in progress, feedback welcome" regardless of who opens them (human or agent), however most automated testing is disabled on these types of contributions. Should agent review be disabled as well?
- **WIP** pull request titles are another convention. These signal code is not ready for merge, however they often do have automated testing enabled. Should agents review this category of pull request?
- Do we expect agents to generate exploratory/draft changes?
- Draft status is about the state of the work, not about who submitted it — the same exploratory/iterative workflow applies whether the PR comes from a human experimenting or an agent exploring alternatives

## Key Principles

### No Agent Assistance Required

A critical constraint: **using AI must not be required to contribute to the project.**

This means:

- Humans without AI assistants can still contribute successfully
- Documentation should be readable by humans, not just comprehensive enough for AI
- Contribution processes must work without AI tooling
- Humans can ask questions, get mentorship, and learn through participation
- AI-assisted contributions shouldn't get systematically faster processing or better treatment

This is both a practical accessibility concern and a philosophical one. Open source should be open to everyone, including those who:
- Choose not to use AI for personal or professional reasons
- Cannot afford commercial AI services
- Are learning and want to engage directly without AI mediation
- Work in environments where AI tools are restricted

Beyond accessibility, there's a deeper question about whether contribution remains meaningful when agents handle routine work. See [human-factors.md](human-factors.md) for exploration of contributor motivation, domain ownership, and the shift from hands-on development to supervising agent output.

### Human and Agent Equality

The "no agent required" principle also has security implications, aligned with the [zero trust model](security-threat-model.md):

**The system shouldn't grant preferential treatment to an input simply because it appears to be from an agent that belongs to the system.** This means:

- Human-generated and agent-generated contributions must be treated identically
- No fast-tracking or skipping validation for agent-submitted PRs
- No assumption that "internal" agents have done their validation correctly
- All input treated as potentially adversarial, regardless of source
- Same review criteria, same approval requirements, same security checks

This prevents a compromised or drifted agent from exploiting trust relationships. A contribution's validity is determined by its content and compliance with requirements, not by who (or what) submitted it. This creates a more secure system while also ensuring fairness for human contributors.

The goal: make implicit knowledge explicit (which helps AI agents) **without** making contribution so bureaucratic that it requires AI assistance to navigate.

## Relationship to other problem areas

- **[Intent representation](intent-representation.md)** — the intent authorization tier system must be explained to contributors
- **[Code review](code-review.md)** — review expectations and criteria must be transparent
- **[Codebase context](codebase-context.md)** — what knowledge must agents know to succeed?
- **[Governance](governance.md)** — who decides what rules contributors must follow?
- **[Autonomy spectrum](autonomy-spectrum.md)** — CODEOWNERS boundaries affect what changes contributors can make
- **[Architectural invariants](architectural-invariants.md)** — contributors need to know what constraints exist
- **[Human factors](human-factors.md)** — while this document focuses on making rules accessible, human factors explores whether the resulting contribution experience remains meaningful and rewarding for human participants

## Open questions

- Should intent authorization tier classification be self-reported by contributors or determined by reviewers (human or AI)? What if they disagree?
- How do we handle the learning curve for new human contributors who don't yet understand the intent system, while also providing enough written context for AI assistants?
- What's the right balance between "helpful guidance" (from AI reviewer agents) and "intrusive gatekeeping"? How do we ensure AI feedback is constructive?
- How do we measure whether contribution guidance is working? (time to first PR merge? contributor retention? reduction in misclassified changes? satisfaction of AI-assisted vs. unassisted contributors?) See also [human-factors.md](human-factors.md) for metrics around contributor engagement and meaningful participation.
- Should there be a "sandbox" repo where contributors can experiment without worrying about intent authorization tier classification and authorization?
- How do we handle contributions from organizations that have their own AI agents opening PRs? Do external AI assistants need special guidance beyond what's in CONTRIBUTING.md and CLAUDE.md?
- What happens when a human contributor disagrees with an AI reviewer's classification or feedback? Is there an escalation path to human reviewers?
- How do we keep contribution documentation up-to-date as the agent system evolves? Who is responsible for capturing new institutional knowledge as it emerges?
- Should contribution guidelines be versioned? If the intent authorization tier definitions change, how do in-flight contributions handle the transition?
- How do we avoid creating a "two-class" system where AI-assisted contributions get faster processing than unassisted human contributions?
- How verbose is too verbose? At what point does comprehensive documentation (helpful for AI) become overwhelming for human contributors?
- Should we explicitly signal which documentation is "need to know" for humans vs. "supplementary context" primarily for AI assistants?
- How do we capture and document the "why" behind decisions when that context is currently tribal knowledge?

## Potential Solutions

Below is a suggested set of solutions that could be candidates for experiments.

### Make implicit knowledge explicit

Write down everything that's currently communicated through word of mouth, mentorship, or institutional memory:

- **Why we prefer certain patterns** — "We use X rather than Y because we had problems with Y in the past"
- **Historical context for decisions** — "The schema uses approach A rather than B because B wasn't mature enough when we designed this"
- **Common mistakes and how to avoid them** — "New contributors often forget to update the mock when changing an interface. Run `make generate` to catch this."
- **Unwritten conventions** — "We prefer table-driven tests. See `path/to/example_test.go` for the pattern."

See [applied docs](applied/) for organization-specific examples of implicit knowledge that should be made explicit.

This isn't a separate CLAUDE.md for agents vs CONTRIBUTING.md for humans. It's making the existing CONTRIBUTING.md and architecture docs more complete and explicit, so both humans and agents can learn from them.

**Pros:** Single source of truth. Benefits humans too (especially new contributors). No special formats or duplication.

**Cons:** Requires discipline to capture institutional knowledge as it emerges. Risk of becoming encyclopedic and overwhelming. Needs curation to stay relevant. Documentation updates should be triggered when patterns emerge from multiple sources (code reviews, agent feedback, repeated questions).

### Minimal CLAUDE.md with references to CONTRIBUTING.md

Keep CLAUDE.md minimal (under 60 lines, per research from ETH Zürich showing verbose context hurts agent performance by 20%+), but have it reference comprehensive content in CONTRIBUTING.md. This creates a single source of truth that serves both humans and agents.

CLAUDE.md should contain:
- **Links to CONTRIBUTING.md sections** — point agents to the comprehensive human-readable guidance
- **Non-obvious context only** — what agents cannot discover from reading the code
- **BOOKMARKS.md references** — for progressive disclosure of additional context (architectural docs, API conventions, etc.)

Example minimal CLAUDE.md:
```
See CONTRIBUTING.md for full contribution guidelines. Key non-obvious points:

- API changes: see CONTRIBUTING.md#api-changes. Always check architecture repo.
- We had production incidents from unvalidated fields in Q3 2024 - all new fields need validation.
- Cross-repo impact: if changing shared schemas, downstream consumers likely need updates.

See BOOKMARKS.md for architectural context and external standards.
```

See [applied docs](applied/) for organization-specific CLAUDE.md examples.

The comprehensive content lives in CONTRIBUTING.md, where both humans and agents can access it. CLAUDE.md is just the "non-obvious shortcuts" layer plus navigation.

**Pros:** Root source of truth in CONTRIBUTING.md. No duplication or sync burden. Aligns with research on minimal, human-written agent context. Humans benefit from comprehensive CONTRIBUTING.md too.

**Cons:** CONTRIBUTING.md needs to be comprehensive enough for both audiences, which requires capturing institutional knowledge. CLAUDE.md must stay minimal and resist feature creep.

### Layered documentation with progressive disclosure

Not all contributors need to know all rules. Structure guidance in `CONTRIBUTING.md` by contribution complexity.

This is a **conceptual model for organizing information**, not a prescription for specific file structures. Different repos might implement this through headings in CONTRIBUTING.md, separate docs/ files, or other approaches:

| Tier | Contributor persona | What they need |
|---|---|---|
| **First-time** | Opening first PR, fixing a typo or small bug | Minimal friction: where to open PR, how to run tests, basic code style |
| **Occasional** | Has contributed before, submitting bug fixes or small improvements | Intent authorization tier classification, issue linkage, CODEOWNERS awareness |
| **Regular** | Frequent contributor, proposing features or architectural changes | Full intent system, authorization process, cross-repo impact analysis |
| **Core maintainer** | Has commit access, reviewing others' work | Governance model, agent configuration, security threat model |

AI agents acting on behalf of regular contributors should have access to all layers, since they can't gradually absorb knowledge through repeated participation.

**Pros:** Reduces cognitive load for new human contributors. Avoids overwhelming people with rules they don't need yet.

**Cons:** New human contributors miss context buried in more "advanced" documentation. Agents have advantage because their system prompts are "loaded" with required rules and context.

### AI-assisted onboarding and feedback

When a contributor opens a PR, an AI agent (acting as a reviewer/helper) provides contextual guidance:

- "This change touches API surface, which requires human CODEOWNERS approval — see [link] for details"
- "This looks like an intent authorization tier 2 feature. You'll need to open an intent proposal at [repo] first."
- "CI is failing because [specific test]. Here's how to run it locally: [command]"

This applies to **all PRs equally** — whether opened by a human contributor working alone, a human with AI assistance, or an AI agent acting autonomously. Treating all contributions the same way aligns with the zero trust principle: no agent should assume another agent has done its job correctly. This also provides better auditability (showing agent-to-agent handoffs) and simpler implementation (no need to detect or differentiate PR sources).

**Pros:** Guidance appears exactly when needed. Reduces documentation burden. Can help humans learn the implicit rules. Provides audit trail for agent-to-agent interactions. Aligns with zero trust security model.

**Cons:** Requires AI review agents to be deployed and reliable. May feel intrusive or patronizing if done poorly. Doesn't help contributors *before* they submit a PR.

### Public contribution checklist

Make the review criteria public and explicit. Before submitting a PR, contributors (or their AI assistants) can see exactly what will be checked:

- [ ] Intent authorization tier classification: _____
- [ ] Linked issue (if intent authorization tier 1+): _____
- [ ] Authorization record (if intent authorization tier 2+): _____
- [ ] Tests added/updated: yes/no
- [ ] CODEOWNERS approval needed: yes/no (auto-detected)
- [ ] Architectural invariants verified: yes/no

This checklist is the same whether a human manually verifies it or their AI assistant checks it before opening the PR.

**Pros:** Transparency. No surprises. Humans can self-check before submitting. AI assistants can validate requirements proactively.

**Cons:** Checklist needs to be comprehensive yet not overwhelming. May feel bureaucratic for small changes. Humans might skip it; AI assistants might over-rely on it.
