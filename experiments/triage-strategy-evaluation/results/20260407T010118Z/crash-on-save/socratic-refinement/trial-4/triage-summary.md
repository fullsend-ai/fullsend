# Triage Summary

**Title:** Save crashes when task list contains imported CSV data with Word special characters

## Problem
The application crashes immediately on save when the task list contains tasks imported from a CSV file that was originally sourced from a Microsoft Word document. The crash appears as the app closing abruptly with a briefly-flashing error dialog. The reporter cannot save their work and loses progress.

## Root Cause Hypothesis
The save/serialization path likely fails on non-ASCII or Word-specific characters (smart quotes like “”, em dashes —, curly apostrophes ’, etc.) present in imported task names. The serializer or file writer probably lacks proper Unicode handling or character encoding, causing an unhandled exception during save that crashes the app instead of being caught gracefully.

## Reproduction Steps
  1. Create a CSV file with task names containing Word-style special characters (smart/curly quotes, em dashes, etc.)
  2. Import the CSV into TaskFlow using the spreadsheet import feature
  3. Attempt to save the task list
  4. Observe: app crashes with a briefly-flashing error dialog

## Environment
Not specified; appears to be a desktop application. Issue is data-dependent rather than environment-dependent.

## Severity: high

## Impact
Any user who imports tasks from CSV files containing non-ASCII characters (common when data originates from Word or similar tools) will be unable to save their work. The crash causes data loss. This likely affects a meaningful subset of users who rely on the import feature.

## Recommended Fix
1. Investigate the save/serialization code path for character encoding issues — ensure it handles the full Unicode range, especially common Word special characters. 2. Add proper error handling around save so that encoding failures surface as user-visible error messages rather than crashing the app. 3. Consider sanitizing or normalizing special characters during CSV import (e.g., replacing smart quotes with straight quotes) with a user notification. 4. As an immediate mitigation, the reporter can likely work around this by replacing special characters in their CSV before re-importing.

## Proposed Test Case
Import a CSV containing task names with Word-specific Unicode characters (smart quotes “ ” ‘ ’, em dash —, ellipsis …, etc.) and verify that (a) the import succeeds, (b) saving succeeds without crashing, and (c) the characters are preserved or gracefully normalized in the saved output.

## Information Gaps
- Exact error message from the flashing dialog (developer can capture via logs or debugger during reproduction)
- Specific application version and platform (desktop OS)
- Whether the issue is in the file-write serializer, an in-memory data structure, or a database layer
