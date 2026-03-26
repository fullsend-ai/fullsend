---
title: "0004. LLM-as-judge evaluation framework for skills"
status: Accepted
relates_to:
  - testing-agents
  - performance-verification
topics:
  - skills
  - evals
  - testing
---

# 0004. LLM-as-judge evaluation framework for skills

Date: 2026-03-26

## Status

Accepted

## Context

Skills are natural-language instruction files that shape agent behavior. When a
skill is modified, we need to know whether the change improved or degraded agent
performance. Traditional assertion-based tests cannot evaluate free-form agent
output, so we need a grading approach that works with natural language
(see [testing-agents.md](../problems/testing-agents.md),
[performance-verification.md](../problems/performance-verification.md)).

## Decision

We adopt an LLM-as-judge evaluation framework with mutation testing for prompt
robustness. The framework lives in `hack/evals/` and skill-specific eval
definitions live alongside each skill in `skills/<skill>/evals.yaml`.

**Grading.** A judge LLM scores each agent response against an expected-behavior
description, returning a YAML verdict (`pass`/`fail`) with evidence. No regex or
pattern matching is used — the judge evaluates semantic alignment.

**Mutation testing.** Each eval prompt is rephrased into multiple variants by an
LLM mutator. All variants (original + mutations) are run and graded. This tests
that the skill works for diverse phrasings, not just the exact seed prompt.
Mutations are cached to disk and invalidated when the skill or eval definition
changes.

**Delta comparison.** Every variant is run twice — once with the skill loaded and
once without. The skill must improve (or at least not degrade) the pass rate.
This ensures the skill itself is the cause of correct behavior.

**Multi-agent.** Evals run against both Claude Code and OpenCode to verify skills
are agent-agnostic. Model selection for mutation, running, and judging is
configured per-skill in `evals.yaml`.

## Consequences

- Skill changes get regression-tested in CI before merging.
- Mutation testing catches prompt fragility that single-example tests would miss.
- LLM-as-judge grading introduces non-determinism; a configurable pass threshold
  (default 0.9) accommodates this.
- Eval runs are slow (multiple LLM calls per variant); caching mutations and
  running only changed skills in CI mitigates cost.
- Adding a new eval requires only a YAML entry — no code changes needed.
