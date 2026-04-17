# Triage Summary

**Title:** Manual save crashes with encoding error after CSV task import (~200 tasks)

## Problem
Clicking the Save button in the toolbar causes an immediate crash (app closes) when the task list contains tasks imported from a CSV file. A dialog briefly flashes mentioning 'encoding' before the app dies. Auto-save continues to work correctly, indicating the manual save code path has a distinct encoding handling issue.

## Root Cause Hypothesis
Manual save and auto-save use different serialization or file-writing code paths. The manual save path likely performs full serialization of all task data and encounters characters from the CSV import that cause an unhandled encoding error (e.g., non-UTF-8 characters, BOM markers, or special characters from a differently-encoded CSV). The encoding exception is uncaught and crashes the application. Auto-save may use incremental writes, a different serializer, or a more lenient encoding mode that tolerates the same data.

## Reproduction Steps
  1. Create or open a task list in TaskFlow
  2. Import tasks from a CSV file (try various encodings: Latin-1, Windows-1252, UTF-8 with BOM, or a file containing non-ASCII characters)
  3. Ensure the list has a substantial number of tasks (~200)
  4. Click the Save button in the toolbar
  5. Observe: app crashes immediately with a brief encoding-related dialog

## Environment
Not specified — appears to affect the desktop/electron app. OS and version not yet confirmed.

## Severity: high

## Impact
Users who import tasks from CSV files lose all unsaved work when they attempt a manual save. The crash is deterministic and repeatable, effectively blocking the manual save workflow for any CSV-imported data. Users can only rely on auto-save as a workaround.

## Recommended Fix
1. Compare the manual save and auto-save code paths to identify where encoding handling diverges. 2. Add proper encoding detection/normalization for CSV-imported data at import time (sanitize to UTF-8 on ingest). 3. Wrap the manual save serialization in proper error handling so encoding failures surface as user-visible errors rather than crashes. 4. Ensure the flashing dialog's error is logged to a crash log for future debugging.

## Proposed Test Case
Import a CSV file containing non-UTF-8 characters (e.g., Latin-1 encoded text with accented characters, or a UTF-8 BOM file) into a task list, then trigger manual save. Verify that save completes without crashing and that the saved data preserves or gracefully handles the original characters.

## Information Gaps
- Exact encoding of the original CSV file and whether it contained non-ASCII characters
- Operating system and app version
- Whether the crash produces a log file or stack trace on disk
- Whether the issue reproduces with a smaller number of CSV-imported tasks (encoding vs. size interaction)
