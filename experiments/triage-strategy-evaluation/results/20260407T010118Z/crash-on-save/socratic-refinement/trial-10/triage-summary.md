# Triage Summary

**Title:** App crashes on save with encoding error after CSV import containing typographic characters

## Problem
TaskFlow crashes (full app close) every time the user saves a task list that was populated via CSV import. A brief error dialog flashes mentioning 'encoding' before the app dies. The crash is 100% reproducible and began immediately after the CSV import. The CSV contains typographic characters such as smart quotes and em dashes.

## Root Cause Hypothesis
The CSV import ingested text containing non-UTF-8 or mixed-encoding characters (e.g., Windows-1252 smart quotes and em dashes). The save/serialization path likely assumes clean UTF-8 and hits an unhandled encoding exception when it encounters these characters, causing the app to crash instead of gracefully handling or sanitizing the input.

## Reproduction Steps
  1. Create a CSV file containing tasks with typographic characters (curly/smart quotes, em dashes, etc. — common in text copied from Word or macOS)
  2. Import the CSV into TaskFlow
  3. Edit the task list (or leave as-is)
  4. Click Save
  5. Observe: app crashes with a brief encoding-related error dialog

## Environment
macOS, TaskFlow ~v2.3.x

## Severity: high

## Impact
User is completely unable to save their work — 200 tasks at risk of data loss on every edit. Any user who imports CSV data with non-ASCII characters will hit this. The crash is not intermittent; it blocks all saves.

## Recommended Fix
Investigate the save/serialization code path for encoding assumptions. Likely needs to: (1) ensure the save path handles or normalizes non-UTF-8 characters gracefully, (2) consider sanitizing or re-encoding imported CSV data at import time, and (3) add a proper error dialog with recovery instead of crashing. Check the CSV import path as well — it may need to detect source encoding and transcode to UTF-8 on ingest.

## Proposed Test Case
Import a CSV containing a mix of smart quotes (U+2018, U+2019, U+201C, U+201D), em dashes (U+2014), and other common Windows-1252 characters into a task list. Verify that saving completes without error and that the characters are preserved (or gracefully normalized) in the saved output.

## Information Gaps
- Exact TaskFlow version (reporter said '2.3 something')
- Exact text of the flashed error dialog
- Original CSV file encoding and content sample
