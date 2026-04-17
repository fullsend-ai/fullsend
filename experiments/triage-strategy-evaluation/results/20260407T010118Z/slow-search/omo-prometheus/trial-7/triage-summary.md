# Triage Summary

**Title:** Regression in 2.3: full-text search on task descriptions is 10-15x slower than expected

## Problem
After upgrading from TaskFlow 2.2 to 2.3, searching by task description content takes 10-15 seconds to return results. Title-based search remains fast. The slowdown is consistent across searches and does not improve with repeated queries (ruling out cold-cache effects).

## Root Cause Hypothesis
The 2.3 release likely changed the description search implementation — most probably dropped or failed to migrate a full-text index on the description field, or switched from indexed search to a naive LIKE/substring scan. With ~5,000 tasks and some descriptions containing thousands of words, an unindexed scan would explain the 10-15 second latency while title search (shorter field, likely still indexed) remains fast.

## Reproduction Steps
  1. Have a TaskFlow 2.3 instance with a large dataset (~5,000 tasks, some with multi-paragraph descriptions)
  2. Perform a search using a term that appears in task descriptions (not just titles)
  3. Observe search taking 10-15 seconds to return results
  4. Perform the same search scoped to titles only — observe it returns quickly
  5. Optionally: repeat on a TaskFlow 2.2 instance with the same dataset to confirm regression

## Environment
TaskFlow 2.3 (upgraded from 2.2), work laptop (OS unspecified), ~5,000 tasks accumulated over 2 years, some descriptions contain pasted meeting notes (~2,000+ words)

## Severity: medium

## Impact
Users with large task databases who rely on description search experience significant delays. Search is functional but painfully slow. Title search remains usable as a partial workaround. Likely affects all users with non-trivial dataset sizes on 2.3.

## Recommended Fix
Diff the search implementation between 2.2 and 2.3, focusing on how description content is queried. Check for: (1) dropped or altered full-text index on the description column, (2) migration that failed to rebuild the index, (3) query change from indexed full-text search to unindexed LIKE/ILIKE scan. Verify the index exists in a 2.3 database and compare query plans (EXPLAIN ANALYZE) for description vs. title search.

## Proposed Test Case
Create a test database with 5,000+ tasks where at least 500 have descriptions over 1,000 words. Run a description search query and assert it completes in under 1 second. Run the same test against both 2.2 and 2.3 schemas to verify the regression and confirm the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the title-vs-description split)
- Whether the 2.3 release notes mention any search or database migration changes
- Whether the slowdown scales with search term frequency (common vs. rare words)
