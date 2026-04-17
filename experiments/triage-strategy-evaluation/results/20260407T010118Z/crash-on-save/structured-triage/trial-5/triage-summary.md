# Triage Summary

**Title:** Desktop app crashes on save after CSV import of ~200 tasks

## Problem
The TaskFlow desktop app on macOS crashes immediately (entire app closes) when the user clicks the Save button in the toolbar. A brief error dialog flashes but disappears too fast to read. The user loses unsaved work each time. The crash is 100% reproducible and began after the user imported a large number of tasks from a CSV file.

## Root Cause Hypothesis
The CSV import likely introduced data that triggers a failure during the save path — possible causes include malformed or oversized field values, unsupported characters, or a data volume issue (~200 tasks) that causes a serialization error, memory issue, or unhandled exception during save. The fact that saving worked before the import strongly points to the imported data itself.

## Reproduction Steps
  1. Import a large set of tasks (~200) from a CSV file
  2. Edit the task list
  3. Click the Save button in the toolbar
  4. Observe: app crashes immediately with a brief error dialog flash

## Environment
macOS Sonoma (exact version unconfirmed), TaskFlow desktop app (version unknown — user updated 'a while ago')

## Severity: high

## Impact
User loses all unsaved work on every save attempt. The app is effectively unusable for this user since they cannot persist any changes. Any user who imports a large CSV may hit the same issue.

## Recommended Fix
1. Attempt to reproduce with a large CSV import (~200 tasks) — try with varied data (special characters, long fields, empty fields). 2. Inspect the save/serialization code path for unhandled exceptions, particularly around data validation or size limits. 3. Check for uncaught exceptions in the Electron/native crash handler that could explain the disappearing error dialog. 4. Add proper error handling so save failures display a persistent error message instead of crashing the app.

## Proposed Test Case
Import a CSV file containing 200+ tasks (including edge-case data: special characters, very long text, empty optional fields) and verify that clicking Save completes successfully without crashing. Additionally, verify that if a save error does occur, it is caught and displayed in a persistent error dialog rather than crashing the app.

## Information Gaps
- Exact TaskFlow version (reporter declined to check)
- Crash log or stack trace (reporter declined to retrieve from Console.app)
- Contents/format of the original CSV file used for import
- Exact macOS version (reporter unsure if Sonoma or latest)
