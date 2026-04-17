# Triage Summary

**Title:** Search on task descriptions extremely slow (~10-15s) since v2.3 upgrade; title search unaffected

## Problem
After upgrading from TaskFlow 2.2 to 2.3, searching from the main task list search bar takes 10-15 seconds when the matching text is in task descriptions. Searches matching task titles return quickly. The user has approximately 5,000 tasks.

## Root Cause Hypothesis
The v2.3 upgrade likely introduced a regression in how description searches are executed — most probably a missing or dropped database index on the task descriptions column, or a change from indexed search to unoptimized full-text scanning. The fact that title searches remain fast suggests the title field still has proper indexing while the description field does not.

## Reproduction Steps
  1. Have a TaskFlow instance running v2.3 with a large number of tasks (~5,000)
  2. Open the main task list view
  3. Use the search bar at the top to search for a keyword known to exist in a task description (not in any task title)
  4. Observe that results take 10-15 seconds to appear
  5. Search for a keyword known to exist in a task title and observe that results return quickly

## Environment
Ubuntu 22.04, ThinkPad T14, TaskFlow 2.3 (desktop app), upgraded from v2.2 approximately two weeks ago

## Severity: medium

## Impact
Users with large task counts experience significant delays when searching task descriptions, degrading daily usability of the search feature. Title-only searches are unaffected, so users have a partial workaround.

## Recommended Fix
Compare the search query execution path between v2.2 and v2.3, focusing on how description fields are queried. Check for missing or dropped indexes on the task descriptions column. Review the v2.3 migration scripts for any schema changes affecting the descriptions table or full-text search configuration. Profile the description search query against a ~5,000 task dataset to confirm the bottleneck.

## Proposed Test Case
Create a dataset with 5,000+ tasks. Run a search for a keyword that appears only in task descriptions and assert that results return within an acceptable threshold (e.g., under 2 seconds). Run the same test for a title-only keyword and verify comparable performance.

## Information Gaps
- Exact timing of when the slowness started relative to the v2.3 upgrade (reporter was not 100% certain)
- Whether the issue occurs on other platforms or is specific to the Linux desktop app
- No error messages or logs were provided, though the reporter did not mention any errors appearing
