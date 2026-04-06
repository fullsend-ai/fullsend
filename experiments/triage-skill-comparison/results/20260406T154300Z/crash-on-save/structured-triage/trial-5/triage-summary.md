# Triage Summary

**Title:** Desktop app crashes with encoding error when saving large existing task list

## Problem
TaskFlow desktop app crashes (closes entirely) when the user clicks the Save button in the toolbar while editing an existing task list containing approximately 200 tasks. A brief encoding-related error dialog flashes before the app closes, causing the user to lose unsaved work.

## Root Cause Hypothesis
The save/serialization path likely encounters a character encoding issue when processing a large task list — possibly a task contains special or non-ASCII characters that trigger an unhandled encoding exception during serialization, and the error handler or crash reporter briefly shows a dialog before the process exits.

## Reproduction Steps
  1. Open TaskFlow v2.3.1 desktop app on macOS 14.2
  2. Open an existing task list containing approximately 200 tasks
  3. Make an edit to one or more tasks
  4. Click the Save button in the toolbar
  5. Observe: app crashes (closes entirely) with a brief flash of an encoding-related error dialog

## Environment
macOS 14.2 (Sonoma), TaskFlow v2.3.1, desktop app

## Severity: high

## Impact
Users with large existing task lists lose all unsaved work when attempting to save. This blocks a core workflow (editing and saving tasks) and causes data loss.

## Recommended Fix
Investigate the save/serialization code path for encoding issues — check how task content is encoded when written to disk or sent to the backend. Look for unhandled exceptions in the encoding/serialization layer, particularly with special characters, emoji, or non-ASCII text in task fields. Add proper error handling so encoding failures surface a readable error instead of crashing. Also investigate whether the large list size (200 tasks) contributes (e.g., buffer overflow or memory issue during serialization).

## Proposed Test Case
Create a task list with 200+ tasks including tasks containing special characters (emoji, accented characters, CJK characters, etc.), edit a task, and verify that clicking Save completes successfully without crashing. Also test with a task list of similar size containing only ASCII to isolate whether size or content triggers the issue.

## Information Gaps
- Exact encoding error message (flashes too quickly for reporter to read — could be retrieved from crash logs)
- Whether the issue reproduces with smaller task lists or only with ~200+ tasks
- Whether any tasks contain special/non-ASCII characters
- Frequency: whether it happens on every save attempt or intermittently
