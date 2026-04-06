# Triage Summary

**Title:** TaskFlow crashes on save with brief 'encoding' error dialog (macOS, v2.3.1)

## Problem
When the user clicks Save from the toolbar, a dialog box briefly flashes (appearing to mention 'encoding') and the application immediately closes, causing loss of unsaved work.

## Root Cause Hypothesis
The save operation likely encounters a character encoding error (e.g., an unsupported or malformed character in the task data) that triggers an unhandled exception, causing the app to crash instead of displaying a persistent error dialog.

## Reproduction Steps
  1. Open TaskFlow v2.3.1 on macOS 14.2 (Sonoma)
  2. Create or open a task with content
  3. Click the Save button in the toolbar
  4. Observe: a dialog briefly flashes mentioning 'encoding' and the app closes immediately

## Environment
macOS 14.2 (Sonoma), TaskFlow v2.3.1 (desktop app)

## Severity: high

## Impact
Users lose all unsaved work every time they attempt to save. The issue is fully blocking — saving is a core function and it crashes 100% of the time from the toolbar.

## Recommended Fix
Investigate the save-to-disk code path triggered by the toolbar Save button. Look for encoding-related operations (file encoding selection, character set conversion, or content serialization) that may throw an unhandled exception. Add proper error handling so encoding failures surface a persistent error dialog rather than crashing the app. Check whether the issue is specific to certain file content (e.g., non-ASCII characters, emoji) or affects all saves.

## Proposed Test Case
Create a task, click Save from the toolbar, and verify the app does not crash. Additionally, test saving content containing non-ASCII characters, emoji, and special characters to confirm encoding is handled gracefully, with a user-visible error message on failure rather than a crash.

## Information Gaps
- Exact error message from the flashing dialog (reporter could not read it fully)
- Console or crash logs that would pinpoint the exact exception and stack trace
- Whether the crash occurs with all task content or only content containing specific characters (e.g., non-ASCII, emoji)
- Whether saving via a keyboard shortcut (Cmd+S) also triggers the crash, or only the toolbar button
