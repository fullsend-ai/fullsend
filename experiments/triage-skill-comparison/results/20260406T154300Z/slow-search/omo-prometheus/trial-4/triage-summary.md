# Triage Summary

**Title:** Description search regression in v2.3: 10-15s response time with ~5,000 tasks

## Problem
After upgrading to TaskFlow v2.3, searching across task descriptions takes 10-15 seconds. Title search remains fast. The user has approximately 5,000 tasks accumulated over 2 years, some containing long descriptions (pasted meeting notes).

## Root Cause Hypothesis
The v2.3 release likely introduced a regression in how description search is executed — most probably a missing or broken full-text index on the task descriptions column/field. Title search remaining fast suggests title indexing is intact, while description search may have fallen back to a sequential scan or unindexed LIKE/regex query. The presence of long description text (meeting notes) would amplify the cost of any unindexed scan.

## Reproduction Steps
  1. Have a TaskFlow instance running v2.3 with a substantial number of tasks (~5,000)
  2. Ensure some tasks have lengthy descriptions (e.g., pasted meeting notes)
  3. Perform a search using a term that would match task descriptions (not just titles)
  4. Observe that results take 10-15 seconds to return
  5. Compare with a title-only search, which should return quickly

## Environment
TaskFlow v2.3, ~5,000 tasks, work laptop (specific OS/specs unknown but not likely relevant given this is a regression)

## Severity: medium

## Impact
Users with large task databases experience severely degraded description search performance after upgrading to v2.3. Title search still works as a partial workaround, but users who rely on searching within descriptions (especially those who store detailed notes in tasks) are significantly impacted.

## Recommended Fix
1. Diff the search implementation between v2.2 and v2.3 — look for changes to query construction, indexing, or ORM usage for description search. 2. Check whether a full-text index on the descriptions field was dropped or altered in the v2.3 migration. 3. Profile the description search query against a dataset of ~5,000 tasks to confirm the bottleneck. 4. Restore or add proper indexing for description search and verify performance returns to pre-v2.3 levels.

## Proposed Test Case
Performance test: seed a database with 5,000 tasks (some with descriptions >1,000 characters), execute a description search, and assert results return in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the fix restores expected performance.

## Information Gaps
- Exact OS and hardware specs of the reporter's laptop (unlikely to be relevant since this is a version regression)
- Whether the database backend is SQLite, PostgreSQL, or another store (would inform indexing strategy)
- Whether the v2.3 release notes mention any search-related changes
