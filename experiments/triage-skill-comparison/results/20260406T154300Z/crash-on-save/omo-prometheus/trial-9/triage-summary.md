# Triage Summary

**Title:** App crashes on save when task list contains special characters imported from CSV (encoding error)

## Problem
The application crashes (closes entirely) when the user clicks Save in the toolbar after importing tasks from a CSV file that contains special characters such as em-dashes and curly quotes. A dialog mentioning 'encoding' flashes briefly before the app closes. The crash is isolated to task lists containing the imported data — saving works correctly for non-imported tasks, and removing the imported tasks restores normal save behavior.

## Root Cause Hypothesis
The CSV file was exported from a Mac application and likely uses a non-UTF-8 encoding (e.g., MacRoman or Windows-1252). The import process ingested the raw bytes without transcoding them to the application's internal encoding (presumably UTF-8). When the save/serialization routine encounters these improperly-encoded characters (em-dashes U+2014, curly quotes U+201C/U+201D stored as single-byte MacRoman sequences), it hits an encoding error and crashes instead of handling it gracefully.

## Reproduction Steps
  1. Create or obtain a CSV file containing special characters (em-dashes, curly quotes) exported from a Mac application (likely MacRoman or Windows-1252 encoded)
  2. Open TaskFlow and create or open a task list
  3. Import the CSV file into the task list using the CSV import feature
  4. Click Save in the toolbar
  5. Observe: app crashes with a brief 'encoding' error dialog

## Environment
Not specified beyond the CSV originating from a Mac application. Issue is encoding-dependent, not platform-dependent.

## Severity: high

## Impact
Any user who imports CSV data containing non-ASCII characters from external tools (Excel on Mac, other Mac apps) will experience data loss — the app crashes on save, preventing them from persisting any changes to the affected task list. The user currently has no workaround other than removing all imported tasks.

## Recommended Fix
1. Investigate the CSV import path: ensure it detects or normalizes the source encoding to UTF-8 on import (e.g., using charset detection like ICU or chardet). 2. Investigate the save/serialization path: add proper error handling so encoding failures produce a user-visible error message rather than a crash. 3. Consider adding an encoding selector to the CSV import dialog, defaulting to UTF-8 with fallback options (Windows-1252, MacRoman). 4. For already-imported bad data, provide a repair/re-encode utility or migration.

## Proposed Test Case
Import a CSV file saved with MacRoman encoding containing em-dashes (0x97), curly left quotes (0x93), and curly right quotes (0x94). Verify that (a) the import correctly transcodes these to their UTF-8 equivalents (U+2014, U+201C, U+201D), and (b) the task list saves successfully without crashing. Additionally, test that a corrupted/mixed-encoding task list triggers a recoverable error message rather than a crash.

## Information Gaps
- Exact encoding of the source CSV file (MacRoman vs. Windows-1252 vs. other)
- Exact error message in the transient dialog (only 'encoding' was partially read)
- Application version and platform the reporter is running on
- Whether the crash produces a crash log or stack trace that could pinpoint the exact serialization call
