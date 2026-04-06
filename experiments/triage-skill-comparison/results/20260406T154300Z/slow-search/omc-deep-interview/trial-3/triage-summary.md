# Triage Summary

**Title:** Search across task descriptions regressed to 10-15s in v2.3 (was <1s in v2.2)

## Problem
After upgrading from TaskFlow v2.2 to v2.3, searching across task descriptions takes 10-15 seconds regardless of query specificity. Title-only search remains fast (<1s). The user has ~5,000 tasks accumulated over 2 years, some with lengthy descriptions (pasted meeting notes). The regression appeared immediately after the v2.3 upgrade approximately 2 weeks ago.

## Root Cause Hypothesis
The v2.3 release likely introduced a change to how description search is performed — most probably a missing or dropped database index on the descriptions column, a switch from indexed/FTS lookup to a full table scan, or a newly added processing step (e.g., parsing, sanitization, or ranking) that runs per-row during description search. The fact that title search is unaffected suggests the two search paths diverge and only the description path was modified.

## Reproduction Steps
  1. Install TaskFlow v2.3
  2. Seed the workspace with ~5,000 tasks, ensuring a meaningful subset have long descriptions (>500 words, simulating pasted meeting notes)
  3. Perform a search with scope set to include task descriptions
  4. Observe query latency (expected: 10-15 seconds)
  5. Compare against a title-only search on the same dataset (expected: <1 second)
  6. Optionally repeat on v2.2 with the same dataset to confirm the regression

## Environment
TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks with some lengthy descriptions, running on a work laptop (OS and hardware specs not specified but likely not the bottleneck given v2.2 was fast on the same machine)

## Severity: medium

## Impact
Any user with a substantial task count who searches across descriptions will experience multi-second delays. Power users with large workspaces (accumulated tasks, long descriptions) are most affected. Title-only search is a partial workaround but forces users to change their workflow.

## Recommended Fix
1. Diff the search query path between v2.2 and v2.3, focusing on the description search branch. 2. Check for dropped or missing indexes on the task descriptions table/column. 3. Profile the description search query with EXPLAIN/ANALYZE on a dataset of ~5,000 tasks with long descriptions. 4. If an index was removed, restore it; if a new processing step was added, ensure it runs post-query rather than per-row, or add appropriate indexing (e.g., full-text search index).

## Proposed Test Case
Performance regression test: seed a test database with 5,000 tasks (descriptions averaging 500+ words). Assert that a full-text search across descriptions returns results in under 2 seconds. Run this test against both v2.2 and v2.3 code paths to catch regressions.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be the root cause given the v2.2 baseline)
- Whether other v2.3 users with large workspaces also experience the slowdown (likely yes, but unconfirmed)
- The specific v2.3 changelog entries related to search or database schema changes
