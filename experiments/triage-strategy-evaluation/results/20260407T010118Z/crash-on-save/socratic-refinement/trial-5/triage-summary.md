# Triage Summary

**Title:** App crashes on save when project contains CSV-imported tasks

## Problem
The application force-closes immediately when the user saves a project that contains tasks imported from a CSV file. A brief error message flashes on screen before the app exits, but it disappears too quickly to read. The user has approximately 200 tasks, a portion of which were imported. Saving works normally if the imported tasks are deleted first.

## Root Cause Hypothesis
The CSV import likely introduces data that the save/serialization code path cannot handle — most probably special characters, encoding issues, unexpectedly long fields, or empty/null values in required fields. The save function appears to throw an unhandled exception when it encounters this data, causing the application to crash rather than fail gracefully.

## Reproduction Steps
  1. Create a project with some manually-created tasks and confirm saving works
  2. Prepare a CSV file with potential edge cases: special characters, very long text fields, empty fields, non-UTF-8 encoding
  3. Import the CSV file into the project
  4. Attempt to save the project
  5. Observe crash — app should force-close with a brief error flash

## Environment
Not specified by reporter. The project contains approximately 200 tasks including a batch imported from a CSV that was originally built in a spreadsheet by a coworker.

## Severity: high

## Impact
User loses unsaved work on every save attempt and cannot use the application productively. The only workaround (deleting imported tasks) means losing data the user needs. Any user who imports CSV data with similar characteristics would hit the same crash.

## Recommended Fix
1. Investigate the save/serialization code path for unhandled exceptions — add proper error handling so a single bad record doesn't crash the app. 2. Audit the CSV import function to sanitize or validate incoming data at import time (encoding normalization, field length limits, null handling). 3. Check for the specific fleeting error by running the app from a terminal or examining crash logs to identify the exact exception. 4. Consider adding a save-to-temp-then-rename pattern so that a crash during save doesn't destroy existing data.

## Proposed Test Case
Import a CSV file containing tasks with edge-case data (special/unicode characters, empty required fields, extremely long descriptions, mixed encodings) into a project, then call save. Verify the save completes without crashing. If any records are invalid, verify the app surfaces a clear error and still saves the valid data.

## Information Gaps
- Exact OS and app version (not critical — the bug is data-dependent, not platform-dependent)
- Contents of the specific CSV file that triggered the issue (would speed up reproduction but developer can test with synthetic edge-case CSVs)
- The exact error message that flashes before the crash (developer can capture this from logs or terminal output)
