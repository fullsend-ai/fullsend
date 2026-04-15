# Variant Definitions

This directory contains only **V8-hybrid** — the variant proposed in
[PR #189](https://github.com/fullsend-ai/fullsend/pull/189).

All other variants (V1–V7) were used during the evaluation but are
not included in this PR to keep the diff reviewable. They are published at:

**[ascerra/code-agent-eval-scenarios — variants/](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants)**

## Variant inventory

| ID | Name | Browse | Description |
|----|------|--------|-------------|
| V1 | fullsend-single-skill | [view](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants/V1-fullsend-single-skill) | PR #189 original — agent + single skill + scan-secrets |
| V2 | fullsend-multi-skill | [view](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants/V2-fullsend-multi-skill) | PR #189 with skill split into 4 pieces |
| V3 | vanilla-claude | [view](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants/V3-vanilla-claude) | No guardrails baseline — just a prompt |
| V4 | claudemd-only | [view](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants/V4-claudemd-only) | CLAUDE.md instructions only — stopped early |
| V5 | apex | [view](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants/V5-apex) | Enhanced V1 with reasoning protocol, self-review, minimal diff |
| V6 | apex-github | [view](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants/V6-apex-github) | V5 hardcoded for GitHub (rigidity hurts) |
| V7 | ultimate | [view](https://github.com/ascerra/code-agent-eval-scenarios/tree/main/variants/V7-ultimate) | V5 + "understand before you act" + reproduction step |
| **V8** | **hybrid** | [here](./V8-hybrid/) | **Cleaned V1 + V5 minimal-diff + V7 reproduction/task-type** |
