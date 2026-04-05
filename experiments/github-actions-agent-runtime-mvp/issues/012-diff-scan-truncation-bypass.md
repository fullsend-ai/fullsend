# 012: Prompt Injection Scan Truncates Diff at 1000 Lines

## Problem

The review agent truncates the PR diff to 1000 lines before sending it to Model Armor for prompt injection scanning, but passes the full diff to the LLM. An attacker can place injection payload past line 1000 to bypass the scan while still being processed by the agent.

## Impact

Deliberate evasion — attacker pads the PR with 1000+ lines of legitimate changes, then appends injection payload in a later file or hunk.

## Proposed Fix

1. **Scan the full diff** — remove the 1000-line truncation, or scan in chunks
2. **Scan per-file** — split the diff by file and scan each file's hunks individually
3. **Accept the risk** — document the limitation and rely on the agent's inherent injection resistance as a secondary defense
