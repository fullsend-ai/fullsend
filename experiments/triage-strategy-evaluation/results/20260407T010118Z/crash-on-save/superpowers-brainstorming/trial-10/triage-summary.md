# Triage Summary

**Title:** Save crashes when task list contains CSV-imported tasks

## Problem
The application crashes every time the user saves their task list after importing ~200 tasks from a CSV file. Removing all imported tasks restores normal save functionality. The user needs the imported tasks and cannot work around the issue.

## Root Cause Hypothesis
The CSV import likely produces task records with data that the save/serialization process cannot handle — for example, special characters, encoding mismatches, fields exceeding expected lengths, or missing/malformed fields that manually-created tasks always have. The import may succeed in loading the data into memory but produce objects that fail validation or serialization on save.

## Reproduction Steps
  1. Create or use a task list with a small number of manually-created tasks
  2. Verify that saving works normally
  3. Import a CSV file containing ~200 tasks
  4. Attempt to save the task list
  5. Observe the crash

## Environment
Not specified — issue appears to be data-dependent rather than environment-dependent

## Severity: high

## Impact
Users who import tasks via CSV are unable to save their work at all, leading to data loss. The reporter has an upcoming deadline and is completely blocked.

## Recommended Fix
Investigate the difference between CSV-imported task objects and manually-created ones at save time. Check the serialization/persistence layer for: (1) unescaped special characters, (2) encoding issues (e.g., non-UTF8 from CSV), (3) missing required fields that the CSV importer doesn't populate, (4) field length or type mismatches. Add defensive validation in the CSV importer to normalize data to match the schema expected by the save path. Consider adding a try/catch in the save path that identifies the offending record rather than crashing.

## Proposed Test Case
Import a CSV containing tasks with varied edge-case data (special characters, long names, empty fields, unicode) and verify that save completes successfully. Also test with 200+ imported tasks to rule out a pure volume/memory issue.

## Information Gaps
- No error message or stack trace from the crash
- Exact contents/format of the CSV file used
- Whether removing only *some* imported tasks (rather than all) would allow saving — could help distinguish a single bad record from a volume issue
- Platform and application version
