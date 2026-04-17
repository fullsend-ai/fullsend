# Triage Summary

**Title:** App crashes on save when task list contains CSV-imported tasks (~150 items)

## Problem
TaskFlow crashes immediately (window closes entirely) every time the user clicks Save in the toolbar when working with a task list that contains ~150 tasks imported from a CSV file. A brief error dialog flashes but disappears too quickly to read. The crash is 100% reproducible on the affected project. A separate, smaller project (~30 tasks, no CSV import) saves without issue. Removing the imported tasks restores save functionality.

## Root Cause Hypothesis
The CSV import likely introduced data that the save/serialization path cannot handle. Most probable causes: (1) a field contains characters that break serialization — unescaped quotes, null bytes, or encoding mismatches (e.g., Latin-1 data in a UTF-8 pipeline), (2) a field exceeds an assumed length limit causing a buffer or memory error during write, or (3) the import left orphaned or malformed relational references (e.g., missing project IDs, circular dependencies) that the save routine chokes on. The fact that the import 'succeeded' while save fails suggests validation happens at save time, not import time.

## Reproduction Steps
  1. Install TaskFlow ~2.3.x on macOS
  2. Create a new project/task list
  3. Import a large CSV (~150 tasks) exported from another project management tool — include varied punctuation (dashes, quotes, etc.)
  4. Edit any task in the list
  5. Click Save in the toolbar
  6. Observe: app window closes abruptly with a momentary error flash

## Environment
macOS (version unknown), TaskFlow ~2.3.x (exact build unknown), task list with ~200 total tasks including ~150 imported from CSV

## Severity: high

## Impact
Complete loss of save functionality for any project containing CSV-imported tasks. User is forced to maintain a manual text-file backup. The workaround (deleting imported tasks) defeats the purpose of the feature. Likely affects any user who imports a large or similarly-structured CSV.

## Recommended Fix
1. Check the macOS crash report for TaskFlow in ~/Library/Logs/DiagnosticReports to identify the exact exception and stack trace. 2. Inspect the save/serialization code path for unhandled exceptions — add proper error handling so the crash dialog persists and the app doesn't terminate. 3. Audit the CSV import pipeline for data sanitization gaps: check for encoding issues, unescaped special characters, excessively long fields, and malformed relational data. 4. Add import-time validation that catches data the save path cannot handle. 5. Test with a variety of CSVs including edge-case characters and large row counts.

## Proposed Test Case
Import a CSV containing 150+ tasks with mixed punctuation (quotes, dashes, ampersands, accented characters, empty fields) into a new project. Verify that saving succeeds without crashing. Additionally, create a unit test for the serialization path that exercises all supported field types with boundary-length and special-character inputs.

## Information Gaps
- Exact TaskFlow version and macOS version
- Contents of the macOS crash report (would pinpoint the exact exception)
- The original CSV file (would enable direct reproduction)
- Whether specific imported tasks trigger the crash or if it's a volume/aggregate issue
