# Triage Summary

**Title:** Crash on save when editing task list containing CSV-imported data

## Problem
The application crashes every time the user attempts to save edits to a task list that contains data imported from a CSV file. Newly created task lists save without issue. The user reports this started recently, though the CSV import happened some time ago and may not have been the immediate trigger.

## Root Cause Hypothesis
The CSV import likely introduced data that the save/serialization path cannot handle — possible causes include special characters, excessively long fields, malformed or unexpected data types, or a record count that exceeds a size/memory limit in the save operation. A recent app update may have introduced a regression in how such edge-case data is processed during save.

## Reproduction Steps
  1. Identify or obtain a task list that contains CSV-imported data (or import a CSV file into a new task list)
  2. Edit any task in that list
  3. Click Save
  4. Observe crash — the app terminates and unsaved work is lost

## Environment
Not specified — but the issue is data-dependent rather than environment-dependent, as confirmed by the isolation test (new lists save fine on the same setup)

## Severity: high

## Impact
User is completely unable to save edits to their primary task list, causing repeated data loss. This is a blocking issue for their daily workflow.

## Recommended Fix
Investigate the save/serialization code path for task lists. Compare the data in the crashing list against a working list to identify problematic records or fields. Add defensive handling (input sanitization, size limits, error boundaries) so malformed data causes a graceful error rather than a crash. Check recent commits to the save path for regressions in data handling.

## Proposed Test Case
Import a CSV file containing edge-case data (special characters, very long strings, empty fields, non-UTF-8 characters, large row counts) into a task list, then verify that saving edits succeeds or fails gracefully with a user-facing error message rather than crashing.

## Information Gaps
- Exact error message or stack trace from the crash
- Contents/structure of the CSV file that was imported
- Browser/platform details (though likely irrelevant given this is data-dependent)
- Exact app version and whether a recent update correlates with the onset
