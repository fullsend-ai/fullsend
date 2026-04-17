# Triage Summary

**Title:** App crashes on task save in project with CSV-imported data

## Problem
The application crashes every time the user attempts to save any task edit in a project containing approximately 200 tasks, many of which were bulk-imported via CSV. A separate, smaller project (~30-40 tasks, no CSV import) saves without issue. The crash is 100% reproducible and is causing the reporter to lose work.

## Root Cause Hypothesis
The CSV import likely introduced malformed or edge-case data (e.g., special characters, excessively long field values, invalid enum values, or encoding issues) that causes a crash during the save/serialization path. The save operation may be loading or validating related tasks or project-level data, causing even non-imported tasks in the same project to trigger the crash. An alternative hypothesis is a project-size-related issue (200 vs 30-40 tasks), but the CSV import timing correlation makes data corruption more likely.

## Reproduction Steps
  1. Identify the reporter's affected project (the one with ~200 tasks including CSV-imported data)
  2. Open any task in that project for editing
  3. Make any change to any field
  4. Click Save
  5. Observe crash (should be 100% reproducible)

## Environment
Not specified — reproducible regardless of field edited. Reporter has multiple projects; only the project with CSV-imported data is affected.

## Severity: high

## Impact
User is completely blocked from editing any tasks in their primary project (~200 tasks). They are losing work on every save attempt and have an active deadline. Any user who has done a large CSV import may be similarly affected.

## Recommended Fix
1. Pull crash logs for this user's save attempts to identify the exact failure point (serialization, validation, database write, etc.). 2. Inspect the CSV-imported data in the affected project for malformed entries — look for special characters, encoding issues, overly long strings, null/invalid values in required fields. 3. Compare the data schema of the working project vs the broken one. 4. If the root cause is malformed data, fix the data and add validation to the CSV import pipeline to reject or sanitize bad input. 5. If the root cause is project size, investigate the save path for N+1 queries or memory issues with large task counts.

## Proposed Test Case
Import a CSV file containing edge-case data (special characters, very long strings, empty required fields, mixed encodings) into a project, then verify that all tasks in that project can be saved without crashing. Additionally, test saving in a project with 200+ tasks to rule out scale-related issues.

## Information Gaps
- Exact error message or stack trace (available from server crash logs)
- Browser and OS (unlikely to matter given the data-correlation evidence, but available from user-agent logs)
- Exact contents of the imported CSV file (inspectable from the database or import history)
- Whether other users who imported CSVs experience the same issue (queryable from logs)
