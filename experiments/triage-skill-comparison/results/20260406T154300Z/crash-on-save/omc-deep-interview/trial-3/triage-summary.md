# Triage Summary

**Title:** App crashes on save with encoding error when saving large lists containing CSV-imported typographic characters

## Problem
The application crashes immediately (window closes) when saving a task list that contains ~200+ tasks imported from a CSV file with typographic characters (curly quotes, em-dashes). A brief error dialog mentioning 'encoding' flashes before the crash. Smaller lists (<50 tasks) with the same imported data save successfully, and removing the imported tasks from the large list eliminates the crash.

## Root Cause Hypothesis
The save/serialization path likely fails when encoding typographic characters (smart quotes U+2018/U+2019/U+201C/U+201D, em-dash U+2014) that are outside the ASCII range. The size dependency suggests either: (1) a buffer overflow where the multi-byte UTF-8 encoding of these characters causes a miscalculated buffer size to overflow only at scale, or (2) an error-handling path that accumulates encoding errors and only crashes past a threshold. The most likely root cause is that the save routine assumes ASCII or single-byte encoding for buffer allocation, and the multi-byte typographic characters cause a write-past-end that becomes fatal at ~200 tasks.

## Reproduction Steps
  1. Create or obtain a CSV file with typographic characters in task names (curly/smart quotes, em-dashes) — export from a spreadsheet app to get these naturally
  2. Import the CSV into TaskFlow
  3. Ensure the task list contains approximately 200 tasks (include the imported ones with typographic characters)
  4. Click Save in the toolbar
  5. Observe: brief encoding error dialog flash, then app crashes (window closes)

## Environment
Desktop app (OS not specified). CSV was exported from a spreadsheet application (Excel or Google Sheets). No specific version reported.

## Severity: high

## Impact
Users who import CSV data with typographic characters into large task lists lose all unsaved work on every save attempt. Workaround exists: remove imported tasks or keep lists under ~50 tasks, but this is not practical for the reporter's 200-task workflow.

## Recommended Fix
Investigate the save/serialization code path for encoding handling. Likely candidates: (1) Check buffer size calculation in the save routine — ensure it accounts for multi-byte UTF-8 characters, not just character count. (2) Check if the serializer (JSON, XML, or custom format) properly handles Unicode code points above U+007F. (3) Add proper error handling around the encoding step so failures surface a readable error instead of crashing. (4) Consider normalizing typographic characters on CSV import (e.g., converting smart quotes to straight quotes) as a secondary defense.

## Proposed Test Case
Create a task list with 200+ tasks where at least 20 tasks contain typographic characters (curly quotes, em-dashes, ellipsis characters). Verify that save completes without error and that the data round-trips correctly (reload and confirm typographic characters are preserved). Also test boundary: 50, 100, 150, 200 tasks with the same special characters to identify the exact failure threshold.

## Information Gaps
- Exact OS and app version not provided
- Whether the CSV was UTF-8 or another encoding (e.g., Windows-1252) is unknown
- Exact error message in the dialog (it disappears too quickly to read fully)
- Whether the crash produces a crash log or stack trace on disk
