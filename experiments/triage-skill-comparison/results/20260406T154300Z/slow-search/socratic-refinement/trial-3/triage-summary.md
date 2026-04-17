# Triage Summary

**Title:** Search on task descriptions regressed to 10-15s after v2.3 upgrade (title search unaffected)

## Problem
After upgrading from TaskFlow v2.2 to v2.3, searching across task descriptions takes 10-15 seconds, whereas it previously completed in under a second. Searching by task title remains fast. The reporter has tasks with lengthy descriptions (multi-thousand-word meeting notes). Search results are accurate — only latency is affected.

## Root Cause Hypothesis
The v2.3 release likely introduced a change to description search — possible causes include a dropped or missing full-text index on the description field, a switch from indexed search to naive substring/regex scanning, or a new search code path that doesn't handle large text fields efficiently. The fact that title search is unaffected suggests the regression is isolated to the description search path.

## Reproduction Steps
  1. Install TaskFlow v2.3
  2. Create or import tasks with lengthy descriptions (2000+ words each) — at least 50-100 tasks to approximate a realistic dataset
  3. Perform a search query that matches content in task descriptions
  4. Observe response time (expected: 10-15 seconds, should be under 1 second)
  5. Perform the same search targeting task titles only and confirm it completes quickly
  6. Optionally repeat on v2.2 with the same dataset to confirm the regression

## Environment
TaskFlow v2.3 (upgraded from v2.2), running on a work laptop (OS and specs not specified)

## Severity: medium

## Impact
Users who search across task descriptions experience 10-15 second delays, significantly disrupting workflow. Users with large/verbose task descriptions are disproportionately affected. Title-only search users are unaffected.

## Recommended Fix
Review the v2.3 changelog and diff for changes to the search subsystem, particularly the description search path. Check whether a full-text index on the description field was dropped or altered during a migration. If the search implementation changed (e.g., from database-level full-text search to application-level scanning), restore indexed search or add appropriate indexing. Profile the description search query against a dataset with large description fields to identify the bottleneck.

## Proposed Test Case
Create a test dataset with 100+ tasks, some containing descriptions of 2000+ words. Execute a description search and assert that results return within an acceptable threshold (e.g., under 2 seconds). Run this test against both v2.2 and v2.3 code paths to validate the regression and confirm the fix.

## Information Gaps
- Exact number of tasks in the reporter's dataset
- Database backend in use (SQLite, PostgreSQL, etc.)
- Whether the v2.3 release notes mention any search-related changes
- Hardware specs of the work laptop (CPU, RAM, disk type)
