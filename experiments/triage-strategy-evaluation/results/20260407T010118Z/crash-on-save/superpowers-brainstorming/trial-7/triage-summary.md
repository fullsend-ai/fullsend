# Triage Summary

**Title:** Mac desktop app crashes on any task save when CSV-imported tasks are present

## Problem
The Mac desktop app crashes every time the user saves any task (new or existing, any field). The crash began after the user imported ~200 tasks via CSV. Deleting the imported tasks restores normal save functionality. The crash is 100% reproducible.

## Root Cause Hypothesis
The CSV import introduced malformed or edge-case data (e.g., invalid characters, overlong fields, null values in required columns, or broken references) that the save codepath loads or validates globally. The save operation likely serializes or checks all tasks (or related indices) and chokes on the bad data, even when the user is editing an unrelated task.

## Reproduction Steps
  1. Open TaskFlow Mac desktop app with a fresh or empty project
  2. Import a batch of tasks from a CSV file (the reporter's CSV would be ideal)
  3. Edit any task (imported or newly created) and hit Save
  4. Observe crash

## Environment
Mac desktop app. Exact macOS version and app version unknown.

## Severity: critical

## Impact
User is completely unable to save any task edits, losing work repeatedly. The user's entire project (~200 tasks) depends on the imported data, so the workaround (deleting imports) is not viable. Deadline pressure.

## Recommended Fix
1. Obtain or recreate the reporter's CSV and inspect the imported data for malformed values (encoding issues, special characters, nulls, constraint violations). 2. Add a breakpoint or crash log capture on the save codepath to identify exactly where it fails when imported records are present. 3. Fix the save path to handle the problematic data gracefully, and consider adding CSV import validation to reject or sanitize bad data at import time.

## Proposed Test Case
Import a CSV containing edge-case data (special characters, empty required fields, very long strings, non-UTF-8 encoding) into a project with existing tasks. Verify that all tasks — both imported and pre-existing — can be edited and saved without crashing.

## Information Gaps
- Exact macOS version and app version
- Contents or format of the CSV file used for import
- Crash log or error message details
