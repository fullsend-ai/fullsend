# Triage Summary

**Title:** Desktop app crashes (force-closes) when saving an edited task via toolbar Save button on macOS

## Problem
When editing a task and clicking the Save button in the toolbar, the entire desktop app force-closes. A brief error dialog flashes before the app disappears, reportedly mentioning something about 'encoding'. The reporter has lost work multiple times due to this crash.

## Root Cause Hypothesis
An unhandled exception in the task serialization/save path, likely related to character encoding — possibly a task containing special characters (emoji, non-ASCII text, or smart quotes from macOS) that the save routine fails to encode properly, causing a fatal crash instead of a graceful error.

## Reproduction Steps
  1. Open TaskFlow desktop app on macOS
  2. Open an existing task for editing
  3. Make changes to the task
  4. Click the Save button in the toolbar
  5. Observe: app force-closes with a brief error dialog

## Environment
macOS (version unspecified), TaskFlow desktop app, version approximately 2.3.x

## Severity: high

## Impact
Users lose unsaved work every time they attempt to save edited tasks. The reporter has experienced data loss multiple times. This blocks a core workflow (editing and saving tasks).

## Recommended Fix
Investigate the task save/serialization code path in the desktop app for encoding-related exceptions. Check for unhandled errors when serializing task content containing non-ASCII characters. Add proper error handling so encoding failures surface a user-visible error message rather than crashing the app. Review crash logs on macOS (Console.app / ~/Library/Logs/DiagnosticReports) for the exact stack trace.

## Proposed Test Case
Create a task containing various special characters (emoji, accented characters, smart quotes, CJK characters), edit it, and verify that saving via the toolbar completes without crashing. Also verify that a graceful error is shown if encoding does fail.

## Information Gaps
- Exact TaskFlow version number
- Exact macOS version
- Full text of the error dialog
- Whether the crash occurs with all tasks or only specific ones (e.g., those containing special characters)
- Whether crash logs are available in macOS Console
