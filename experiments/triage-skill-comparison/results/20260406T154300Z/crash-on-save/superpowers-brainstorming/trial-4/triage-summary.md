# Triage Summary

**Title:** App crashes on manual Save of task list — brief 'encoding' error before shutdown

## Problem
When a user clicks the 'Save' button in the toolbar while editing a task list (~200 tasks), the app crashes. A dialog box briefly flashes with what appears to be an 'encoding'-related message before the application shuts down. Auto-save continues to work correctly; only the manual save path is affected.

## Root Cause Hypothesis
The manual save code path uses a different serialization or encoding method than auto-save. Most likely, manual save attempts to encode the full task list in a way that either hits an unhandled character (e.g., non-ASCII/emoji in a task title or description) or a buffer/size limitation that the incremental auto-save avoids. The uncaught encoding exception causes the crash.

## Reproduction Steps
  1. Open TaskFlow and navigate to a task list with a large number of tasks (~200)
  2. Edit any task in the list
  3. Click the 'Save' button in the toolbar (do not rely on auto-save)
  4. Observe the brief encoding error dialog followed by an app crash

## Environment
Not platform-specific based on report; likely reproducible across environments. Task list size (~200 items) may be a contributing factor.

## Severity: high

## Impact
Users who manually save task lists lose unsaved work and experience data loss anxiety. Users with large task lists are most likely affected. Auto-save provides a partial workaround but users cannot trust explicit saves.

## Recommended Fix
Compare the manual save and auto-save code paths. Investigate encoding/serialization differences — check for charset handling (UTF-8 BOM, special characters, emoji), payload size limits, or exception handling gaps in the manual save flow. Add proper error handling around the encoding step so failures surface a readable error instead of crashing. Check whether the encoding dialog is a system-level unhandled exception dialog.

## Proposed Test Case
Create a task list with 200+ tasks including tasks containing special characters (Unicode, emoji, accented characters, RTL text). Trigger manual save via the toolbar button. Verify save completes without crash and data is persisted correctly. Also test with a smaller list to isolate whether size or content triggers the issue.

## Information Gaps
- Exact text of the encoding error message (flashes too fast for reporter to read)
- Whether the crash depends on task count, specific task content, or both
- Platform and OS version (not yet asked — unlikely to change fix approach)
