# Triage Summary

**Title:** App crashes on save when task list contains Unicode characters from CSV import

## Problem
The TaskFlow Mac desktop app crashes (force-closes) immediately when the user saves a task list that was populated via CSV import. The imported tasks contain non-ASCII characters such as curly/smart quotes and em dashes. A brief error dialog mentioning 'encoding' flashes before the app terminates. Saving works normally for task lists created manually without imported data.

## Root Cause Hypothesis
The save/serialization path does not handle non-ASCII Unicode characters (smart quotes, em dashes, etc.) correctly. The CSV import likely ingests these characters verbatim, but the save routine either assumes ASCII encoding, uses a codec that cannot represent these characters, or fails to encode them properly — resulting in an unhandled encoding exception that crashes the app.

## Reproduction Steps
  1. Create a CSV file with task data containing Unicode characters: smart/curly quotes (“ ” ‘ ’), em dashes (—), and other non-ASCII punctuation
  2. Import the CSV file into TaskFlow on Mac desktop app
  3. Verify the tasks appear in the list after import
  4. Click Save
  5. Observe: app crashes immediately with a brief encoding-related error dialog

## Environment
Mac desktop app (macOS version unknown, app version unknown)

## Severity: high

## Impact
User is completely blocked from saving a 200-task list representing real work. Data loss occurs on every crash since unsaved changes are discarded. Any user who imports CSV data with non-ASCII characters will hit this.

## Recommended Fix
Investigate the save/serialization code path for encoding assumptions. Ensure all string data is encoded as UTF-8 (or the appropriate Unicode encoding) before writing. Add proper error handling around the save operation so encoding failures surface a user-facing error message rather than crashing the app. Also audit the CSV import path to either normalize Unicode characters on ingest or ensure downstream code handles them.

## Proposed Test Case
Create a task list containing entries with smart quotes (“”), em dashes (—), accented characters (é, ñ), and emoji. Verify that saving and re-loading the list preserves all characters without errors or crashes.

## Information Gaps
- Exact macOS version and TaskFlow app version
- Full error message / stack trace from the crash (reporter unable to retrieve logs)
- Original CSV file structure and content (deleted by reporter)
- Whether the issue is specific to certain Unicode characters or affects all non-ASCII text
- Whether the crash occurs with a smaller subset of the imported tasks (e.g., 10 rows with special characters)
