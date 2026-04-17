# Triage Summary

**Title:** App crashes on Save with large task list imported from CSV (~200 tasks, Mac, v2.3.1)

## Problem
TaskFlow hard-crashes (immediately closes) every time the user clicks Save in the toolbar. The crash began after the user imported approximately 200 tasks from a CSV file. A separate list with ~30 tasks saves without issue. A brief error message flashes on screen before the app closes but is unreadable.

## Root Cause Hypothesis
The save operation likely fails when serializing or validating a large number of tasks, possibly due to an unhandled exception in the save path that scales with list size. The CSV import may have introduced data that the save function doesn't handle gracefully — either the volume itself triggers a timeout/memory issue, or the imported data contains subtle formatting (e.g., encoding, line breaks, or characters from the source app's export) that corrupts the save payload. The fact that it started after the import and affects only that list points to a data-dependent crash rather than a general save regression.

## Reproduction Steps
  1. Install TaskFlow 2.3.1 on macOS
  2. Create a CSV file with ~200 task names (plain text, one per row — simulate an export from another task app)
  3. Import the CSV into a new task list
  4. Click Save in the toolbar
  5. Observe: app crashes immediately with a brief error flash

## Environment
macOS, TaskFlow v2.3.1 (reported as approximate)

## Severity: critical

## Impact
User cannot save their primary task list at all (100% crash rate), resulting in data loss on every attempt. Any user who imports a large CSV is likely to hit this. Smaller lists are unaffected.

## Recommended Fix
1. Add crash logging / exception handling around the save path so the fleeting error is captured to a log file. 2. Reproduce with a ~200-row imported CSV and inspect the stack trace. 3. Investigate whether the crash is size-dependent (try 50, 100, 150, 200 rows) or data-dependent (try 200 rows of trivial 'Task N' names). 4. Check for unbounded memory allocation, synchronous blocking on large payloads, or malformed data from CSV import (encoding issues, unexpected delimiters). 5. Regardless of root cause, the save function should never hard-crash — add graceful error handling and user-visible error reporting.

## Proposed Test Case
Import a CSV with 200+ task names into a new list, then invoke Save. Verify the app does not crash, the file is persisted correctly, and all 200 tasks are present when the list is reopened. Additionally, test with CSVs containing edge-case data (unicode, empty rows, very long task names) to cover data-dependent failures.

## Information Gaps
- Exact error message (flashes too fast for user to read — crash logs would reveal this)
- Original CSV file (user may be able to locate it later for exact reproduction)
- Exact TaskFlow version (user said '2.3.1 I think')
- Whether the source app's CSV export uses any unusual encoding or formatting
