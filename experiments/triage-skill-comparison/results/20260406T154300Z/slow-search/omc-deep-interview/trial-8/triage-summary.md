# Triage Summary

**Title:** Description search regression in v2.3: 10-15s latency with CPU spike on datasets with ~5,000 tasks

## Problem
After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds consistently and causes high CPU usage (fan spin-up). Title-only search remains instant. The user has ~5,000 tasks accumulated over 2 years, some with very long descriptions (copy-pasted meeting notes).

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in the description search path — most probably a dropped or changed full-text index on the description field, a switch from indexed search to brute-force substring scanning, or a new search algorithm that doesn't scale with large text bodies. The fact that title search is unaffected and the CPU spikes heavily suggests the app is doing an unoptimized sequential scan over all 5,000 description fields in-process rather than using a database index.

## Reproduction Steps
  1. Install TaskFlow v2.3
  2. Populate the database with ~5,000 tasks, including some with long descriptions (e.g., multi-paragraph text)
  3. Perform a search that targets task descriptions (not just titles)
  4. Observe 10-15 second latency and high CPU usage
  5. Compare with TaskFlow v2.2 on the same dataset to confirm regression

## Environment
TaskFlow v2.3, upgraded from v2.2. Work laptop (specific OS/specs not provided but likely not the bottleneck given v2.2 worked fine). ~5,000 tasks, some with very long description fields.

## Severity: high

## Impact
Any user with a moderately large task database (~5,000+ tasks) will experience unusable description search performance after upgrading to v2.3. Title search still works as a partial workaround, but description search is a core feature. Heavy CPU usage may also degrade other application responsiveness during searches.

## Recommended Fix
Diff the search implementation between v2.2 and v2.3 to identify what changed in the description search code path. Likely candidates: (1) check if a full-text index on the description column was dropped or altered in a migration, (2) check if the search query was changed from an indexed lookup to a LIKE/regex scan, (3) check if description content is now being loaded into memory and searched in-process rather than at the database level. Restore indexed search behavior and verify with a 5,000+ task dataset.

## Proposed Test Case
Performance regression test: populate a test database with 5,000 tasks (including 100+ tasks with descriptions over 1,000 characters). Run a description search and assert that results return in under 1 second. Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to matter given v2.2 worked fine)
- Whether the v2.3 changelog mentions any search-related changes
- Whether the database backend is SQLite, PostgreSQL, or another engine (affects index investigation)
