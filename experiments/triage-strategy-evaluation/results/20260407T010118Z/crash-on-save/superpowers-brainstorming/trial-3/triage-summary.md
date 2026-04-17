# Triage Summary

**Title:** App crashes on save with ~200 tasks after spreadsheet import

## Problem
The app force-closes when the user clicks Save in the toolbar while editing a task list containing approximately 200 tasks. A brief error message flashes before the app closes. The issue began roughly one week ago, around the same time the user imported a large number of tasks from a spreadsheet. Save was working normally prior to the import.

## Root Cause Hypothesis
The spreadsheet import likely introduced data that the save/serialization code path cannot handle — possible causes include special characters, unexpected field formats, excessively long text, or data type mismatches (e.g., dates or numbers stored as strings). The save operation hits an unhandled exception when serializing these records, crashing the process.

## Reproduction Steps
  1. Create a project in TaskFlow
  2. Import a large set of tasks (~200) from a spreadsheet (CSV or similar)
  3. Open the task list for editing
  4. Click Save in the toolbar
  5. Observe: app crashes / closes with a brief error flash

## Environment
Not specified (reporter did not mention OS or app version). The issue is likely platform-independent since it appears to be a data-triggered crash.

## Severity: high

## Impact
The user cannot save any edits to their task list, causing repeated data loss. This blocks all productive use of the project. Any user who has imported a large number of tasks from a spreadsheet may be affected.

## Recommended Fix
1. Add error handling/logging around the save serialization path so crashes produce a persistent error message instead of silently closing. 2. Inspect the imported task data for malformed fields (special characters, encoding issues, type mismatches, oversized values). 3. Check whether the save operation has a size or memory limit being exceeded at ~200 records. 4. Add input validation/sanitization to the spreadsheet import feature to prevent bad data from entering the system.

## Proposed Test Case
Import a spreadsheet containing 200+ tasks with a variety of edge-case data (special characters, Unicode, very long strings, empty fields, numeric strings in text fields) and verify that saving the task list completes without error. Additionally, verify that if serialization does fail, a clear error message is displayed and the app does not crash.

## Information Gaps
- Exact error message (flashes too fast for reporter to read — crash logs would reveal this)
- Specific spreadsheet format used for import (CSV, XLSX, etc.)
- Whether the crash occurs with a smaller subset of the imported tasks (isolating the problematic records)
- OS and app version
