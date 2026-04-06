# Triage Summary

**Title:** App crashes on save when task list contains smart punctuation from Excel CSV import

## Problem
TaskFlow crashes immediately (app closes, no freeze) when saving a task list that contains tasks imported from an Excel-exported CSV file. A dialog briefly flashes mentioning 'encoding' before the app shuts down. The crash only occurs after importing CSV data containing smart/typographic punctuation (em-dashes, curly quotes, etc.). Saving works fine for plain-text tasks and for the same list before the import.

## Root Cause Hypothesis
TaskFlow's save/serialization logic assumes a specific encoding (likely UTF-8 or ASCII) but the Excel-exported CSV contains Windows-1252 (cp1252) encoded characters — specifically smart quotes (U+2018/U+2019/U+201C/U+201D) and em-dashes (U+2014) that Excel auto-formats. When the save routine attempts to encode these characters, it hits an unhandled encoding error and crashes instead of gracefully handling or transcoding them.

## Reproduction Steps
  1. Open TaskFlow and create or open any task list
  2. Prepare a CSV file exported from Excel containing task names with smart punctuation (curly quotes, em-dashes — characters Excel auto-formats)
  3. Import the CSV into the task list
  4. Click Save in the toolbar
  5. Observe: a dialog flashes briefly mentioning 'encoding', then the app closes

## Environment
TaskFlow desktop app; CSV source is Microsoft Excel export; task list contains ~200 tasks; specific version not confirmed but was working before the import

## Severity: high

## Impact
Any user who imports CSV data from Excel (or similar tools that produce smart punctuation) will be unable to save their task list, resulting in data loss. This is a data-loss scenario on a common workflow (Excel CSV import).

## Recommended Fix
Investigate the save serialization path for encoding handling. The fix should: (1) ensure the CSV import normalizes or correctly preserves character encoding on ingest, transcoding cp1252 characters to UTF-8; (2) ensure the save routine handles non-ASCII characters gracefully rather than crashing — either by supporting UTF-8 throughout or by replacing/escaping unsupported characters with a user warning; (3) wrap the save encoding step in proper error handling so an encoding failure shows a user-friendly error instead of crashing the app.

## Proposed Test Case
Import a CSV containing task names with Windows-1252 smart punctuation (curly quotes: ‘ ’ “ ”, em-dash: —, ellipsis: …) into a task list and verify that saving completes successfully without crash, and that the characters are preserved or cleanly normalized in the saved output.

## Information Gaps
- Exact TaskFlow version and platform (Windows/Mac/Linux)
- Full text of the encoding error dialog (it flashes too quickly to read)
- Whether the crash also occurs when exporting/saving to different formats, or only the default save
