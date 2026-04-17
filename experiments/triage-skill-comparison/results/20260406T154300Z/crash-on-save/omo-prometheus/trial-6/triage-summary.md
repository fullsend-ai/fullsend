# Triage Summary

**Title:** App crashes on save with encoding error after CSV task import

## Problem
After importing tasks from a CSV file, clicking Save in the toolbar causes the app to briefly flash a dialog referencing an 'encoding' error and then terminate immediately. The user loses any unsaved work. Saving worked correctly before the CSV import; no app or OS updates occurred — the CSV import is the sole change.

## Root Cause Hypothesis
The imported CSV likely contains characters in an encoding (e.g., Latin-1, Windows-1252, or mixed encoding) that the save/serialization routine does not handle. When the app attempts to serialize the ~200 tasks (including the imported ones) to its storage format, it encounters characters it cannot encode, throws an unhandled encoding exception, and crashes. The briefly-visible dialog is likely an uncaught exception or error reporter surfacing before the process exits.

## Reproduction Steps
  1. Start with a working TaskFlow installation with a task list that saves successfully
  2. Prepare or obtain a CSV file containing tasks with non-ASCII or mixed-encoding characters (e.g., accented characters, em-dashes, curly quotes from a Windows-origin file)
  3. Import the CSV file using TaskFlow's CSV import feature
  4. Click Save in the toolbar
  5. Observe: a dialog briefly flashes mentioning 'encoding', then the app closes

## Environment
Not specified — likely desktop app. No recent OS or app updates. Issue is data-triggered (CSV content), not environment-specific.

## Severity: high

## Impact
User is completely blocked from saving their task list. Any new work done in the app is lost on every save attempt. The task list contains ~200 tasks, so recreating it or reverting the import may not be practical.

## Recommended Fix
1. Investigate the save/serialization path for encoding handling — identify where it assumes or enforces a specific encoding. 2. Add proper encoding detection/normalization during CSV import so that all incoming text is converted to the app's internal encoding (likely UTF-8) at import time. 3. Add a fallback or graceful error in the save path so that encoding errors are caught and reported to the user rather than crashing the app. 4. Consider adding a 'repair' or 're-encode' utility for existing task lists that already contain problematic characters.

## Proposed Test Case
Import a CSV file containing a mix of UTF-8, Latin-1, and Windows-1252 encoded characters (including accented characters, curly quotes, em-dashes). Verify that: (a) the import normalizes all text to UTF-8, (b) saving after import succeeds without error, and (c) if a malformed character is encountered during save, the app displays a meaningful error and does not crash.

## Information Gaps
- Exact source and encoding of the CSV file
- Specific characters or rows in the CSV that trigger the issue
- App version and platform (Windows/macOS/Linux)
- Full text of the encoding error dialog (it flashes too quickly for the reporter to read)
