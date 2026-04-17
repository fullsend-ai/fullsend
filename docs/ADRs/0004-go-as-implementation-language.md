---
title: "4. Go as the implementation language for core tooling"
status: Accepted
relates_to:
  - agent-infrastructure
  - agent-compatible-code
topics:
  - language
  - tooling
  - cli
---

# 4. Go as the implementation language for core tooling

Date: 2026-04-01

## Status

Accepted

## Context

Fullsend's core tooling — the CLI, installer, entry-point dispatcher, and
label-state-machine guard — needs to be distributed to adopting organizations
as a consumer-facing artifact. Adopters run this tooling on CI runners, local
machines, and GitHub Actions environments that the fullsend team does not
control. Easy, dependency-free installation matters more here than it does for
internal experiments.

The team held a prior informal vote on language preference and revisited the
decision in the 2026-04-01 team sync. Constraints that shaped the discussion:

1. **Distribution simplicity.** Adopting organizations should be able to
   download a single binary with no runtime dependencies. Languages that
   require a runtime or virtual environment create installation friction and
   support burden.
2. **LLM-generated code review.** Much of the implementation will be written
   or assisted by LLMs (primarily Claude). The team must be able to critically
   review generated code to catch hallucinations, security issues, and subtle
   bugs. This rules out languages the team cannot confidently vet.
3. **Experiments are exempt.** Experiments in the [experiments repository](https://github.com/fullsend-ai/experiments)
   are one-shot throwaways; contributors may use any language there.

## Options

### Option 1: Python

The team's strongest language. Rich ecosystem for LLM tooling (langchain,
anthropic SDK, etc.).

**Trade-offs:** Requires a runtime and dependency management (venv, pip,
version conflicts) on every consumer machine. Distribution as a single
artifact (PyInstaller, shiv) is possible but fragile. The "dependency mess"
is a recurring pain point for consumer-facing tools.

### Option 2: Go

Compiles to a single static binary. Well-supported cross-compilation. The
team has working proficiency — enough to review LLM-generated Go with
confidence, though not all members are Go experts.

**Trade-offs:** Weaker LLM tooling ecosystem compared to Python. Less familiarity across the team than Python. Verbose in places where
Python would be concise.

### Option 3: Rust

Single-binary distribution like Go, with stronger safety guarantees
(memory safety, type system).

**Trade-offs:** No team member has enough Rust experience to confidently vet
LLM-generated code. In a system that is still unstable and heavily
LLM-assisted, the inability to catch subtle Rust-specific bugs is a safety
risk that outweighs the language's safety properties.

### Option 4: Hybrid (Python → Go translation)

Write in Python, auto-translate to Go via a CI check on commit.

**Trade-offs:** Appealing in theory. Unproven in practice at the scale of a
real project. Translation fidelity, debugging translated code, and
maintaining two representations add complexity the team cannot absorb now.

## Decision

Go is the implementation language for fullsend's core tooling (CLI, installer,
entry-point dispatcher, and supporting libraries).

The decision is **explicitly provisional.** The team chose Go because it
satisfies distribution and reviewability constraints today, not because it is
the ideal long-term choice. If the team's Rust proficiency grows, or if a
hybrid workflow proves viable, this ADR may be superseded.

Experiments in the [experiments repository](https://github.com/fullsend-ai/experiments)
remain language-unconstrained. Contributors may prototype in Python or any other language;
only code that ships as part of the core tooling must be in Go.

## Consequences

- Adopting organizations get a single binary with no runtime dependencies,
  reducing installation support burden.
- The team can review LLM-generated Go with reasonable confidence, keeping
  the human-in-the-loop review effective during early development.
- Python-heavy contributors face a learning curve; LLM assistance partially
  offsets this but does not eliminate the need for Go review skill.
- The LLM tooling ecosystem in Go is less mature than Python's, which may
  require writing more glue code or maintaining Go bindings.
- Revisiting this decision is expected — the "provisional" framing avoids
  lock-in while giving the team a concrete starting point.
