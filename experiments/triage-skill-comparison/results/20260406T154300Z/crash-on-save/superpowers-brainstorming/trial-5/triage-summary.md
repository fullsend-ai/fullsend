# Triage Summary

**Title:** App crashes on save when task list contains CSV-imported tasks due to encoding error

## Problem
Saving a task list that contains tasks imported from a CSV file causes the application to crash with an encoding-related error. The crash is deterministic (100% reproduction rate) and affects only lists containing CSV-imported tasks. Smaller or manually-created lists save without issue.

## Root Cause Hypothesis
The CSV import path does not normalize character encoding (e.g., the CSV may be in Latin-1, Windows-1252, or contain BOM/invalid UTF-8 sequences). The imported task data is stored in memory as-is, and the save/serialization routine (likely expecting valid UTF-8) crashes when it encounters bytes it cannot encode.

## Reproduction Steps
  1. Create or obtain a CSV file with ~200 tasks (likely containing non-ASCII characters, special punctuation, or mixed encoding)
  2. Import the CSV into TaskFlow as a task list
  3. Click 'Save' in the toolbar
  4. Observe: app crashes with a briefly-visible encoding error dialog

## Environment
Not yet confirmed — reporter did not specify OS, browser, or app version. However, the bug is encoding-related and likely platform-independent.

## Severity: high

## Impact
Any user who imports tasks from CSV files risks data loss — the app crashes on save with no recovery path, and work done after import cannot be persisted.

## Recommended Fix
1. Inspect the CSV import code: ensure incoming data is decoded and re-encoded as UTF-8 (or the app's canonical encoding) at import time, with invalid byte sequences handled gracefully (replace or reject with a user-visible warning). 2. Inspect the save/serialization path: add a try/catch around encoding operations so that an encoding failure surfaces a readable error message instead of crashing the app. 3. Validate with a CSV containing mixed encodings, BOM markers, and non-ASCII characters (em-dashes, accented characters, smart quotes).

## Proposed Test Case
Import a CSV file containing tasks with non-UTF-8 characters (e.g., Latin-1 accented names, Windows-1252 smart quotes, and raw 0x80–0xFF bytes). Verify that (a) import either succeeds with normalized encoding or warns about invalid characters, and (b) saving the resulting task list succeeds without crashing.

## Information Gaps
- Exact encoding of the original CSV file
- Full text of the error dialog (reporter may still provide a screen recording)
- OS, browser/app version, and platform details
