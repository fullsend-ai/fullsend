# Triage Summary

**Title:** Desktop app crashes on save with encoding error after CSV import (~200 tasks)

## Problem
TaskFlow desktop app v2.3.1 crashes immediately when the user clicks the Save button in the toolbar on the main task list view. The crash began after importing approximately 200 tasks from a CSV file. A brief error dialog referencing 'encoding' flashes before the app closes entirely. The crash occurs even without making any changes — simply clicking Save triggers it.

## Root Cause Hypothesis
The CSV import likely introduced task data containing characters in an encoding the save/serialization routine cannot handle (e.g., non-UTF-8 characters, malformed Unicode sequences, or a BOM). When the app attempts to serialize the full task list to disk, it hits an unhandled encoding exception and crashes.

## Reproduction Steps
  1. Install TaskFlow desktop app v2.3.1 on macOS 14.2
  2. Import a CSV file containing approximately 200 tasks
  3. Navigate to the main task list view
  4. Click the 'Save' button in the toolbar
  5. Observe the app crash (brief encoding-related error dialog flashes before the app closes)

## Environment
macOS 14.2 (Sonoma), TaskFlow desktop app v2.3.1

## Severity: high

## Impact
User cannot save any work at all, leading to repeated data loss. The app is effectively unusable after the CSV import. Any user who imports CSV data with similar encoding characteristics would hit the same crash.

## Recommended Fix
Investigate the save/serialization path for unhandled encoding exceptions. Check how imported CSV data is stored in memory and written to disk — likely the CSV reader accepted data in a non-UTF-8 encoding that the save routine does not handle. Add encoding validation or normalization during CSV import, and add graceful error handling (with a readable error message) in the save path so it never crashes silently.

## Proposed Test Case
Create a CSV file containing tasks with mixed encodings (e.g., Latin-1 characters, curly quotes, BOM markers, null bytes) and import it into TaskFlow. Verify that (a) the import either normalizes encoding or rejects invalid characters with a clear message, and (b) saving after import succeeds or produces a user-readable error rather than crashing.

## Information Gaps
- Exact content/encoding of the original CSV file used for import
- Full text of the encoding error dialog (disappears too quickly to read)
- Whether deleting the imported tasks restores the ability to save
- Application crash logs (e.g., from macOS Console or ~/Library/Logs)
