# Triage Summary

**Title:** Desktop app crashes on save with encoding-related error (macOS, v2.3.1)

## Problem
When the user clicks Save in the toolbar, a dialog briefly flashes (mentioning 'encoding') and the app immediately closes, losing all unsaved changes. This occurs consistently.

## Root Cause Hypothesis
The save operation likely encounters a character encoding issue — possibly the task content contains characters that the serialization layer cannot handle (e.g., non-UTF-8 characters, special Unicode, or an encoding mismatch between the editor and the file writer). The unhandled encoding exception causes the app to crash rather than surfacing a user-facing error.

## Reproduction Steps
  1. Open TaskFlow v2.3.1 desktop app on macOS 14.2
  2. Create or open a task (potentially one containing non-ASCII or special characters)
  3. Click the Save button in the toolbar
  4. Observe: a dialog briefly flashes mentioning 'encoding', then the app closes

## Environment
macOS 14.2 (Sonoma), TaskFlow v2.3.1, desktop app

## Severity: high

## Impact
Users lose all unsaved work every time they attempt to save. This blocks core functionality — the app is effectively unusable for affected users since saving is a fundamental operation.

## Recommended Fix
Investigate the save/serialization path for unhandled encoding exceptions. Check what character encoding the file writer expects versus what the editor produces. Add proper error handling around the save operation so encoding failures surface as a user-facing error dialog rather than crashing the app. Review crash logs on macOS (Console.app / ~/Library/Logs/DiagnosticReports) for the specific exception.

## Proposed Test Case
Create a task containing various character classes (ASCII, accented characters, emoji, CJK characters, mixed encodings) and verify that saving succeeds or fails gracefully with an informative error — never crashes.

## Information Gaps
- Exact error message from the flashing dialog (goes by too fast to read fully)
- Whether the crash occurs with all tasks or only tasks containing specific characters/content
- Crash logs from macOS diagnostic reports
- Whether this is a regression from a previous TaskFlow version
