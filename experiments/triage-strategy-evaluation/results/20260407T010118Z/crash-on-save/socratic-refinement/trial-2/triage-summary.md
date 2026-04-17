# Triage Summary

**Title:** Save crashes with imported CSV data containing non-standard Unicode characters

## Problem
The app crashes immediately on save when the task list contains ~200 tasks imported from a CSV file. A brief error message flashes before the app closes. Saving a new list with simple manually-created tasks works fine, confirming the issue is data-dependent, not a general save failure.

## Root Cause Hypothesis
The save/serialization path does not handle non-standard Unicode characters (curly quotes, em dashes, and similar typographic punctuation) that were present in the CSV-imported task names. The serializer likely throws an unhandled exception on encountering these characters, crashing the app instead of gracefully handling or sanitizing the input.

## Reproduction Steps
  1. Create a new task list in TaskFlow
  2. Import or manually create tasks with non-standard Unicode punctuation in names — e.g., curly quotes (“ ” ‘ ’), em dashes (—), or other typographic characters
  3. Add enough tasks to approximate the reporter's scale (~200), or test with fewer to find the minimum trigger
  4. Click Save
  5. Observe app crash with brief error flash

## Environment
Not specified — reproduce on all supported platforms. Reporter's list contains ~200 tasks imported from CSV.

## Severity: high

## Impact
Users with imported data cannot save their work at all, leading to repeated data loss. The reporter has lost work multiple times. Any user who imports CSV data with typographic characters will hit this.

## Recommended Fix
Investigate the save/serialization code path for Unicode handling. Likely candidates: (1) character encoding mismatch during serialization, (2) unescaped characters breaking a format like JSON or XML, (3) the CSV import not normalizing Unicode on ingest. Fix should ensure all valid Unicode is handled in the save path. Additionally, the CSV importer should sanitize or normalize non-ASCII punctuation on import. The crash should also be caught gracefully with a user-visible error rather than an abrupt close.

## Proposed Test Case
Create a task list containing task names with curly quotes, em dashes, en dashes, ellipsis characters, and other common typographic Unicode. Verify that save completes without error. Also test round-trip: import CSV with these characters, save, close, reopen, and confirm data integrity.

## Information Gaps
- Exact error message (flashes too fast for reporter to read — check application logs or crash reports)
- Specific platform and app version the reporter is using
- Whether the issue is character-specific or also scale-dependent (does a list of 5 tasks with curly quotes also crash?)
