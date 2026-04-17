# Triage Summary

**Title:** App crashes on save after CSV import — encoding error with large task list

## Problem
When a user imports tasks from a CSV file and then attempts to save the task list via the toolbar 'Save' button, the application crashes (closes abruptly). A brief error dialog flashes mentioning 'encoding' before the app terminates. The user has approximately 200 tasks in the list. The crash is reproducible on every save attempt and causes data loss.

## Root Cause Hypothesis
The CSV import likely ingested text containing characters in a non-UTF-8 encoding (e.g., Latin-1, Windows-1252) or introduced malformed byte sequences. The save/serialization path probably assumes UTF-8 and throws an unhandled encoding exception when it encounters these characters, causing the app to crash.

## Reproduction Steps
  1. Create or obtain a CSV file with ~200 tasks containing non-ASCII characters (e.g., accented characters, curly quotes, em dashes from a Windows-originated file)
  2. Import the CSV file into TaskFlow
  3. Click 'Save' in the toolbar
  4. Observe the app crash with a brief encoding-related error

## Environment
Not specified — likely reproducible across platforms since the root cause is data-driven (encoding in imported content), not OS-specific

## Severity: high

## Impact
Any user who imports tasks from CSV files with non-UTF-8 characters will be unable to save their work, resulting in repeated data loss. The reporter is blocked on a project deadline.

## Recommended Fix
1. Inspect the save/serialization code path for unhandled encoding exceptions — add proper error handling so encoding failures don't crash the app. 2. Fix the CSV import to normalize all incoming text to UTF-8 (detect source encoding and transcode). 3. Consider adding a recovery mechanism so unsaved data isn't lost on crash (e.g., auto-save to a temp file before serialization).

## Proposed Test Case
Import a CSV file containing characters in Windows-1252 encoding (e.g., curly quotes, em dashes) with 200+ rows, then save the task list. Verify the save completes without error and all characters are preserved correctly.

## Information Gaps
- Exact error message (reporter couldn't read the full dialog)
- Source and encoding of the original CSV file
- Whether the crash occurs with a smaller number of imported tasks (encoding vs. size issue)
- Operating system and TaskFlow version
