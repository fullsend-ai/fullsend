# Triage Summary

**Title:** App crashes on save with encoding error when task list exceeds ~200 items containing typographic characters from CSV import

## Problem
TaskFlow v2.3.1 crashes immediately (app closes) when the user saves a task list containing approximately 200 tasks that were imported from an Excel-exported CSV file. A brief dialog flash mentioning 'encoding' appears before the crash. The issue does not occur with smaller subsets (~15 tasks) of the same data, including rows containing the same typographic characters.

## Root Cause Hypothesis
The save/serialization path likely has an encoding conversion issue (e.g., converting Windows-1252 typographic characters like curly quotes and em-dashes to UTF-8) that manifests only when the data volume exceeds a threshold — possibly a fixed-size buffer overflow in the encoding conversion routine, or an O(n²) operation in character escaping that causes a timeout or memory issue at scale.

## Reproduction Steps
  1. Install TaskFlow v2.3.1
  2. Create or obtain a CSV file exported from Excel containing ~200 task rows, with some task names containing typographic characters (curly quotes, em-dashes)
  3. Import the CSV into TaskFlow
  4. Click Save
  5. Observe: brief encoding-related dialog flash, then app closes entirely

## Environment
TaskFlow v2.3.1. CSV source: Excel export. OS not confirmed but likely desktop (Windows or macOS given Excel workflow).

## Severity: high

## Impact
Users with large imported task lists cannot save at all, resulting in complete data loss of any changes. Any user who imports a sufficiently large CSV from Excel with typographic characters will hit this. No known workaround reported.

## Recommended Fix
Investigate the save/serialization code path for encoding handling. Check for: (1) fixed-size buffers in character encoding conversion, (2) improper handling of Windows-1252 characters (curly quotes U+2018/U+2019, em-dashes U+2014) during serialization, (3) any size-dependent behavior in the encoding layer such as chunked processing or buffer allocation. Add a try-catch around the encoding step to surface the actual error instead of crashing. Consider normalizing all imported text to UTF-8 at import time rather than at save time.

## Proposed Test Case
Create a test that programmatically generates a task list of 250+ items where ~20% contain curly quotes (U+2018, U+2019) and em-dashes (U+2014), then invokes the save routine and asserts it completes without error and the saved file round-trips correctly with characters preserved.

## Information Gaps
- Exact size threshold where the crash begins (somewhere between 15 and 200 tasks)
- Operating system of the reporter
- Whether the crash also occurs on auto-save or only manual save
- Full text of the encoding error dialog (only partially glimpsed)
