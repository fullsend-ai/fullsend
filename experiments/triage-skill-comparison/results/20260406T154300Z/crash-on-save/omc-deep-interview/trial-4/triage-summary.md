# Triage Summary

**Title:** App crashes on save when task list contains special characters imported from CSV

## Problem
TaskFlow v2.3.1 on macOS 14.2 crashes when saving a task list that contains special characters (em-dashes, curly quotes) introduced via CSV import. The app briefly flashes an error dialog mentioning 'encoding' before closing without a crash report. The user loses unsaved work each time.

## Root Cause Hypothesis
The save/serialization path likely fails on non-ASCII punctuation characters (smart quotes, em-dashes) that were imported from a Word-originated CSV. The encoding error suggests the save routine assumes ASCII or a specific encoding and chokes on these characters. The crash instead of graceful error handling suggests an unhandled exception in the serialization or file-write layer. The size correlation (works under ~50 imported tasks, fails at ~200) may indicate a buffer or memory interaction, or simply that fewer tasks means lower probability of hitting a problem character.

## Reproduction Steps
  1. Install TaskFlow v2.3.1 on macOS 14.2
  2. Create a CSV file containing tasks with Word-style special characters: curly/smart quotes (“ ” ‘ ’) and em-dashes (—)
  3. Create a new task list in TaskFlow
  4. Import the CSV file (aim for ~200 tasks to match reporter's scenario)
  5. Attempt to save the task list
  6. Observe: brief encoding error dialog flashes, then app closes/crashes

## Environment
TaskFlow v2.3.1, macOS 14.2 (Sonoma)

## Severity: high

## Impact
Any user who imports CSV data containing non-ASCII punctuation (common when data originates from Word or Google Docs) risks a crash on save with total loss of unsaved work. This is a data-loss scenario with no graceful fallback.

## Recommended Fix
Investigate the save/serialization code path for encoding assumptions. Likely fix: ensure the file writer uses UTF-8 encoding throughout. Add proper error handling around the save operation so encoding failures surface as a user-facing error message rather than crashing the app. Also review the CSV import path — it should either normalize special characters on import or ensure they are stored in a save-compatible encoding from the start.

## Proposed Test Case
Create a task list containing tasks with em-dashes, curly quotes, and other common non-ASCII punctuation. Verify that saving succeeds without error. Test at various list sizes (50, 200, 500 tasks) to confirm no size-dependent regression. Also verify that the save operation fails gracefully (error message, no data loss) if an encoding issue is deliberately introduced.

## Information Gaps
- Exact error message in the flashing dialog (reporter could not read it fully)
- Whether the issue reproduces on other OSes (Windows, Linux)
- Whether specific characters are the trigger vs. any non-ASCII character
- Whether the crash produces any log output in Console.app or system logs
