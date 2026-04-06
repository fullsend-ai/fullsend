# Triage Summary

**Title:** Search across task descriptions regressed to ~10-15s after v2.3 upgrade (title search unaffected)

## Problem
After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds. Searching by task title remains fast. The user has approximately 5,000 tasks, many with long descriptions containing pasted meeting notes.

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in description search — most probably a dropped or missing full-text index on the task descriptions column, a change from indexed search to a sequential scan, or a query/ORM change that bypasses the existing index. The fact that title search is unaffected suggests the title index is intact while the description search path changed.

## Reproduction Steps
  1. Set up a TaskFlow instance with v2.3
  2. Populate with ~5,000 tasks, many with long multi-paragraph descriptions
  3. Perform a search that targets task descriptions (not just titles)
  4. Observe search takes 10-15 seconds
  5. Downgrade to v2.2 with the same dataset and confirm description search is fast

## Environment
TaskFlow v2.3, upgraded from v2.2. Running on a work laptop (OS and hardware details not specified). ~5,000 tasks with lengthy descriptions.

## Severity: high

## Impact
Any user with a non-trivial number of tasks who searches by description content experiences unusable search latency. Users with large datasets and long descriptions are most affected. Title-only search users are unaffected.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3, focusing on how description search is executed. Check for: (1) missing or dropped full-text index on the descriptions column, (2) query changes that bypass the index (e.g., switching to LIKE/ILIKE from a full-text search function), (3) ORM or migration changes that removed the index. Add or restore the appropriate index and verify the query plan uses it.

## Proposed Test Case
Create a performance test that populates a database with 5,000+ tasks with multi-paragraph descriptions, runs a description search, and asserts the query completes within an acceptable threshold (e.g., under 2 seconds). Run this test as part of the search module's regression suite.

## Information Gaps
- Exact database backend in use (SQLite, PostgreSQL, etc.) — may affect index strategy
- Whether the v2.3 changelog or migration scripts mention any search-related changes
- Laptop hardware specs and OS (unlikely to be root cause given the version correlation, but could be a compounding factor)
