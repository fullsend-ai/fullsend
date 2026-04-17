# Triage Summary

**Title:** App crashes on manual Save after importing tasks from CSV — encoding error

## Problem
After importing approximately 200 tasks from a CSV file, clicking the Save button in the toolbar causes the application to crash. A brief error dialog referencing 'encoding' flashes before the app closes. Auto-save continues to function normally; only the manual Save button triggers the crash.

## Root Cause Hypothesis
The CSV import likely introduced task data containing characters in a non-UTF-8 encoding (or malformed multi-byte sequences). The manual Save button and auto-save use different serialization code paths — the manual save path likely attempts a full serialization or encoding conversion that chokes on the malformed data, while auto-save may do incremental or partial writes that bypass the problematic encoding step.

## Reproduction Steps
  1. Create or open a project in TaskFlow
  2. Import a large set of tasks (~200) from a CSV file
  3. Edit or view the imported task list
  4. Click the 'Save' button in the toolbar
  5. Observe the app crash with a brief encoding-related error dialog

## Environment
Not specified — appears to be the desktop/Electron app based on toolbar Save button and crash behavior

## Severity: high

## Impact
Users who import tasks from CSV files are unable to manually save their work, leading to data loss. The workaround (relying on auto-save) is fragile and not obvious to users.

## Recommended Fix
1. Compare the manual Save and auto-save code paths to identify where encoding handling differs. 2. Add proper encoding detection/conversion during CSV import (normalize to UTF-8 at ingest time). 3. Add error handling in the manual save path so encoding failures surface a readable error instead of crashing. 4. Consider validating encoding on import and warning the user about problematic characters.

## Proposed Test Case
Import a CSV file containing non-UTF-8 characters (e.g., Latin-1 accented characters, Windows-1252 smart quotes, or raw byte sequences) with 200+ tasks, then click the manual Save button. Verify the save completes without crashing and the data is preserved correctly.

## Information Gaps
- Exact encoding of the source CSV file and whether it contains non-ASCII characters
- Application platform/version and OS
- Whether the crash produces a crash log or stack trace beyond the flashed dialog
- Whether deleting the imported tasks restores normal save behavior
