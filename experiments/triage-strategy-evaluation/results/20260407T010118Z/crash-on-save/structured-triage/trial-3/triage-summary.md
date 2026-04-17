# Triage Summary

**Title:** Desktop app crashes immediately on save when editing tasks (macOS, v2.3.x)

## Problem
The TaskFlow desktop app force-closes every time the user clicks Save while editing their task list. A brief error dialog flashes on screen but disappears too quickly to read. The user loses all unsaved work each time.

## Root Cause Hypothesis
An unhandled exception during the save/serialization path causes a fatal crash. The flashing error box suggests the app does catch the error and attempts to display it, but the process terminates before the dialog can persist — possibly an uncaught exception in an async save handler or a segfault in the native layer that tears down the window.

## Reproduction Steps
  1. Open TaskFlow desktop app (~v2.3.x) on macOS
  2. Open or navigate to the task list
  3. Edit any existing task
  4. Click Save
  5. Observe: app closes immediately with a briefly flashing error dialog

## Environment
macOS (latest version, likely Sequoia), TaskFlow desktop app version ~2.3.x

## Severity: critical

## Impact
Complete data loss on every save attempt. The user cannot use the core save functionality at all, making the app effectively unusable for editing. Any user on a similar macOS + v2.3.x configuration is likely affected.

## Recommended Fix
1. Check macOS crash logs (~/Library/Logs/DiagnosticReports or Console.app) for TaskFlow crash reports to identify the exact exception and stack trace. 2. Review the save/serialization code path for unhandled exceptions, particularly around file I/O, data validation, or async callbacks. 3. Investigate whether a recent change in v2.3.x introduced a regression in the save handler. 4. Ensure the error dialog is modal and blocks process exit so users can read error messages.

## Proposed Test Case
Create or open a task list, modify a task field, and invoke Save. Verify the save completes without crashing and the edited data persists after reopening the app. Additionally, test with malformed or edge-case task data to confirm the save path handles errors gracefully rather than crashing.

## Information Gaps
- Exact macOS version (assumed latest/Sequoia)
- Exact TaskFlow version (reporter said ~2.3)
- Crash log or diagnostic report content from macOS Console
- Whether the crash started after a specific app update or macOS update
- Whether the issue is specific to editing existing tasks or also affects creating new tasks
