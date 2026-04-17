# Triage Summary

**Title:** Search performance regression after upgrading from v2.2 to v2.3 (~10-15s response times)

## Problem
Search queries that were previously fast (~instant) now take 10-15 seconds to return results. The slowdown began immediately after upgrading from TaskFlow v2.2 to v2.3, with no other changes to data, configuration, or environment.

## Root Cause Hypothesis
The v2.3 release likely introduced a search regression — possible causes include a changed query strategy (e.g., switching from indexed to full-table scan), a missing or dropped search index during migration, a new search feature (e.g., full-text or fuzzy matching) that scales poorly, or an unintended change to query construction that bypasses previously-used optimizations.

## Reproduction Steps
  1. Set up a TaskFlow instance with approximately 5,000 tasks (or use a dataset seed that generates this volume)
  2. Run search queries on TaskFlow v2.2 and note response times
  3. Upgrade the instance to v2.3 using the standard upgrade path
  4. Run the same search queries and observe the degraded response times (~10-15 seconds)

## Environment
TaskFlow v2.3 (upgraded from v2.2), work laptop (OS/specs not specified), ~5,000 tasks accumulated over 2 years

## Severity: high

## Impact
Search is a core workflow feature. A 10-15 second delay on every search degrades the experience for any user with a non-trivial number of tasks. Likely affects all v2.3 users with large task collections.

## Recommended Fix
Diff all search-related code between v2.2 and v2.3 — query construction, indexing, and any new search features. Check the v2.3 database migration for dropped or altered indexes on task-searchable fields. Profile the search query on a 5,000-task dataset to identify whether the bottleneck is in the database query, application-layer filtering, or result serialization. If v2.3 introduced a new search mode (e.g., fuzzy matching), verify it uses appropriate indexing or consider making it opt-in.

## Proposed Test Case
Performance regression test: seed a database with 5,000 tasks, execute a representative search query, and assert that results are returned within an acceptable threshold (e.g., under 1 second). Run this test against both v2.2 and v2.3 code paths to confirm the regression and validate the fix.

## Information Gaps
- Whether all search queries are slow or only certain query types (e.g., broad vs. specific terms, filtered vs. unfiltered)
- Exact laptop specs and OS (could rule out hardware/platform-specific issues, though unlikely given the version correlation)
- Whether the reporter is using any TaskFlow plugins or integrations that interact with search
- Server-side vs. client-side — whether TaskFlow is running locally or against a remote backend
