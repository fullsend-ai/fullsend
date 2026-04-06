# Triage Summary

**Title:** App crashes on save when task list contains CSV-imported tasks with encoding issues

## Problem
TaskFlow crashes instantly (app closes) when the user clicks Save on a task list containing tasks imported from a CSV file. A brief error dialog mentioning 'encoding' flashes before the app terminates. Removing the imported tasks restores normal save functionality. The user loses unsaved work on each crash.

## Root Cause Hypothesis
The CSV import path does not normalize character encoding, allowing non-UTF-8 or malformed characters into task data. The save/serialization code then encounters these characters and throws an unhandled encoding exception, which crashes the app instead of being caught and surfaced gracefully.

## Reproduction Steps
  1. Open TaskFlow v2.3.1 on macOS 14.2
  2. Import tasks from a CSV file (particularly one with ~200 tasks or containing non-ASCII/mixed-encoding characters)
  3. Click Save in the toolbar
  4. Observe: app crashes immediately with a brief 'encoding' error flash

## Environment
TaskFlow v2.3.1, macOS 14.2

## Severity: high

## Impact
Any user who imports tasks from CSV files risks data loss on every subsequent save. The crash is deterministic and repeatable, with no graceful recovery. Workaround exists (remove imported tasks) but defeats the purpose of the import feature.

## Recommended Fix
1. Investigate the save/serialization code path for unhandled encoding exceptions — add proper error handling so encoding failures surface as user-visible errors rather than crashes. 2. Audit the CSV import path to sanitize or re-encode input data to UTF-8 at import time, rejecting or replacing characters that cannot be converted. 3. Consider adding an encoding selector to the CSV import dialog.

## Proposed Test Case
Create CSV files with various encodings (Latin-1, Shift-JIS, UTF-8 with BOM, mixed encoding, embedded null bytes) and special characters. Import each into a task list and verify that (a) import either succeeds with clean data or fails with a clear error, and (b) saving a task list containing imported data never crashes the app.

## Information Gaps
- Exact error message in the crash dialog (reporter could only partially read it)
- The specific CSV file contents and its original encoding
- Whether the issue reproduces with smaller imports or only at scale (~200 tasks)
- Whether crash logs exist in macOS Console/crash reporter
