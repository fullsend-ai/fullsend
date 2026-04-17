# Triage Summary

**Title:** App crashes on save after importing CSV with probable non-UTF-8 encoding

## Problem
The application crashes (fully quits) whenever the user saves after importing tasks from a CSV file. A brief error dialog mentioning 'encoding' flashes before the app closes. Removing the imported tasks restores normal save functionality.

## Root Cause Hypothesis
The CSV file from the reporter's team spreadsheet likely contains text in a non-UTF-8 encoding (e.g., Windows-1252, Latin-1) or includes characters that the save/serialization path does not handle. When the app attempts to serialize task data to disk, it hits an unhandled encoding error that crashes the process instead of being caught and reported gracefully.

## Reproduction Steps
  1. Create or obtain a CSV file exported from a spreadsheet tool (e.g., Excel) that uses non-UTF-8 encoding or contains special/extended characters in task names or dates
  2. Import the CSV into TaskFlow using the CSV import feature
  3. Attempt to save
  4. Observe: app crashes with a brief encoding-related error dialog

## Environment
Not confirmed — app appears to be a desktop or Electron application (fully quits on crash rather than showing a web error). OS not specified. CSV originated from a team spreadsheet (likely Excel).

## Severity: high

## Impact
Complete data loss risk — the user cannot save any work after importing CSV data, which is a core workflow. The user is currently forced to use manual copy-paste as a backup workaround. Any user importing CSVs from spreadsheet tools with non-UTF-8 encoding would hit this.

## Recommended Fix
Investigate the save/serialization codepath for encoding assumptions. Likely the save routine assumes UTF-8 input but the CSV importer passes through raw bytes or a different encoding. Fix should: (1) normalize imported text to UTF-8 at import time, or (2) handle encoding errors gracefully during save with a user-facing error message instead of a crash. Also add a try/catch around the save serialization to prevent full app crashes on any serialization error.

## Proposed Test Case
Import a CSV file saved with Windows-1252 encoding containing extended characters (e.g., accented names, em-dashes, curly quotes) into a task list, then trigger save. Verify the app either saves successfully (after encoding normalization) or displays a clear error message — and does not crash.

## Information Gaps
- Exact CSV file contents and encoding (reporter doesn't have visibility into this)
- OS and app version
- Exact error message text from the crash dialog
