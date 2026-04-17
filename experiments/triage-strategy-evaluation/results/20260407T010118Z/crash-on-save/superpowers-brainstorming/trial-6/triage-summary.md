# Triage Summary

**Title:** App crashes on save when workspace contains CSV-imported tasks

## Problem
Saving any changes in the task view causes the app to crash when CSV-imported tasks are present. The crash is 100% reproducible and occurs every time the user clicks Save in the toolbar. The issue began after the user imported tasks from a CSV file. Removing the imported tasks eliminates the crash, confirming the imported data is the trigger.

## Root Cause Hypothesis
The CSV import accepts data that the save/serialization logic cannot handle. Likely candidates: special characters or encoding issues in text fields, missing required fields that the import doesn't validate but the save path assumes are present, or data type mismatches (e.g., a date field containing non-date text). The save operation processes all tasks in the view, so even if the user is editing an unrelated task, the crash occurs when the imported tasks are serialized.

## Reproduction Steps
  1. Start with a clean workspace with at least one manually-created task
  2. Verify that saving works normally
  3. Import tasks from a CSV file (try with various edge cases: special characters, empty required fields, long text, non-UTF8 encoding)
  4. Click Save in the toolbar
  5. Observe crash

## Environment
Not specified — appears to be a data-handling issue likely independent of environment

## Severity: high

## Impact
Any user who imports tasks via CSV risks making their entire workspace unsaveable, causing repeated data loss. The user has been blocked for days.

## Recommended Fix
1. Add input validation/sanitization to the CSV import pipeline — reject or clean data that doesn't conform to the task schema. 2. Make the save logic defensive against malformed task data (catch and isolate bad records rather than crashing the entire save). 3. Add a CSV import preview/validation step so users see problems before committing the import.

## Proposed Test Case
Import a CSV containing edge-case data (empty required fields, special characters, extremely long strings, malformed dates) and verify that (a) the import either rejects or sanitizes the data, and (b) the save operation completes without crashing even if malformed data is present.

## Information Gaps
- Exact CSV file contents or structure that triggered the issue
- Specific error message or stack trace from the crash
- Which platform/version of the app the user is running
