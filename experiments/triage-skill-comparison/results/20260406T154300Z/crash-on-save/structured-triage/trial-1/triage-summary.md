# Triage Summary

**Title:** App crashes on save when task list contains special characters imported from CSV

## Problem
TaskFlow crashes immediately upon saving a task list that contains approximately 200 tasks imported from a CSV file. A dialog briefly flashes mentioning 'encoding' before the app closes completely, resulting in data loss. Smaller task lists without the imported data save successfully.

## Root Cause Hypothesis
The CSV import likely brought in text with non-UTF-8 encoding (e.g., Windows-1252 or MacRoman) containing characters like em-dashes and curly quotes. The save routine appears to hit an encoding error when serializing these characters, and the unhandled exception causes the app to crash instead of gracefully recovering.

## Reproduction Steps
  1. Create or obtain a CSV file containing task names with special characters (em-dashes, curly quotes) exported from a spreadsheet application
  2. Import the CSV into TaskFlow to create a task list with ~200 tasks
  3. Attempt to save the task list
  4. Observe the brief 'encoding' dialog flash followed by the app closing

## Environment
macOS 14.2 (Sonoma), TaskFlow v2.3.1

## Severity: high

## Impact
Users who import task data from spreadsheets via CSV lose all unsaved work on every save attempt. This blocks normal use of the app for affected task lists and risks significant data loss.

## Recommended Fix
Investigate the save/serialization path for encoding handling. Ensure imported CSV data is normalized to UTF-8 at import time, or that the save routine can handle mixed encodings gracefully. The crash dialog should be caught and surfaced as a recoverable error rather than terminating the app.

## Proposed Test Case
Import a CSV file containing task names with non-ASCII characters (em-dashes, curly quotes, accented characters) in various encodings (Windows-1252, MacRoman, UTF-8 with BOM). Verify that saving the resulting task list succeeds without errors and that the special characters are preserved correctly.

## Information Gaps
- Exact encoding of the original CSV file is unknown
- Exact error message from the flashing dialog was not fully captured
- Whether the issue is triggered by list size, specific characters, or a combination of both is not fully isolated
