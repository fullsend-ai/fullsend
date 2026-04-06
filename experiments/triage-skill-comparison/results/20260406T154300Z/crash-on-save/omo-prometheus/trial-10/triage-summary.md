# Triage Summary

**Title:** App crashes on save when task list contains CSV-imported tasks (encoding error)

## Problem
When a user imports tasks from a CSV file and then attempts to save the task list via the toolbar Save button, the application crashes (closes entirely). A briefly-visible error dialog references an 'encoding' issue. The crash does not occur if the imported tasks are removed first, confirming the imported data is the trigger.

## Root Cause Hypothesis
The CSV import path accepts data with characters or encoding (e.g., non-UTF-8, BOM markers, or special/multi-byte characters) that the save/serialization code path cannot handle. The save routine likely assumes a consistent encoding and fails — probably an unhandled exception in the serialization layer that propagates to an uncaught crash.

## Reproduction Steps
  1. Create or open a task list in TaskFlow
  2. Import tasks from a CSV file (reporter had ~200 tasks after import)
  3. Click Save in the toolbar
  4. Observe: app crashes with a brief encoding-related error dialog

## Environment
Not specified (reporter did not mention OS/version/platform). The issue is data-dependent rather than environment-dependent — triggered by CSV import content.

## Severity: high

## Impact
Any user who imports tasks from CSV risks a crash on every subsequent save, leading to repeated data loss. The workaround (removing imported tasks) negates the value of the import feature entirely.

## Recommended Fix
1. Inspect the save/serialization code path for encoding assumptions — likely it expects UTF-8 but the CSV import does not normalize encoding on ingest. 2. Add encoding normalization (e.g., detect and convert to UTF-8) in the CSV import pipeline. 3. Add a try/catch around the save serialization that surfaces the full error to the user instead of crashing. 4. Consider validating imported data at import time and warning the user about encoding issues before they hit save.

## Proposed Test Case
Import a CSV file containing non-ASCII characters (e.g., accented characters, CJK characters, emoji, BOM markers, and mixed encodings like Latin-1 mixed with UTF-8). Save the task list and verify it completes without error. Verify the saved data round-trips correctly when reopened.

## Information Gaps
- Exact CSV file encoding and content that triggers the crash (developer can reproduce with various non-UTF-8 CSVs)
- Full error message from the crash dialog
- User's OS and TaskFlow version
- Whether this affects all CSV imports or only CSVs from specific sources/tools
