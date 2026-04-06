# Triage Summary

**Title:** Search regression in v2.3: description-field searches take 10-15s with large task corpus

## Problem
After upgrading from v2.2 to v2.3, searching for text that appears in task descriptions takes 10-15 seconds, whereas title-based searches remain near-instant. The user has ~5,000 tasks accumulated over 2 years, some with descriptions exceeding 2,000 words. Prior to v2.3, all searches completed in under a second.

## Root Cause Hypothesis
The v2.3 update likely introduced a change to how task description text is indexed or queried. Probable causes in order of likelihood: (1) a full-text search index on the description field was dropped or not migrated during the v2.3 upgrade, forcing a sequential scan; (2) a query planner change in v2.3 causes the description search path to bypass an existing index; (3) a new search implementation (e.g., switching from indexed lookup to regex/LIKE matching) was introduced for description fields without appropriate indexing for large text bodies.

## Reproduction Steps
  1. Create or seed a TaskFlow instance with ~5,000 tasks, including a subset (50+) with descriptions over 1,000 words
  2. Run TaskFlow v2.2 and perform a search for a phrase known to exist in a task description — confirm it returns in under 1 second
  3. Upgrade to TaskFlow v2.3 (following the standard upgrade path)
  4. Repeat the same description-text search and observe response time (expected: 10-15 seconds)
  5. Search for the same task by its title and observe response time (expected: near-instant)
  6. Compare query plans or database logs between the two searches to identify the divergence

## Environment
TaskFlow v2.3 (upgraded from v2.2), ~5,000 tasks, some with 2,000+ word descriptions, running on a work laptop (OS and specs not specified but not likely relevant given the regression is version-correlated)

## Severity: high

## Impact
Any user with a substantial task corpus who searches by description content will experience severe performance degradation after upgrading to v2.3. This is a regression from previously working functionality and affects core search usability.

## Recommended Fix
1. Diff the v2.2 and v2.3 database migrations and search query logic for changes to the description field indexing or query path. 2. Check whether a full-text index on the description column exists post-upgrade; if dropped, restore it. 3. If the search implementation changed (e.g., to support richer queries), ensure the new implementation uses appropriate indexing for large text fields. 4. Consider adding query-time logging or EXPLAIN output to the search endpoint to surface slow queries proactively.

## Proposed Test Case
Performance test: with a database seeded with 5,000 tasks (100 having descriptions > 1,000 words), assert that a description-text search returns results in under 2 seconds. Run this test against both the title and description search paths to ensure parity.

## Information Gaps
- Exact database backend (SQLite, PostgreSQL, etc.) — may affect index behavior
- Whether filters compound the slowness (reporter doesn't use filters regularly)
- Whether the issue affects other users or is dataset-size-dependent with a specific threshold
- Server-side vs. client-side search architecture details
