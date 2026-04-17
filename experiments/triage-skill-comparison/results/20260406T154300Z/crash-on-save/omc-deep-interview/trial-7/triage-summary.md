# Triage Summary

**Title:** App crashes on save when task list contains special characters imported from CSV

## Problem
The application terminates abruptly (no freeze, immediate window close) when the user saves a task list that contains tasks imported from a CSV file. A dialog box briefly flashes mentioning 'encoding' before the app disappears. The crash does not occur when saving only manually-created tasks with plain ASCII text. Removing the imported tasks restores normal save behavior.

## Root Cause Hypothesis
The save/serialization path fails on characters outside the basic ASCII range — specifically typographic characters like curly quotes (“”) and em-dashes (—) that originated from a spreadsheet-exported CSV. The encoding dialog flash suggests the serializer encounters a character it cannot encode (likely attempting to write as ASCII or a narrow encoding rather than UTF-8), throws an unhandled exception, and the app crashes instead of surfacing the error.

## Reproduction Steps
  1. Prepare a CSV file containing tasks with special characters such as curly quotes (“”), em-dashes (—), or other non-ASCII typographic characters (e.g., export from Excel or Google Sheets)
  2. Import the CSV into TaskFlow (approximately 200 tasks to match reporter's scenario, though a smaller set with special characters likely suffices)
  3. Click Save
  4. Observe: app closes immediately with a brief 'encoding' dialog flash

## Environment
Not yet confirmed — reporter did not specify OS, app version, or platform. The CSV originated from a colleague's spreadsheet (likely Excel or Google Sheets export with Windows-1252 or UTF-8 encoding).

## Severity: high

## Impact
Any user who imports CSV data containing non-ASCII characters (common in spreadsheet exports) will lose unsaved work and be unable to save until the offending tasks are removed. Workaround exists (delete imported tasks) but defeats the purpose of the import feature.

## Recommended Fix
Investigate the save/serialization code path for encoding handling. Ensure the serializer writes UTF-8 (or the app's declared encoding) and gracefully handles characters outside the expected range. Specifically: (1) check file write calls for hardcoded ASCII or narrow encoding, (2) wrap the save operation in proper error handling so a encoding failure shows a user-facing error rather than crashing, (3) consider sanitizing or transcoding imported CSV data at import time to normalize encoding.

## Proposed Test Case
Create a task list containing strings with curly quotes (“”‘’), em-dashes (—), and other common non-ASCII characters (accented letters, emoji). Verify that saving succeeds without error and that the characters round-trip correctly when the file is reloaded. Additionally, test with a 200+ task import from a CSV encoded in Windows-1252 to cover the reporter's exact scenario.

## Information Gaps
- Exact OS and app version not confirmed
- Unknown whether the CSV file itself was UTF-8 or Windows-1252 encoded
- Unclear whether the crash requires a large number of tasks or if a single task with curly quotes is sufficient to trigger it
- No crash log or full error dialog text available
