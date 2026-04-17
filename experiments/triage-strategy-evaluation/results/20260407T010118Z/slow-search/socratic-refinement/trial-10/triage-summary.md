# Triage Summary

**Title:** Full-text description search regression in v2.3: 10-15s response time with high CPU on large workspaces

## Problem
After updating from TaskFlow 2.2 to 2.3, searching across task descriptions takes 10-15 seconds and causes high CPU usage. Title-only searches remain fast. The issue is consistent across all description searches, not limited to specific projects or query patterns.

## Root Cause Hypothesis
The 2.2 → 2.3 update likely introduced a regression in the description search path — possibly removing or bypassing a full-text index, switching to unoptimized string matching, or disabling a query cache. The fact that title search is still fast while description search is slow (with high CPU) suggests the description search is now doing brute-force scanning across all description text rather than using an index. With ~5,000 tasks containing lengthy descriptions (meeting notes, etc.), this would explain both the latency and CPU spike.

## Reproduction Steps
  1. Set up a TaskFlow 2.3 workspace with ~5,000 tasks, many with lengthy descriptions (paragraphs of text)
  2. Perform a search that targets task descriptions (full-text search, not title-only)
  3. Observe response time of 10-15 seconds and elevated CPU usage
  4. Compare: perform a title-only search and observe it completes quickly
  5. Optionally: repeat on TaskFlow 2.2 with the same dataset to confirm the regression

## Environment
TaskFlow 2.3 (upgraded from 2.2), work laptop (specific OS not confirmed), workspace with ~5,000 tasks accumulated over 2 years, many tasks contain lengthy descriptions with pasted meeting notes

## Severity: high

## Impact
Users with large workspaces (thousands of tasks with substantial descriptions) experience unusable description search performance after upgrading to 2.3. This likely affects any long-term TaskFlow user who relies on full-text search. Title-only search users are unaffected.

## Recommended Fix
Diff the search implementation between 2.2 and 2.3, focusing on the description search code path. Look for: (1) removed or broken full-text index on the descriptions column/field, (2) changes to the search query that bypass indexing, (3) migration steps that may have failed to rebuild an index on upgrade, (4) any new feature (e.g., fuzzy matching, new tokenizer) that replaced an indexed lookup with a scan. Verify that the description search path uses an appropriate index and is not doing in-memory or row-by-row string matching.

## Proposed Test Case
Create a performance regression test: populate a test workspace with 5,000+ tasks with multi-paragraph descriptions, execute a description search, and assert response time is under 1 second (or an acceptable threshold). Include this in CI to prevent future regressions. Also add a comparison assertion that description search time scales sublinearly with task count.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to matter given this is a clear version regression)
- Whether the issue reproduces on other machines or is environment-specific (likely reproduces given the version correlation)
- Whether the 2.2→2.3 upgrade ran any database migrations that might have affected indexes
