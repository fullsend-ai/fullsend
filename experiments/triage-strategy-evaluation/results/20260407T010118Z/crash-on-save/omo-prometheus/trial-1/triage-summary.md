# Triage Summary

**Title:** Desktop app crashes on save when task list contains CSV-imported data with non-ASCII characters

## Problem
The desktop app (macOS, v2.3.x) crashes immediately — full app close — when the user saves a task list containing ~200 tasks that were previously imported from a CSV file. A brief error referencing 'encoding' flashes before the app dies. Smaller lists without the imported data save successfully.

## Root Cause Hypothesis
The CSV import path likely ingests data as raw bytes or in a legacy encoding (e.g., Windows-1252 / Latin-1) without normalizing to UTF-8. Characters such as smart quotes (U+2018/U+2019), em dashes (U+2014), and other typographic characters from the source application are stored in an inconsistent encoding. The save/serialization routine then fails when it encounters these bytes — either a UTF-8 encode call throws on invalid sequences, or a JSON/XML serializer rejects them, causing an unhandled exception that crashes the app.

## Reproduction Steps
  1. Create or obtain a CSV file containing tasks with typographic characters — smart quotes (‘ ’ “ ”), em dashes (—), or other non-ASCII punctuation (easily produced by exporting from Excel or Word)
  2. Import the CSV into TaskFlow to create a task list with at least ~100–200 tasks
  3. Open the task list in the desktop app on macOS
  4. Click Save
  5. Observe: app crashes (full close) with a brief encoding-related error

## Environment
macOS, TaskFlow desktop app v2.3.x

## Severity: high

## Impact
Any user who imports tasks from external sources (CSV, spreadsheet exports) risks a hard crash on every save attempt, resulting in complete inability to persist changes. The user's existing data is effectively locked — visible but unsaveable. No known workaround short of manually removing offending characters.

## Recommended Fix
1. **Immediate fix:** Add encoding error handling in the save/serialization path — catch encoding exceptions, normalize characters to UTF-8 (or replace/escape unencodable characters gracefully), and surface a user-visible error instead of crashing. 2. **Root cause fix:** Audit the CSV import path to ensure all ingested text is normalized to UTF-8 at import time, converting from detected source encodings (Windows-1252, Latin-1, etc.). 3. **Data migration:** Provide a one-time migration or repair routine that re-encodes existing task data to valid UTF-8 so affected users can save without re-importing.

## Proposed Test Case
Import a CSV containing tasks with smart quotes (‘test’), em dashes (foo — bar), accented characters (éèê), and emoji (😀) into a list of 200+ tasks. Verify that (a) the import succeeds, (b) saving the list succeeds, (c) re-opening the list preserves all characters correctly, and (d) no unhandled exceptions are thrown.

## Information Gaps
- Exact app version beyond '2.3 something'
- Exact error message (reporter could not read it before app closed)
- Crash log contents (reporter could not locate log directory)
- Specific source application that produced the CSV
