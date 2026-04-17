# Triage Summary

**Title:** App crashes on save when task list contains CSV-imported tasks (encoding error)

## Problem
When a user imports tasks from a CSV file and then attempts to save a task list containing those imported tasks, the application crashes. A dialog referencing 'encoding' flashes briefly before the crash. Removing the CSV-imported tasks from the list allows saving to work normally.

## Root Cause Hypothesis
The CSV import path is ingesting task data without normalizing character encoding. The imported data likely contains non-UTF-8 characters (e.g., Latin-1, Windows-1252 smart quotes, BOM markers, or other special characters) that the save/serialization code does not handle, causing an unhandled encoding exception.

## Reproduction Steps
  1. Prepare a CSV file with ~200 tasks (likely containing non-ASCII characters such as smart quotes, accented characters, or a BOM)
  2. Import the CSV file into TaskFlow
  3. Create or open a task list containing the imported tasks
  4. Click the 'Save' button in the toolbar
  5. Observe the encoding error dialog flash and the application crash

## Environment
Not specified — likely desktop application. The issue is data-dependent rather than environment-dependent.

## Severity: high

## Impact
Users who import tasks from CSV files lose unsaved work when the app crashes on save. This blocks a core workflow (CSV import + save) and causes data loss.

## Recommended Fix
1. Add a try/catch around the save serialization path to surface the full encoding error instead of crashing. 2. Audit the CSV import code to ensure it detects and normalizes input encoding to UTF-8 (or the application's internal encoding) at import time. 3. Add defensive encoding handling in the save path so malformed characters are replaced or escaped rather than causing an unhandled exception.

## Proposed Test Case
Import a CSV file containing non-ASCII characters (smart quotes, accented characters, BOM marker, mixed encodings) into a task list with 200+ tasks, then save. Verify the save completes without crashing and the data round-trips correctly.

## Information Gaps
- Exact encoding of the reporter's CSV file (UTF-8 with BOM, Latin-1, Windows-1252, etc.)
- Full text of the encoding error dialog
- Operating system and application version
- Whether this affects all CSV files or only specific ones
