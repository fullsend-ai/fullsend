# Triage Summary

**Title:** Search slow (10-15s) when query matches task description/notes but fast for title matches — regression since ~v2.3

## Problem
Keyword searches that match content in task descriptions or notes take 10-15 seconds to return results, while searches matching task titles remain fast. The reporter has ~5,000 tasks, many with large descriptions containing pasted meeting notes. Performance was consistently fast before approximately 2 weeks ago, correlating with an update to v2.3.

## Root Cause Hypothesis
The v2.3 update likely changed search behavior to include task body/description fields without proper indexing, or dropped/broke an existing full-text index on those fields. The large volume of text in descriptions (pasted meeting notes) makes unindexed scans expensive, while title fields — shorter and possibly still indexed — remain fast.

## Reproduction Steps
  1. Create or use an account with a large number of tasks (~5,000), many containing lengthy descriptions
  2. Search for a keyword that appears only in a task title — observe fast results
  3. Search for a keyword that appears only in task descriptions or notes — observe 10-15 second delay
  4. Compare query execution plans or logs for both searches

## Environment
TaskFlow ~v2.3, running on a work laptop (OS and platform not specified), ~5,000 tasks accumulated over 2 years, many with large description fields

## Severity: medium

## Impact
Users with large task histories experience severe search slowdowns when queries need to match against task body content, making full-text search effectively unusable for its primary purpose. Users with fewer or shorter tasks may not notice.

## Recommended Fix
1. Review v2.3 changelog for changes to search scope or query logic. 2. Check whether full-text indexes exist on task description/notes fields — add or rebuild them if missing. 3. If search was expanded to include body fields in v2.3, ensure those fields are indexed before being included in queries. 4. Consider adding pagination or query-time limits to prevent long scans.

## Proposed Test Case
Create a dataset of 5,000+ tasks with varying description lengths (some with multi-KB pasted text). Benchmark search for a term appearing only in titles vs. a term appearing only in descriptions. Both should return results in under 1 second. Repeat after simulating a v2.2-to-v2.3 migration to verify no index regression.

## Information Gaps
- Exact TaskFlow version not confirmed (reporter said 'I think' v2.3)
- Platform (web app vs desktop) and database backend not identified
- Whether the reporter has any search filters or settings that might affect scope
