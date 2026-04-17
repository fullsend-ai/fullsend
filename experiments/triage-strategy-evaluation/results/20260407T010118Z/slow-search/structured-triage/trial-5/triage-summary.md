# Triage Summary

**Title:** Full-text search on task descriptions extremely slow (~10-15s) with large dataset since v2.3

## Problem
Keyword searches that match task descriptions take 10-15 seconds to return results for a user with ~5,000 tasks. Searches that match task titles return quickly. The slowness appears to have started around the upgrade from v2.2 to v2.3.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in description search — possibly a missing or dropped full-text index on the task description field, a change in the search query path (e.g., switching from indexed search to unindexed LIKE/scan), or a new description-search feature that performs poorly at scale.

## Reproduction Steps
  1. Create or use an account with a large number of tasks (~5,000)
  2. Enter a keyword in the main search bar that is known to appear in task descriptions but not in task titles
  3. Observe that results take 10-15 seconds to return
  4. Repeat with a keyword that matches a task title and observe fast results

## Environment
Ubuntu 22.04, ThinkPad T14, TaskFlow desktop app v2.3 (upgraded from v2.2 ~2 weeks ago)

## Severity: medium

## Impact
Users with large task collections (~5,000+) experience severe search degradation when searching task descriptions. Title-only searches are unaffected. This impacts long-term power users who rely on description search for daily workflows.

## Recommended Fix
Compare the search query execution path for descriptions between v2.2 and v2.3. Check whether a full-text index on the description field was dropped or altered during the upgrade. Profile the description search query against a 5,000-task dataset to identify the bottleneck. If an index was removed, restore it; if the query strategy changed, revert or optimize it.

## Proposed Test Case
Performance test: with a seeded database of 5,000+ tasks, assert that a keyword search matching only task descriptions returns results in under 2 seconds. Include a regression test that verifies the description search index exists after schema migrations.

## Information Gaps
- Exact version of v2.3 (patch level) if multiple patches exist
- Whether the reporter can confirm the issue did not exist on v2.2 by downgrading
- Application logs or query timing data that could confirm the bottleneck
