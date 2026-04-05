# 001: Fix Agent Cycle Time (7-21 min)

## Problem

Fix agent takes 15-21 min per review-fix cycle. Telemetry shows ~70% is shell command execution (`make test`, `make lint`, `git`, `gh api`), only ~30% is LLM inference.

## Data ([PR #7](https://github.com/nonflux/integration-service/pull/7))

| Metric | Run A (~17 min) | Run B (~21 min) |
|--------|-----------------|-----------------|
| LLM API time | 284s (29%) | 342s (29%) |
| Tool execution (shell) | 690s (71%) | 833s (70%) |
| Total tokens | 1.49M (89% cached) | 2.38M (91% cached) |

## Optimization Targets

1. **Targeted testing** — run only tests for changed packages instead of full `make test`
2. **Pre-fetch PR context** — fetch review comments in workflow step, not via LLM tool calls
3. **Time budget** — add `timeout-minutes: 20` on the LLM step
4. **Skip tests for non-code changes** — workflow-only or markdown-only PRs don't need `make test`
