# Triage Summary

**Title:** Bulk save crashes when workspace contains CSV-imported tasks

## Problem
The application crashes every time the user saves after importing ~200 tasks from a CSV file. The save operation is a bulk save (all tasks at once), and the crash is 100% reproducible. Removing the imported tasks restores normal save functionality. The user's work is blocked because they need the imported tasks.

## Root Cause Hypothesis
The CSV import accepts data that the bulk save/serialization path cannot handle. Likely candidates: special characters (unescaped quotes, newlines, unicode), field values exceeding length limits, null or missing required fields, or data type mismatches (e.g., a string in a date field) that pass import validation but fail during save serialization.

## Reproduction Steps
  1. Start with a clean TaskFlow workspace where saving works normally
  2. Import a CSV file containing a non-trivial number of tasks (~200)
  3. Edit any task (or make no edits at all)
  4. Click Save — the app crashes
  5. Remove all imported tasks and try saving again — save succeeds

## Environment
Not specified; issue appears data-dependent rather than environment-dependent

## Severity: high

## Impact
User is completely blocked from saving any work as long as imported tasks exist in the workspace. Data loss occurs on each crash. Any user who imports tasks via CSV may hit this.

## Recommended Fix
1. Examine the bulk save serialization path for unhandled data types, missing null checks, or field length/format assumptions. 2. Compare the schema expectations of the save path against what the CSV importer actually produces — there is likely a validation gap where import accepts data that save cannot serialize. 3. Add defensive handling in the save path so malformed records fail gracefully (skip or flag) rather than crashing the entire operation. 4. Consider adding import-time validation that matches save-time constraints.

## Proposed Test Case
Import a CSV with edge-case values (special characters, very long strings, empty required fields, type mismatches) and verify that both import and subsequent bulk save complete without crashing. Additionally, test that a single malformed record does not prevent the rest from saving.

## Information Gaps
- Exact error message or stack trace from the crash
- Contents/schema of the CSV file used for import
- Application version and platform (web, desktop, mobile)
