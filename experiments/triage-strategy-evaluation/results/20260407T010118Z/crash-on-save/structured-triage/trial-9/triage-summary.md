# Triage Summary

**Title:** Desktop app crashes (sudden close) when saving a task list on macOS

## Problem
The desktop application abruptly closes whenever the user attempts to save a task list. A brief error message flashes on screen but disappears too quickly to read. The user reports losing work repeatedly. The issue is a recent regression — saving previously worked fine.

## Root Cause Hypothesis
A recent change (likely in v2.3 or a near update) introduced a crash in the save/serialization path for task lists on macOS. The briefly-flashing error suggests an unhandled exception that triggers an app-level crash handler or immediate process exit rather than a graceful error dialog.

## Reproduction Steps
  1. Install TaskFlow desktop app (~v2.3) on macOS 14
  2. Open or create a task list
  3. Make edits to the task list
  4. Click Save
  5. Observe: app closes abruptly with a momentary error flash

## Environment
macOS 14 (Sonoma), TaskFlow desktop app version ~2.3

## Severity: high

## Impact
Users lose unsaved work on every save attempt. The core save functionality is broken, making the app effectively unusable for task list editing. Likely affects all macOS desktop users on this version.

## Recommended Fix
1. Pull the macOS crash report from Console.app (or reproduce locally) to get the full stack trace. 2. Check recent changes to the task-list save/serialization path in the v2.3 release. 3. Look for unhandled exceptions in file I/O, serialization, or autosave logic that could cause an immediate process exit rather than an error dialog. 4. Ensure the save path has proper error handling that surfaces errors gracefully instead of crashing.

## Proposed Test Case
Create a task list with several items on macOS, click Save, and verify the app remains open with a success confirmation. Additionally, test save with edge-case content (empty list, very large list, special characters) to ensure no crash occurs.

## Information Gaps
- Exact app version (reporter said '2.3 I think')
- Exact macOS version (reporter said '14 something')
- Full error message or crash log from macOS Console.app
- What specifically changed before the issue started ('it was working fine until recently')
- Whether the crash occurs with all task lists or only specific ones
