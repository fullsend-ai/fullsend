# Triage Summary

**Title:** Description-field search is extremely slow (~10-15s) with large task counts since v2.3 upgrade

## Problem
Searching for keywords that appear in task descriptions takes 10-15 seconds, while searching for keywords in task titles returns results quickly. The reporter has ~5,000 tasks accumulated over 2 years. The slowness may have started after upgrading from v2.2 to v2.3 approximately two weeks ago.

## Root Cause Hypothesis
The v2.3 upgrade likely changed or regressed the description search path — possibly a missing or dropped index on the task description column, a change from indexed search to full table scan for description text, or a switch in search implementation (e.g., removing full-text indexing) that only affects the description field. Title search likely still hits an indexed column, explaining the performance disparity.

## Reproduction Steps
  1. Create or use an account with ~5,000 tasks, many containing descriptive text (e.g., pasted meeting notes)
  2. Search for a keyword or phrase that appears in task descriptions but not in task titles
  3. Observe that results take 10-15 seconds to return
  4. Search for a keyword that appears in a task title
  5. Observe that title-match results return quickly

## Environment
Ubuntu 22.04, ThinkPad T14, TaskFlow desktop app v2.3 (upgraded from v2.2 ~2 weeks ago)

## Severity: high

## Impact
Users with large task histories experience unusable search performance for description-text queries — a core workflow. Likely affects all users with substantial task counts on v2.3.

## Recommended Fix
Diff the search query path and database schema between v2.2 and v2.3. Check whether a full-text index on the task description column was dropped or altered during the upgrade. Profile the description search query on a dataset of ~5,000 tasks to confirm whether it's doing a full table/collection scan. Restore or add appropriate indexing for description-field search.

## Proposed Test Case
With a dataset of 5,000+ tasks containing varied description text, assert that a keyword search matching description content returns results in under 2 seconds (matching title search performance).

## Information Gaps
- Whether the slowness definitively started with the v2.3 upgrade or was a gradual degradation
- Whether any error messages or debug logs are produced during slow searches
- Exact query execution time (reporter estimates 10-15s but no precise measurement)
