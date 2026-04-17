# Triage Summary

**Title:** App crashes on save when task list contains CSV-imported tasks with special characters (smart quotes, dashes)

## Problem
TaskFlow crashes immediately on save (app closes, brief unreadable error dialog) when a task list contains tasks imported from a CSV file. The CSV data includes special punctuation such as smart quotes and em-dashes. Small lists and non-imported tasks save without issue. The crash is 100% reproducible by the reporter: removing imported tasks allows save; re-importing breaks it again.

## Root Cause Hypothesis
The CSV import path likely preserves raw Unicode characters (smart quotes U+2018/U+2019, em-dashes U+2014, etc.) that the save/serialization code does not handle correctly. This could be an encoding issue in the serializer (e.g., assuming ASCII-safe input), an unescaped character breaking a structured format (JSON/XML), or a buffer size issue when serializing large lists with multi-byte characters. The crash (app closes entirely rather than showing an error) suggests an unhandled exception in the save path with no top-level catch.

## Reproduction Steps
  1. Create or open a task list in TaskFlow on Mac (version 2.3.x)
  2. Prepare a CSV file with ~200 tasks containing special punctuation: smart quotes (‘ ’ “ ”), em-dashes (—), and similar non-ASCII characters
  3. Import the CSV into the task list
  4. Click Save
  5. Observe: app crashes immediately (closes with a brief error flash)

## Environment
macOS, TaskFlow version 2.3.x (exact minor version unknown)

## Severity: high

## Impact
Any user who imports CSV data containing non-ASCII punctuation into a task list will experience a hard crash on save, with potential data loss. This is especially likely for users migrating from other tools, as exported CSVs commonly contain smart quotes and typographic characters.

## Recommended Fix
1. Inspect the save/serialization code path for encoding assumptions — look for places where task text is written without proper Unicode handling. 2. Inspect the CSV import code for whether it sanitizes or normalizes special characters on ingest. 3. Add a top-level exception handler around the save operation so unhandled errors display a proper error dialog instead of crashing the app. 4. Consider normalizing smart quotes and typographic dashes to their ASCII equivalents during CSV import, or ensuring the serializer handles the full Unicode range.

## Proposed Test Case
Import a CSV containing task names with smart quotes (‘example’), em-dashes (task — description), and other non-ASCII punctuation into a list of 200+ tasks. Verify that saving completes successfully without crashing, and that the saved data round-trips correctly (special characters are preserved or consistently normalized).

## Information Gaps
- Exact TaskFlow version (reporter said '2.3 something')
- Actual crash log from Console.app (reporter declined to check)
- Sample CSV file for exact reproduction (not provided, but character types are described)
- Whether the crash threshold is count-dependent (e.g., 100 imported tasks vs. 200) or purely character-dependent
