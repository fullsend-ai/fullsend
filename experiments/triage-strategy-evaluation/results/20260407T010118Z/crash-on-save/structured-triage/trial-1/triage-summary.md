# Triage Summary

**Title:** App crashes immediately on save with large task list (~200 tasks, possibly after CSV import)

## Problem
TaskFlow desktop app closes instantly when the user clicks Save on their task list. A brief flash appears on screen (likely an unhandled error dialog) but disappears too quickly to read. The user loses unsaved work each time.

## Root Cause Hypothesis
The crash is likely triggered by saving a large number of tasks (~200), possibly related to data imported from a CSV file. The CSV import may have introduced malformed data, special characters, or a volume of records that exceeds a buffer or triggers an unhandled serialization error during the save operation.

## Reproduction Steps
  1. Create or import a large task list (~200 tasks) via CSV import in TaskFlow desktop app
  2. Click Save
  3. Observe that the app crashes immediately with a brief visual flash

## Environment
macOS 14.2 (Sonoma), TaskFlow desktop app, version approximately 2.3.x

## Severity: high

## Impact
User cannot save any work at all, resulting in complete data loss for the session. This is a total workflow blocker for any user with a similarly large or CSV-imported task list.

## Recommended Fix
Investigate the save/serialization path for large task lists. Specifically: (1) Check whether CSV-imported tasks contain data that fails validation or serialization on save (encoding issues, unexpected fields, oversized content). (2) Add a try/catch around the save operation to surface the error instead of crashing. (3) Test save behavior with 200+ tasks created manually vs. imported from CSV to isolate whether volume or import-specific data is the trigger.

## Proposed Test Case
Import a CSV file containing 200+ tasks into TaskFlow, then trigger a save. Verify that the save completes successfully without crashing. Additionally, test with CSVs containing edge-case data (special characters, very long fields, empty fields) to confirm robustness.

## Information Gaps
- Exact TaskFlow version (user said approximately 2.3.x)
- Crash log from macOS Console/crash reporter that would pinpoint the exact exception
- Whether the crash also occurs with a smaller number of tasks or only at scale
- Contents/format of the original CSV file used for import
