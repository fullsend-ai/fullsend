# Triage Summary

**Title:** App crashes on save when task list contains typographic characters (curly quotes, em dashes) from CSV import

## Problem
Saving a task list that was populated via CSV import causes the app to crash (closes entirely). The crash occurs during serialization and appears to be an encoding error triggered by non-ASCII typographic characters such as curly/smart quotes and em dashes present in imported task names.

## Root Cause Hypothesis
The save/serialization code path does not correctly handle non-ASCII typographic characters (e.g., U+2018/U+2019 curly quotes, U+2013/U+2014 em/en dashes). These characters likely entered via CSV import and trigger an unhandled encoding exception during file write or data serialization, causing the app to crash instead of gracefully handling the error. The brief error dialog mentioning 'encoding' supports this. Likely candidates: the save routine assumes ASCII or a narrow encoding, or a serialization library is misconfigured.

## Reproduction Steps
  1. Create or obtain a CSV file containing task names with typographic characters (curly quotes like ‘ ’ “ ” and em dashes like —)
  2. Import the CSV into TaskFlow to populate a task list (aim for ~200 tasks to match reporter's scenario, though fewer may reproduce it)
  3. Attempt to save the task list
  4. Observe: app crashes (closes entirely) with a brief encoding-related error dialog

## Environment
Not specified. Reproduction should be attempted across all supported platforms. The issue is likely platform-independent since it stems from character encoding handling in the save path.

## Severity: high

## Impact
Any user who imports tasks containing non-ASCII typographic characters (common when exporting from tools like Excel, Google Sheets, or Notion) will be unable to save their task list. The crash causes data loss for unsaved changes. The reporter has been blocked for days.

## Recommended Fix
1. Inspect the save/serialization code path for encoding assumptions — ensure it uses UTF-8 throughout. 2. Check the CSV import path to see if it normalizes or validates character encoding on ingest. 3. Add graceful error handling around the save operation so encoding failures surface as user-visible errors rather than crashes. 4. Consider normalizing typographic characters on import (e.g., replacing curly quotes with straight quotes) or ensuring the full pipeline is Unicode-safe.

## Proposed Test Case
Create a task list containing task names with curly single quotes (‘’), curly double quotes (“”), em dashes (—), en dashes (–), and other common non-ASCII punctuation. Verify that saving completes without error and that the characters round-trip correctly when the list is reloaded.

## Information Gaps
- Exact error message from the crash dialog (reporter could not read it in time)
- Operating system and app version
- Original CSV file and the tool that generated it
- Whether the issue is strictly about character count/list size or purely about character encoding (the small-list test used new tasks, not a subset of imported ones)
