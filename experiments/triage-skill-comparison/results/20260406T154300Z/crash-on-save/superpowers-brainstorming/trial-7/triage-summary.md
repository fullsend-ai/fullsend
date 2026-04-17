# Triage Summary

**Title:** App crashes on save with encoding error when task list contains CSV-imported tasks

## Problem
After importing tasks from a CSV file, clicking the toolbar Save button to save the task list causes the app to crash. A brief error dialog referencing 'encoding' flashes before the app closes. The crash is 100% reproducible whenever the imported tasks are present in the list, persists across app restarts, and is eliminated by removing the imported tasks.

## Root Cause Hypothesis
The CSV import path accepts text data with an encoding (e.g., Latin-1, Windows-1252, or UTF-8 with BOM) or characters (e.g., special/non-ASCII characters, null bytes) that the save/serialization layer cannot handle. When the save routine iterates over task data to serialize it, it encounters these characters and throws an unhandled encoding exception, crashing the app.

## Reproduction Steps
  1. Create or obtain a CSV file with ~200 tasks (likely containing non-ASCII or special characters)
  2. Import the CSV file into TaskFlow using the CSV import feature
  3. Click the 'Save' button in the toolbar to save the task list
  4. Observe the brief 'encoding' error dialog followed by the app crashing

## Environment
Not platform-specific based on available information. The trigger is the CSV file content/encoding, not the OS or app version.

## Severity: high

## Impact
Any user who imports tasks from CSV risks persistent crashes on save, leading to data loss and inability to use the app until imported tasks are manually removed. This blocks a core workflow (CSV import → continued use).

## Recommended Fix
1. Add encoding detection/normalization in the CSV import path (e.g., detect source encoding and transcode to UTF-8 on import). 2. Add defensive encoding handling in the save/serialization path so malformed characters are handled gracefully rather than crashing. 3. Ensure the error dialog is non-fatal and stays visible long enough for users to read and report it.

## Proposed Test Case
Import a CSV file containing mixed encodings (UTF-8, Latin-1, Windows-1252) and special characters (em-dashes, accented characters, null bytes, BOM markers), then verify that (a) import succeeds or reports clear errors per-row, and (b) saving the task list after import completes without crashing.

## Information Gaps
- The exact encoding of the reporter's CSV file is unknown — reproducing with various encodings should cover this
- The full error message was not captured — adding proper error logging or a persistent error dialog would help future debugging
