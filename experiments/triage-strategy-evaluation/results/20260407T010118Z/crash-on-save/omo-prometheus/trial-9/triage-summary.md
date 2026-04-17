# Triage Summary

**Title:** Save crashes with encoding error when task list contains special characters from CSV import

## Problem
The application crashes (closes completely) when the user attempts to save a task list containing tasks that were imported from a CSV file. These tasks contain non-standard characters such as curly/smart quotes and em-dashes. A brief error dialog referencing 'encoding' flashes before the crash. Small lists with plain-text tasks save without issue.

## Root Cause Hypothesis
The save/serialization path does not handle non-ASCII or non-UTF-8 characters gracefully. Tasks imported from CSV likely contain Windows-1252 or similar encoded characters (smart quotes U+2018/U+2019, em-dashes U+2014) that cause an unhandled encoding exception during save — either when serializing to JSON/storage format or when writing to disk. The lack of error handling causes the app to crash instead of surfacing a recoverable error.

## Reproduction Steps
  1. Create or import a task list from a CSV file containing smart/curly quotes (‘ ’ “ ”), em-dashes (—), or other non-ASCII characters
  2. Ensure the list has a non-trivial number of such tasks (reporter had ~200, but the trigger is likely character content, not count)
  3. Click Save
  4. Observe: app crashes with a brief encoding-related error dialog

## Environment
Not specified (reporter did not provide OS/browser/app version). Reproduction likely environment-independent since it is a data-driven encoding bug.

## Severity: high

## Impact
Any user who imports tasks from external sources (CSV, Word, Google Docs) with non-ASCII characters cannot save their work. Data loss occurs on every save attempt. No known workaround short of manually stripping special characters from all tasks.

## Recommended Fix
1. Investigate the save/serialization code path for encoding assumptions — look for implicit ASCII encoding or missing UTF-8 declarations. 2. Ensure all string serialization uses UTF-8 throughout. 3. Add a try/catch around the save operation so encoding failures surface as a user-facing error message rather than a crash. 4. Consider sanitizing or normalizing special characters on CSV import (e.g., converting smart quotes to straight quotes) as a defense-in-depth measure.

## Proposed Test Case
Create a task list containing tasks with smart quotes (‘example’), em-dashes (—), accented characters (é, ñ), and emoji. Save the list. Verify: save completes successfully, and reloading the list preserves all special characters without corruption.

## Information Gaps
- Exact app version and platform (desktop vs. web, OS, browser) — unlikely to change the fix direction since this is a data-driven bug
- Exact error message from the crash dialog — would help pinpoint the exception type but developer can reproduce
- Whether the CSV import itself introduced encoding corruption vs. the save path failing on valid Unicode — developer should check both paths
