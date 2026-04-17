# Triage Summary

**Title:** App crashes on toolbar Save for large task lists containing non-ASCII characters (curly quotes, em-dashes) imported from CSV

## Problem
When a user clicks the 'Save' button in the toolbar on a task list with approximately 200 tasks — some containing non-ASCII characters (curly quotes, em-dashes) imported from a CSV file — the app crashes immediately. A dialog briefly flashes mentioning 'encoding' before the app closes. Auto-save does not trigger the crash, and smaller lists (under 50 tasks) with similar characters save successfully via the toolbar.

## Root Cause Hypothesis
The toolbar Save button uses a different serialization/write path than auto-save — likely a bulk write that re-encodes the entire list at once. This bulk path does not handle non-ASCII characters (e.g., Windows-1252 curly quotes U+2018/U+2019, em-dashes U+2014) correctly, throwing an unhandled encoding exception. The size threshold suggests the bulk path may only be used above a certain task count (an optimization branch), which is why small lists with the same characters save fine — they may still use the simpler per-task path that auto-save also uses.

## Reproduction Steps
  1. Create or import a CSV file containing tasks with curly quotes (‘ ’ “ ”) and em-dashes (—) — at least 50+ tasks
  2. Import the CSV into TaskFlow as a new task list
  3. Edit any task in the list (to dirty the save state)
  4. Click the 'Save' button in the toolbar
  5. Observe the app crash with a brief 'encoding' dialog flash

## Environment
Not OS-specific based on report. Triggered by CSV-imported data containing Windows-1252/Unicode punctuation characters. List size threshold appears to be somewhere between 50 and 200 tasks.

## Severity: high

## Impact
Any user who imports tasks from external sources (CSV/spreadsheets) and accumulates a list above the size threshold is completely unable to manually save. They lose any work done since the last auto-save each time they hit Save. This is a data-loss scenario for users who rely on manual save.

## Recommended Fix
1. Investigate the toolbar Save code path vs the auto-save code path — identify where they diverge in serialization logic, especially for lists above the size threshold. 2. Check for a bulk/batch write optimization that kicks in for larger lists and inspect its character encoding handling. 3. Ensure the bulk path uses UTF-8 encoding (or matches whatever encoding auto-save uses successfully). 4. Add proper error handling around the save serialization so encoding failures surface as a recoverable error dialog rather than an app crash.

## Proposed Test Case
Create a task list with 200+ tasks where at least 10 tasks contain non-ASCII characters (curly quotes, em-dashes, accented characters, emoji). Trigger a toolbar Save and verify it completes without error. Additionally, add a unit test for the bulk serialization path that explicitly includes Windows-1252 punctuation characters (U+2018, U+2019, U+201C, U+201D, U+2014) and asserts successful round-trip encoding.

## Information Gaps
- Exact task count threshold where the save path switches from per-task to bulk write
- Whether the crash also affects the File > Save menu item or only the toolbar button
- Specific OS and app version (not critical since the root cause is in the save code path, not platform-specific)
- Whether the crash leaves a corrupted save file on disk or fails before writing
