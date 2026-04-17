# Triage Summary

**Title:** App crashes (full close) on Save with encoding error when saving large task list

## Problem
When a user clicks the Save button in the toolbar to save a task list containing approximately 200 tasks, the application crashes and closes entirely. A dialog briefly flashes mentioning 'encoding' before the app terminates. The user loses unsaved work each time this occurs.

## Root Cause Hypothesis
The save/serialization code path likely throws an unhandled encoding exception when processing task data containing characters that fall outside the expected character set (e.g., non-ASCII characters, emoji, special symbols, or text pasted from external sources). The exception propagates uncaught, tearing down the application. The briefly-visible dialog is likely the OS or framework's unhandled-exception reporter.

## Reproduction Steps
  1. Create or import a task list with ~200 tasks
  2. Ensure some tasks contain non-ASCII characters (emoji, accented characters, or text pasted from external sources like email or web pages)
  3. Click the Save button in the toolbar
  4. Observe the app crash with a brief encoding-related dialog

## Environment
Not specified — likely reproducible across environments since this appears to be a code-level serialization bug rather than platform-specific

## Severity: high

## Impact
Users with non-trivial task lists risk losing all unsaved work on every save attempt. This blocks a core workflow (persisting data) and erodes trust in the application.

## Recommended Fix
1. Inspect the save handler triggered by the toolbar Save button — trace the serialization path for task list data. 2. Identify where text encoding is performed (likely file write or JSON/XML serialization) and ensure it uses UTF-8 throughout. 3. Wrap the save operation in proper error handling so encoding failures surface as a user-visible error message rather than crashing the app. 4. Add an auto-save or recovery mechanism so in-progress work is not lost on failure.

## Proposed Test Case
Create a task list containing tasks with ASCII text, accented characters (é, ñ), emoji (🎉), CJK characters (任务), and control characters. Verify that saving and reloading preserves all content without errors or crashes. Also test with 200+ tasks to confirm no size-related issues in the serialization path.

## Information Gaps
- Exact OS and app version (unlikely to change the fix but useful for reproduction)
- Whether the crash is 100% reproducible or intermittent
- Whether the task data was manually entered or imported/pasted from an external source
- Exact text of the encoding error dialog
