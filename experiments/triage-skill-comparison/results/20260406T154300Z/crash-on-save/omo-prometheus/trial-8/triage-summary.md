# Triage Summary

**Title:** Desktop app crashes on save when task list contains CSV-imported tasks with special characters (encoding error)

## Problem
When saving a task list that contains tasks imported from a CSV file, the desktop app shows a brief error dialog referencing 'encoding' and then immediately closes. The crash is isolated to lists containing CSV-imported data — manually created tasks save without issue. The CSV contained tasks with special characters such as curly/smart quotes and em-dashes.

## Root Cause Hypothesis
The CSV import path is ingesting text with non-ASCII characters (smart quotes, em-dashes, etc.) that are stored in a form the save/serialization layer cannot handle. Most likely the CSV is read with one encoding (e.g., UTF-8 or Windows-1252) but the save routine assumes a different or more restrictive encoding (e.g., ASCII or a mismatched UTF variant), causing an unhandled encoding exception that crashes the app instead of being caught gracefully.

## Reproduction Steps
  1. Open TaskFlow v2.3.1 desktop app on macOS
  2. Prepare a CSV file containing tasks with special characters (curly quotes, em-dashes, etc.)
  3. Import the CSV into a task list
  4. Click the Save button in the toolbar
  5. Observe: brief error dialog mentioning 'encoding' appears, then the app closes immediately

## Environment
TaskFlow v2.3.1, macOS 14.2 (Sonoma), desktop app. Task list with ~200 items, including tasks imported from CSV with non-ASCII characters.

## Severity: high

## Impact
Any user who imports tasks from CSV files containing non-ASCII characters (common in real-world data) will be unable to save their work. The app crashes without a proper error message or recovery path, causing data loss. This blocks a core workflow (import + save).

## Recommended Fix
1. Investigate the save/serialization code path for encoding assumptions — ensure it handles UTF-8 (and other encodings) consistently with the CSV import path. 2. Add proper error handling around the save operation so encoding failures surface as a user-visible error message rather than crashing the app. 3. Consider normalizing special characters (e.g., curly quotes → straight quotes) during CSV import, or at minimum validating that imported text is round-trippable through the save path. 4. Check for an unhandled exception in the encoding/serialization layer that is causing the abrupt app exit.

## Proposed Test Case
Import a CSV file containing tasks with various non-ASCII characters (smart/curly quotes, em-dashes, accented characters, emoji) into a task list, then save. Verify: (a) the save completes successfully, (b) the special characters are preserved correctly when the list is reopened, and (c) if any character cannot be saved, a clear error message is shown instead of a crash.

## Information Gaps
- Exact error message in the flash dialog (reporter could not read it fully)
- Whether macOS Console.app or ~/Library/Logs contains a crash log with a stack trace
- The exact encoding of the original CSV file (UTF-8, Windows-1252, etc.)
- Whether the issue reproduces on other platforms (web, Windows) or is macOS-desktop-specific
