# Triage Summary

**Title:** App crashes on save when task list contains imported CSV data with special characters

## Problem
The app closes abruptly (with a briefly visible error dialog) whenever the user tries to save a task list that contains tasks imported from a specific CSV file. Removing the imported tasks restores normal save functionality.

## Root Cause Hypothesis
The CSV import path accepts characters (likely smart/curly quotes, em dashes, or other Unicode characters from a formatted spreadsheet) that the save/serialization path cannot handle. The import succeeds because it only reads data in, but saving requires writing it back in a specific format, and the serializer chokes on these characters — causing an unhandled exception that crashes the app.

## Reproduction Steps
  1. Create or open a task list in TaskFlow
  2. Import a CSV file containing tasks with special/formatted characters (smart quotes, em dashes, etc.)
  3. Verify the import completes successfully and tasks appear in the list
  4. Click Save
  5. Observe: app crashes (closes entirely) with a briefly flashing error dialog

## Environment
Not specified; issue appears to be platform-independent and related to data content rather than environment

## Severity: high

## Impact
User cannot save any work while the problematic imported tasks exist in their task list. They are forced to either delete the imported tasks (losing work) or avoid saving (risking data loss from the crash). This is a data-loss scenario that blocks normal workflow.

## Recommended Fix
Investigate the save/serialization codepath for character encoding issues. Likely the serializer does not handle non-ASCII or Unicode characters properly. Check for unhandled exceptions during save and add proper encoding support (e.g., UTF-8) to the serialization logic. Also: the crash on save should never kill the app silently — add error handling so a save failure shows a readable error message and does not discard the user's in-memory state.

## Proposed Test Case
Create a task list containing tasks with various special characters (smart quotes U+2018/U+2019, em dashes U+2014, non-ASCII Unicode, emoji, etc.), then save and verify the save completes without error and the data round-trips correctly when reloaded.

## Information Gaps
- Exact contents of the problematic CSV file (developer can request it from the reporter in a follow-up)
- Exact error message from the flashing dialog (developer can reproduce and capture it, or check logs)
- App version and platform (unlikely to change the investigation direction)
