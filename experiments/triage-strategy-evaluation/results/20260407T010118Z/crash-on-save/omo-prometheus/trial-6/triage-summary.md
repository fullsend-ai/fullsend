# Triage Summary

**Title:** App crashes on save when task list contains non-ASCII characters from CSV import (smart quotes, em-dashes)

## Problem
TaskFlow crashes immediately (app closes) every time the user hits Save in the toolbar when the task list contains tasks imported from a CSV file that originated from a Word document. The CSV data contains Word-style typographic characters such as smart quotes (“”), em-dashes (—), and similar non-ASCII glyphs. A flashing error dialog mentioning 'encoding' appears briefly before the app closes. Saving a new list with plain ASCII tasks works fine.

## Root Cause Hypothesis
The save/serialization path is not handling non-ASCII characters (specifically Windows-1252 or UTF-8 multi-byte characters like smart quotes and em-dashes) correctly. Most likely the save routine assumes ASCII or uses a strict encoding mode that throws a fatal exception on encountering these characters, rather than encoding them properly as UTF-8. This may have been introduced or exposed in a recent update (the user reports it worked before, timeline ~1-2 weeks).

## Reproduction Steps
  1. Create a CSV file containing tasks with Word-style smart quotes (“ ”), em-dashes (—), or other non-ASCII typographic characters
  2. Import the CSV into TaskFlow as a task list
  3. Click Save in the toolbar
  4. Observe: app crashes with a brief encoding-related error dialog

## Environment
macOS 14.2 (Sonoma), TaskFlow 2.3.1. Task list contains ~200 tasks, many imported from CSV sourced from a Word document.

## Severity: high

## Impact
User is completely unable to save their primary task list (~200 tasks). All edits are lost on every save attempt. No workaround exists short of removing the imported tasks. Any user who imports CSV data containing non-ASCII characters would hit this.

## Recommended Fix
Investigate the save/serialization code path for encoding handling. Ensure all file writes use UTF-8 encoding (or at minimum handle multi-byte and extended-ASCII characters gracefully). Check for recent changes to the save path in the last 2-3 weeks that may have introduced a regression. Also verify the CSV import path — it may be ingesting data in one encoding but the save path may assume another.

## Proposed Test Case
Create a task list containing tasks with smart quotes (“” ‘’), em-dashes (—), accented characters (é, ç, ñ), and emoji. Verify that saving and re-loading the list preserves all characters without errors or crashes.

## Information Gaps
- Exact crash log or stack trace (reporter declined to retrieve diagnostic reports)
- Exact text of the flashing error dialog
- Whether a specific recent TaskFlow update introduced this regression or if it correlates purely with the CSV import timing
- The specific encoding of the original CSV file (likely Windows-1252 from Word)
