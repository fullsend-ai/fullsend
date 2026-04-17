# Triage Summary

**Title:** App crashes on save when task list contains Excel-imported tasks with typographic characters (encoding error, size-dependent)

## Problem
Saving a task list crashes the application (immediate close with a brief 'encoding' error dialog) when the list contains tasks imported from an Excel-generated CSV file. The imported tasks contain Windows-1252 typographic characters (curly/smart quotes, em-dashes). The crash only occurs when the imported task count exceeds roughly 50; smaller sets save successfully.

## Root Cause Hypothesis
The save/serialization path likely assumes UTF-8 (or ASCII) encoding but the Excel CSV import preserves Windows-1252 encoded characters (CP1252 curly quotes U+201C/U+201D, em-dashes U+2014). When the save routine processes these characters at scale, it hits an unhandled encoding exception — possibly in a batched or buffered write operation, which explains the size-dependent threshold. The error is not caught gracefully, causing the app to crash instead of surfacing a user-facing error.

## Reproduction Steps
  1. Create a CSV file in Excel with ~200 task entries, ensuring some task names contain curly/smart quotes and em-dashes (type in Word or Excel and let auto-correct produce them)
  2. Import the CSV into TaskFlow using the CSV import feature
  3. Verify the tasks appear in the task list
  4. Click the Save button in the toolbar
  5. Observe: app crashes with a brief encoding-related error dialog

## Environment
No specific OS/version provided, but the issue is data-dependent rather than environment-dependent. Excel on Windows is the source of the CSV, producing Windows-1252 encoded characters.

## Severity: high

## Impact
Any user who imports tasks from Excel-generated CSVs containing typographic characters risks data loss — the app crashes on save with no recovery. The crash is deterministic once the threshold is crossed, and users lose any unsaved work. This likely affects a significant portion of users migrating from spreadsheet-based workflows.

## Recommended Fix
1. Investigate the save serialization path for encoding handling — check whether it enforces or assumes a specific encoding. 2. Ensure the CSV import normalizes characters to UTF-8 at import time, or that the save path handles multi-byte/non-ASCII characters correctly. 3. Add a try/catch around the save operation so encoding errors surface as user-facing error messages rather than crashes. 4. Investigate the size-dependent behavior — check if the save uses batched writes or a fixed buffer that interacts with multi-byte character expansion. 5. Consider adding an import-time sanitization option that replaces typographic characters with ASCII equivalents.

## Proposed Test Case
Create a task list with 200+ tasks where task names contain Windows-1252 typographic characters (curly quotes, em-dashes, ellipsis). Save the list and verify: (a) no crash occurs, (b) the characters are preserved correctly in the saved output, and (c) the file can be reloaded without data corruption. Also test the boundary condition around 50 tasks to ensure the fix isn't size-dependent.

## Information Gaps
- Exact error message (the dialog flashes too fast to read fully — application logs may capture it)
- Whether the app uses a specific serialization format (JSON, SQLite, custom binary) for the save file
- Exact size threshold where the crash begins (reporter estimates ~50 but hasn't tested precisely)
- Whether auto-save (if it exists) is also affected or only the manual toolbar save
