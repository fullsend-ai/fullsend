# Triage Summary

**Title:** App crashes on save with large task lists containing CSV-imported data

## Problem
TaskFlow crashes (closes unexpectedly with a briefly-flashing error dialog) when the user clicks Save in the toolbar. The crash only occurs with a large task list (~200 tasks) that includes data imported from a CSV file. Saving small, manually-created task lists works fine. The user reports it worked before the CSV import.

## Root Cause Hypothesis
The CSV import likely introduced data with malformed characters, encoding issues, or values that exceed expected field limits. The save/serialization code path does not handle these edge cases gracefully — instead of surfacing a user-facing error, it throws an unhandled exception that crashes the app. The flashing error dialog suggests an exception is caught at the UI level but the app terminates before the dialog can be displayed, pointing to a crash in a finally block, a secondary exception during error handling, or the dialog being dismissed by the process exit.

## Reproduction Steps
  1. Install TaskFlow ~2.3.x on macOS 14.2
  2. Create or obtain a CSV file with ~200 tasks, including entries with special characters (emoji, non-ASCII, very long text, or encoding-ambiguous characters)
  3. Import the CSV into TaskFlow
  4. Click Save in the toolbar
  5. Observe: app closes unexpectedly with a briefly-flashing error dialog

## Environment
macOS 14.2, TaskFlow 2.3.x (exact patch version unknown)

## Severity: high

## Impact
Users who import CSV data with special characters into large task lists cannot save their work. The crash causes data loss since unsaved changes are discarded. This blocks a core workflow (bulk task import + save).

## Recommended Fix
1. Check the save/serialization code path for unhandled exceptions when processing tasks with special characters, encoding mismatches, or oversized fields. 2. Add defensive handling around the CSV-imported data — likely needs input sanitization or encoding normalization at import time. 3. Fix the error dialog lifecycle so it remains visible on crash rather than being dismissed by process exit. 4. Consider adding an autosave or recovery mechanism so crashes don't cause data loss.

## Proposed Test Case
Import a CSV containing 200+ tasks with mixed content (emoji, non-ASCII characters like accented letters and CJK characters, very long descriptions, tab/newline characters within fields, and fields with unmatched quotes). Click Save. Verify the save completes without crashing and the data round-trips correctly.

## Information Gaps
- Exact TaskFlow version (user said '2.3 something')
- Contents of the macOS crash report (user declined to check)
- The actual CSV file or its source application (would help identify the specific problematic characters)
- Whether the issue first appeared in a specific TaskFlow version or has always existed for large CSV imports
