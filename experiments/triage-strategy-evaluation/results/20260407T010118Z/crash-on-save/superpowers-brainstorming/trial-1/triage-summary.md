# Triage Summary

**Title:** Mac desktop app crashes on every task save (regression)

## Problem
The Mac desktop app crashes every time a user saves an edited task, regardless of which field was modified. This is a regression — the feature was working correctly until recently. The crash is 100% reproducible and causes data loss, as edits are lost on each crash.

## Root Cause Hypothesis
A recent code change broke the task save/update path in the desktop app. Since it affects all field edits and is platform-specific (Mac desktop), the likely culprit is a recent commit touching the task serialization, persistence layer, or a Mac-specific native integration (e.g., file system write, IPC call, or a dependency update) in the desktop build.

## Reproduction Steps
  1. Open the TaskFlow Mac desktop app
  2. Open any existing task for editing
  3. Modify any field (title, description, due date, etc.)
  4. Click Save
  5. App crashes

## Environment
Mac desktop app (specific macOS version and app version not provided)

## Severity: critical

## Impact
All Mac desktop users are unable to save any task edits. The app crashes on every save attempt, causing repeated data loss. This completely blocks task editing workflows on the Mac desktop client.

## Recommended Fix
1. Review recent commits touching the task save/update code path, especially any Mac-specific or desktop-specific changes. 2. Reproduce on a Mac desktop build — should be immediate on any task save. 3. Check crash logs (macOS Console.app or app crash reporter) for the specific exception/signal. 4. Look for recent dependency updates in the desktop build that may have introduced a breaking change.

## Proposed Test Case
Edit an existing task (modify title, then save; modify due date, then save; modify description, then save) on the Mac desktop app and verify the app does not crash and changes are persisted correctly.

## Information Gaps
- Specific macOS version and app version
- Crash log or error message details
- Whether the issue also affects Windows/Linux desktop or is Mac-only
